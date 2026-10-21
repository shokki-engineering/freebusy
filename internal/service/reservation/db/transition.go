package db

import (
	"context"

	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ reservation.Store = (*Store)(nil)

// Transition applies c if the stored state is one of from and, when etag is
// set, the stored etag equals it; otherwise types.ErrConflict.
func (s *Store) Transition(ctx context.Context, name, etag string, from []reservationpbv1.ReservationState, c reservation.Change) (*reservationpbv1.Reservation, error) {
	next := &reservationpbv1.Reservation{Name: name, State: c.State}
	paths := []string{"state"}
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
