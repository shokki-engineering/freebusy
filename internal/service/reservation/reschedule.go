package reservation

import (
	"context"
	"errors"

	"github.com/oh-tarnished/freebusy/internal/runtime/rpc"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
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
	if replacement == req.GetName() {
		return nil, errorf(types.ErrInvalidArgument, "%s already holds %s; take a new hold for the new window", req.GetName(), req.GetHold())
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
			if r.GetReplacedBy() != replacement {
				return nil, errorf(types.ErrInvalidState, "%s is already being moved to %s", r.GetName(), r.GetReplacedBy())
			}
			// A retry of this same move: finish it and answer as the first
			// call would have.
			return s.finishReschedule(ctx, r)
		case reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED:
		default:
			return nil, errorf(types.ErrInvalidState, "only a confirmed reservation can be moved; %s is %s", r.GetName(), r.GetState())
		}

		marked, err := s.store.Transition(ctx, r.GetName(), req.GetEtag(), confirmed, Change{
			State:      reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING,
			ReplacedBy: &replacement,
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
	replacement := old.GetReplacedBy()
	if old.GetState() == reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED {
		return s.store.Get(ctx, replacement)
	}

	site := "organizationalUnits/" + SiteID(old.GetName())
	rep, err := s.open(ctx, site, HoldOf(replacement), &reservationpbv1.Reservation{
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
// slot, goes back to CONFIRMED with replaced_by cleared.
func (s *Server) revertReschedule(ctx context.Context, old *reservationpbv1.Reservation) error {
	empty := ""
	if _, err := s.store.Transition(ctx, old.GetName(), "", rescheduling, Change{
		State:      reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED,
		ReplacedBy: &empty,
	}); err != nil && !errors.Is(err, types.ErrConflict) {
		return err
	}
	return errorf(types.ErrInvalidState, "the new hold %s can no longer be booked; %s keeps its original slot", HoldOf(old.GetReplacedBy()), old.GetName())
}
