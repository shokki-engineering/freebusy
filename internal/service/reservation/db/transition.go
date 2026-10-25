// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"

	"github.com/shokki-engineering/freebusy/internal/service/reservation"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ reservation.Store = (*Store)(nil)

// Transition applies c if the stored state is one of from and, when etag is
// set, the stored etag equals it; otherwise repox.ErrConflict.
func (s *Store) Transition(ctx context.Context, name, etag string, from []reservationpbv1.ReservationState, c reservation.Change) (*reservationpbv1.Reservation, error) {
	// Every transition is a change of state: the webhook feed reads
	// last_change to tell it from UpdateReservation's change of details.
	next := &reservationpbv1.Reservation{Name: name, State: c.State, LastChange: reservationpbv1.Reservation_CHANGE_STATE}
	paths := []string{"state", "last_change"}
	if c.ConfirmTime != nil {
		next.ConfirmTime = timestamppb.New(*c.ConfirmTime)
		paths = append(paths, "confirm_time")
	}
	if c.CancelTime != nil {
		next.CancelTime = timestamppb.New(*c.CancelTime)
		paths = append(paths, "cancel_time")
	}
	if c.CancelReason != "" {
		next.CancelReason = c.CancelReason
		paths = append(paths, "cancel_reason")
	}
	if c.Replacement != nil {
		// "" clears it: a move that fell through is undone.
		next.Replacement = *c.Replacement
		paths = append(paths, "replacement")
	}
	return s.transition(ctx, etag, from, next, paths)
}
