// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package reservation is ReservationService: the last step of booking, where a
// live hold becomes a confirmed reservation, and the step back out again.
//
// It owns one table and drives the engine in-process. Confirming a reservation
// takes two writes -- the reservation row and the hold's conversion -- and so
// does cancelling one, and on Hasura no transaction can span both. Every
// transition is therefore recorded before it is carried out (PENDING,
// CANCELLING), in an order where a crash can only ever leave a resource taken
// for too long, never sold twice; the reconciler then finishes the job.
package reservation

import (
	"context"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
)

// Change is one state transition: the new state and the fields that move with
// it. Zero fields are left as stored.
type Change struct {
	State        reservationpbv1.ReservationState
	ConfirmTime  *time.Time
	CancelTime   *time.Time
	CancelReason string
	// Replacement sets replacement when non-nil; pointing at "" clears it, which
	// is how a move that fell through is undone.
	Replacement *string
}

// Store persists reservations. Implementations exist for GORM and Hasura;
// errors are the repositories' sentinels (repox).
type Store interface {
	// Get returns one reservation, or repox.ErrNotFound.
	Get(ctx context.Context, name string) (*reservationpbv1.Reservation, error)

	// FindByHold returns the reservation a hold backs, or repox.ErrNotFound.
	// A hold backs at most one: the stores enforce it with a unique index.
	FindByHold(ctx context.Context, hold string) (*reservationpbv1.Reservation, error)

	// List returns a page of a site's reservations, AIP-160 filtered.
	List(ctx context.Context, parent string, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error)

	// Insert writes a new reservation exactly as given (name included), or
	// fails with repox.ErrAlreadyExists when the name, or the hold, is taken.
	Insert(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error)

	// Transition applies c if the stored state is one of from and, when etag
	// is non-empty, the stored etag equals it; otherwise repox.ErrConflict.
	// It is the only write after Insert, and the compare is what makes two
	// racing cancels, or a cancel racing the reconciler, safe.
	Transition(ctx context.Context, name, etag string, from []reservationpbv1.ReservationState, c Change) (*reservationpbv1.Reservation, error)

	// Update writes the fields of r that paths names -- customer, attributes,
	// the ones a caller owns -- if the stored state is one of from and, when
	// r.Etag is non-empty, the stored etag equals it; otherwise
	// repox.ErrConflict. It is UpdateReservation's write, and the only one
	// that changes a field the engine does not own.
	Update(ctx context.Context, r *reservationpbv1.Reservation, from []reservationpbv1.ReservationState, paths []string) (*reservationpbv1.Reservation, error)

	// Stuck returns up to limit reservations, across every site, left PENDING,
	// CANCELLING or RESCHEDULING since before cutoff.
	Stuck(ctx context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error)
}

// Holds is the part of the engine a reservation drives, implemented by
// *allocation.Server.
type Holds interface {
	LookupHold(ctx context.Context, name string) (*allocationpbv1.Hold, error)
	ConvertHold(ctx context.Context, name string) (*allocationpbv1.Hold, error)
	EndConversion(ctx context.Context, name string, at time.Time) (*allocationpbv1.Hold, error)
	// HandOver is EndConversion of from for a reschedule onto to: when to
	// replaces from, the time they share passes to to in the same write.
	HandOver(ctx context.Context, from, to string, at time.Time) (*allocationpbv1.Hold, error)
}
