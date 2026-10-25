// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// updatable are the fields UpdateReservation may change: the ones the caller
// owns. Everything else -- the hold, its window and resources, the state and
// its times -- is the engine's, and moves only through Create, Cancel and
// Reschedule.
var updatable = []string{"customer", "attributes"}

// updateAttempts bounds how often update re-reads after losing a race it was
// not asked to detect: with no etag sent, the caller wants its change made on
// whatever is there, so a concurrent write is a reason to look again, not to
// fail -- unless that write cancelled or moved the reservation.
const updateAttempts = 3

// UpdateReservation changes a confirmed reservation's customer or attributes.
func (s *Server) UpdateReservation(ctx context.Context, req *reservationpbv1.UpdateReservationRequest) (*reservationpbv1.Reservation, error) {
	var out *reservationpbv1.Reservation
	err := rpc.Traced(ctx, "ReservationService", "UpdateReservation", func(ctx context.Context) error {
		r, err := s.update(ctx, req)
		out = r
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) update(ctx context.Context, req *reservationpbv1.UpdateReservationRequest) (*reservationpbv1.Reservation, error) {
	paths, err := updatePaths(req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	in := req.GetReservation()
	name := in.GetName()
	for attempt := 1; ; attempt++ {
		r, err := s.store.Get(ctx, name)
		if err != nil {
			return nil, err
		}
		// A reservation still confirming is finished first, as a cancel
		// does: its fate decides whether there is anything to update.
		if r.GetState() == reservationpbv1.ReservationState_RESERVATION_STATE_PENDING {
			if _, err := s.advance(ctx, r); err != nil && !errors.Is(err, servicex.ErrFailedPrecondition) {
				return nil, err
			}
			if r, err = s.store.Get(ctx, name); err != nil {
				return nil, err
			}
		}
		if err := updatableState(r); err != nil {
			return nil, err
		}
		out, err := s.store.Update(ctx, &reservationpbv1.Reservation{
			Name:       name,
			Customer:   in.GetCustomer(),
			Attributes: in.GetAttributes(),
			Etag:       in.GetEtag(),
		}, confirmed, paths)
		if errors.Is(err, repox.ErrConflict) && in.GetEtag() == "" && attempt < updateAttempts {
			continue
		}
		return out, err
	}
}

// updatableState is nil for a reservation UpdateReservation may change -- a
// confirmed one -- and otherwise says why not and what to do instead.
func updatableState(r *reservationpbv1.Reservation) error {
	switch r.GetState() {
	case reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED:
		return nil
	case reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED:
		return servicex.Blocked("RESERVATION_MOVED", hints.TypeOf(r.GetName()), r.GetName(),
			fmt.Sprintf("reservation %s was rescheduled to %s, which now holds the booking", r.GetName(), r.GetReplacement()),
			"Update the replacement instead: "+hints.Call("UpdateReservation", map[string]string{"reservation.name": r.GetReplacement()}))
	case reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING:
		return servicex.Blocked("RESCHEDULE_IN_PROGRESS", hints.TypeOf(r.GetName()), r.GetName(),
			fmt.Sprintf("reservation %s is being moved to %s, which takes its customer and attributes with it", r.GetName(), r.GetReplacement()),
			"Wait for that move to finish -- "+hints.Call("GetReservation", map[string]string{"name": r.GetName()})+
				" shows its state -- then update whichever reservation holds the booking")
	}
	return servicex.Blocked("RESERVATION_NOT_CONFIRMED", hints.TypeOf(r.GetName()), r.GetName(),
		fmt.Sprintf("reservation %s is %s, and only a confirmed reservation can be updated", r.GetName(),
			strings.ToLower(strings.TrimPrefix(r.GetState().String(), "RESERVATION_STATE_"))),
		"A cancelled or failed reservation is kept as it ended; create a new reservation to book again")
}

// updatePaths resolves UpdateReservation's update_mask against the updatable
// fields. The mask is required -- an update never guesses from the fields
// that happen to be set -- and "*" is every updatable field.
func updatePaths(mask *fieldmaskpb.FieldMask) ([]string, error) {
	if len(mask.GetPaths()) == 0 {
		return nil, servicex.BadField("REQUIRED_FIELD", "update_mask",
			"update_mask is empty, so the update does not say which fields to change",
			fmt.Sprintf("Name the fields to change in update_mask: %s", strings.Join(updatable, ", ")))
	}
	if len(mask.GetPaths()) == 1 && mask.GetPaths()[0] == "*" {
		return slices.Clone(updatable), nil
	}
	// A path may be the field's name or, as REST clients send it, its JSON
	// name (AIP-161); either way only a top-level updatable field is taken.
	fields := (&reservationpbv1.Reservation{}).ProtoReflect().Descriptor().Fields()
	paths := make([]string, 0, len(mask.GetPaths()))
	for _, p := range mask.GetPaths() {
		fd := fields.ByName(protoreflect.Name(p))
		if fd == nil {
			fd = fields.ByJSONName(p)
		}
		if fd == nil || !slices.Contains(updatable, string(fd.Name())) {
			return nil, servicex.BadField("UNKNOWN_MASK_PATH", "update_mask",
				fmt.Sprintf("update_mask names %q, which a reservation update cannot change", p),
				fmt.Sprintf("Updatable fields are %s; the time and resources change with %s, the state with %s",
					strings.Join(updatable, ", "), "RescheduleReservation", "CancelReservation"))
		}
		if !slices.Contains(paths, string(fd.Name())) {
			paths = append(paths, string(fd.Name()))
		}
	}
	return paths, nil
}
