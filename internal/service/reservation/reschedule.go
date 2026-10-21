// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
)

// RescheduleReservation moves a confirmed reservation onto a new hold.
func (s *Server) RescheduleReservation(ctx context.Context, req *reservationpbv1.RescheduleReservationRequest) (*reservationpbv1.Reservation, error) {
	var out *reservationpbv1.Reservation
	err := rpc.Traced(ctx, "ReservationService", "RescheduleReservation", func(ctx context.Context) error {
		r, err := s.reschedule(ctx, req)
		out = r
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// rescheduleAttempts bounds how often reschedule re-reads after losing a race
// to mark the original; each loss means another caller moved it on, and the
// re-read decides whether that caller did this caller's work.
const rescheduleAttempts = 3

// reschedule is a move in three recorded steps, ordered so the customer always
// holds at least one of the two slots:
//
//  1. The original is marked RESCHEDULING, naming its replacement. It keeps
//     its resources.
//  2. The replacement is made from the new hold, through the same path as
//     CreateReservation.
//  3. Only once the replacement is CONFIRMED is the original's hold ended and
//     the original marked RESCHEDULED. If the replacement could not be made --
//     its hold lapsed -- the original goes back to CONFIRMED instead.
//
// A crash between any two steps leaves the original RESCHEDULING, which the
// reconciler carries forward with exactly this logic (finishReschedule).
func (s *Server) reschedule(ctx context.Context, req *reservationpbv1.RescheduleReservationRequest) (*reservationpbv1.Reservation, error) {
	site := "organizationalUnits/" + SiteID(req.GetName())
	replacement, err := reservationFor(site, req.GetHold())
	if err != nil {
		return nil, err
	}
	// If the new hold already backs a reservation -- the caller reserved it,
	// or an earlier attempt at this move got that far -- that is the
	// replacement, whatever it was named.
	if existing, err := s.store.FindByHold(ctx, req.GetHold()); err == nil {
		replacement = existing.GetName()
	} else if !errors.Is(err, types.ErrNotFound) {
		return nil, err
	}
	if replacement == req.GetName() {
		return nil, types.BadField("SAME_HOLD", "hold",
			fmt.Sprintf("reservation %s was made from %s, so moving it there changes nothing", req.GetName(), req.GetHold()),
			"Take a new hold for the new window with "+hints.Call("CreateHold", map[string]string{"parent": siteOf(req.GetName())})+
				" and reschedule onto that")
	}

	for attempt := 1; ; attempt++ {
		r, err := s.store.Get(ctx, req.GetName())
		if err != nil {
			return nil, err
		}
		switch r.GetState() {
		case reservationpbv1.ReservationState_RESERVATION_STATE_PENDING:
			if _, err := s.advance(ctx, r); err != nil {
				return nil, err
			}
			continue
		case reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING,
			reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED:
			if r.GetReplacement() != replacement {
				return nil, types.Blocked("RESCHEDULE_IN_PROGRESS", hints.TypeOf(r.GetName()), r.GetName(),
					fmt.Sprintf("reservation %s is already being moved to %s", r.GetName(), r.GetReplacement()),
					"Wait for that move to finish -- "+hints.Call("GetReservation", map[string]string{"name": r.GetName()})+
						" shows its state -- then reschedule "+r.GetReplacement()+" if it should move again")
			}
			// A retry of this same move: finish it and answer as the first
			// call would have.
			return s.finishReschedule(ctx, r)
		case reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED:
		default:
			return nil, types.Blocked("RESERVATION_NOT_CONFIRMED", hints.TypeOf(r.GetName()), r.GetName(),
				fmt.Sprintf("reservation %s is %s, and only a confirmed reservation can be moved", r.GetName(),
					strings.ToLower(strings.TrimPrefix(r.GetState().String(), "RESERVATION_STATE_"))),
				"Create a new reservation from your new hold instead")
		}

		marked, err := s.store.Transition(ctx, r.GetName(), req.GetEtag(), confirmed, Change{
			State:       reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING,
			Replacement: &replacement,
		})
		if errors.Is(err, types.ErrConflict) && req.GetEtag() == "" && attempt < rescheduleAttempts {
			continue
		}
		if err != nil {
			return nil, err
		}
		return s.finishReschedule(ctx, marked)
	}
}

// finishReschedule carries a RESCHEDULING reservation to its end and returns
// the replacement, or reverts the original and returns why.
func (s *Server) finishReschedule(ctx context.Context, old *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	replacement := old.GetReplacement()
	if old.GetState() == reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED {
		return s.store.Get(ctx, replacement)
	}

	site := "organizationalUnits/" + SiteID(old.GetName())
	newHold, err := s.replacementHold(ctx, old)
	if err != nil {
		return nil, err
	}
	rep, err := s.open(ctx, site, newHold, lastSegment(replacement), &reservationpbv1.Reservation{
		Customer:   old.GetCustomer(),
		Attributes: old.GetAttributes(),
		Replaces:   old.GetName(),
	})
	switch {
	case errors.Is(err, types.ErrInvalidState) || errors.Is(err, types.ErrNotFound):
		// The new hold lapsed or vanished before it could be converted.
		return nil, s.revertReschedule(ctx, old)
	case err != nil:
		return nil, err
	case rep.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED:
		// The replacement exists but does not hold the booking -- it failed,
		// or was cancelled mid-move. Letting go of the original now would
		// leave the customer with nothing.
		return nil, s.revertReschedule(ctx, old)
	}

	// The replacement is confirmed: now, and only now, let go of the old slot.
	at := s.now()
	if _, err := s.holds.EndConversion(ctx, old.GetHold(), at); err != nil && !errors.Is(err, types.ErrNotFound) {
		return nil, err
	}
	if _, err := s.store.Transition(ctx, old.GetName(), "", rescheduling, Change{
		State: reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED,
	}); err != nil && !errors.Is(err, types.ErrConflict) {
		return nil, err
	}
	return rep, nil
}

// revertReschedule calls a move off: the original, which never let go of its
// slot, goes back to CONFIRMED with replacement cleared.
func (s *Server) revertReschedule(ctx context.Context, old *reservationpbv1.Reservation) error {
	empty := ""
	if _, err := s.store.Transition(ctx, old.GetName(), "", rescheduling, Change{
		State:       reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED,
		Replacement: &empty,
	}); err != nil && !errors.Is(err, types.ErrConflict) {
		return err
	}
	return types.Blocked("REPLACEMENT_FAILED", hints.TypeOf(old.GetName()), old.GetName(),
		fmt.Sprintf("the new slot could not be booked, so reservation %s keeps its original one", old.GetName()),
		"Its new hold most likely lapsed: take a fresh hold with "+
			hints.Call("CreateHold", map[string]string{"parent": siteOf(old.GetName())})+" and reschedule again")
}

// replacementHold is the hold a move is onto. A replacement that exists says
// so itself; one not yet made is named after its hold, by reschedule.
func (s *Server) replacementHold(ctx context.Context, old *reservationpbv1.Reservation) (string, error) {
	rep, err := s.store.Get(ctx, old.GetReplacement())
	switch {
	case err == nil:
		return rep.GetHold(), nil
	case errors.Is(err, types.ErrNotFound):
		return HoldName(SiteID(old.GetName()), lastSegment(old.GetReplacement())), nil
	}
	return "", err
}

func lastSegment(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}
