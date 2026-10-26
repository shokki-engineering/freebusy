// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db is the reservation Store, written once against the generated
// repository so it runs on whichever backend the connection carries -- GORM or
// Hasura -- with no query of its own.
package db

import (
	"context"
	"slices"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/protobuf/proto"
)

// Store is the reservation Store over the generated repository. Transition,
// which speaks the service's Change, is in transition.go: the service package
// is named reservation too, so each file imports one of the two.
type Store struct {
	reservations reservation.ReservationRepository
	// lists serves List, the API's ListReservations: through the list cache
	// when the connection has one. Everything else reads reservations, so the
	// paths that decide something -- FindByHold, which keeps a retried create
	// from making a second reservation, and Stuck, which the reconciler acts
	// on -- always read the database.
	lists reservation.ReservationRepository
}

// New returns the Store on conn's backend, with List served through conn's
// list cache when it has one.
func New(conn *database.Connection) *Store {
	reservations := reservation.New(conn.Repox()).Reservations
	s := &Store{reservations: reservations, lists: reservations}
	if lists := conn.Caches.Reservations; lists != nil {
		s.lists = lists.Wrap(reservations)
	}
	return s
}

// Get returns one reservation.
func (s *Store) Get(ctx context.Context, name string) (*reservationpbv1.Reservation, error) {
	return s.reservations.Get(ctx, name)
}

// FindByHold returns the reservation a hold backs. A hold backs at most one:
// the reservation table's unique index on hold enforces it.
func (s *Store) FindByHold(ctx context.Context, hold string) (*reservationpbv1.Reservation, error) {
	found, _, err := s.reservations.List(ctx, repox.ListInput{
		PageSize: 1,
		Where:    []filterx.Condition{{Field: "hold", Op: filterx.OpEq, Value: hold}},
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, repox.ErrNotFound
	}
	return found[0], nil
}

// List returns a page of reservations.
func (s *Store) List(ctx context.Context, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
	return s.lists.List(ctx, in)
}

// Insert writes r exactly as given, server-owned fields included, its
// last_change CHANGE_STATE: being made is a change of state. A taken name or
// hold is repox.ErrAlreadyExists.
func (s *Store) Insert(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	r = proto.Clone(r).(*reservationpbv1.Reservation)
	r.LastChange = reservationpbv1.Reservation_CHANGE_STATE
	return s.reservations.Create(ctx, r)
}

// transition applies a change when the stored state is one of from and, when
// etag is set, the stored etag equals it. The write is guarded by the etag it
// read, so a state that changed between the read and the write is a conflict,
// not a lost update -- the compare that keeps two racing cancels, or a cancel
// and the reconciler, from both winning. next carries the fields to write,
// paths names them.
func (s *Store) transition(ctx context.Context, etag string, from []reservationpbv1.ReservationState, next *reservationpbv1.Reservation, paths []string) (*reservationpbv1.Reservation, error) {
	cur, err := s.reservations.Get(ctx, next.GetName())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(from, cur.GetState()) || (etag != "" && etag != cur.GetEtag()) {
		return nil, repox.ErrConflict
	}
	next.Etag = cur.GetEtag()
	return s.reservations.UpdateOutputOnly(ctx, next, paths)
}

// Update writes the caller-owned fields of r that paths names, through the
// generated field-mask update, when the stored state is one of from and, when
// r.Etag is set, the stored etag equals it. Like transition, the write is
// guarded by the etag it read, so a cancel landing between the read and the
// write is a conflict, never an update of a cancelled reservation. The same
// write sets last_change to CHANGE_DETAILS, which is how the webhook feed
// knows it for freebusy.reservation.updated.
func (s *Store) Update(ctx context.Context, r *reservationpbv1.Reservation, from []reservationpbv1.ReservationState, paths []string) (*reservationpbv1.Reservation, error) {
	cur, err := s.reservations.Get(ctx, r.GetName())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(from, cur.GetState()) || (r.GetEtag() != "" && r.GetEtag() != cur.GetEtag()) {
		return nil, repox.ErrConflict
	}
	next := proto.Clone(r).(*reservationpbv1.Reservation)
	next.Etag = cur.GetEtag()
	next.LastChange = reservationpbv1.Reservation_CHANGE_DETAILS
	return s.reservations.UpdateWithOutputOnly(ctx, next, paths, []string{"last_change"})
}

// Stuck returns up to limit reservations left PENDING,
// CANCELLING or RESCHEDULING since before cutoff, oldest first.
func (s *Store) Stuck(ctx context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error) {
	found, _, err := s.reservations.List(ctx, repox.ListInput{
		PageSize: int32(limit),
		OrderBy:  "update_time",
		Where: []filterx.Condition{
			filterx.In("state", "PENDING", "CANCELLING", "RESCHEDULING"),
			{Field: "update_time", Op: filterx.OpLt, Value: cutoff.UTC().Format(time.RFC3339Nano)},
		},
	})
	return found, err
}
