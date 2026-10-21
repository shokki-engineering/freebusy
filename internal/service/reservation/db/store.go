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
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
)

// Store is the reservation Store over the generated repository. Transition,
// which speaks the service's Change, is in transition.go: the service package
// is named reservation too, so each file imports one of the two.
type Store struct {
	reservations reservation.ReservationRepository
}

// New returns the Store on conn's backend.
func New(conn *database.Connection) *Store {
	return &Store{reservations: reservation.New(conn.Repox()).Reservations}
}

// acrossSites is the AIP-159 wildcard parent: reservations of every site.
const acrossSites = "organizationalUnits/-"

// Get returns one reservation.
func (s *Store) Get(ctx context.Context, name string) (*reservationpbv1.Reservation, error) {
	return s.reservations.Get(ctx, name)
}

// FindByHold returns the reservation a hold backs. A hold backs at most one:
// the reservation table's unique index on hold enforces it.
func (s *Store) FindByHold(ctx context.Context, hold string) (*reservationpbv1.Reservation, error) {
	found, _, err := s.reservations.List(ctx, acrossSites, repox.ListInput{
		PageSize: 1,
		Where:    []filterx.Condition{{Field: "hold", Op: filterx.OpEq, Value: hold}},
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, types.ErrNotFound
	}
	return found[0], nil
}

// List returns a page of a site's reservations.
func (s *Store) List(ctx context.Context, parent string, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
	return s.reservations.List(ctx, parent, in)
}

// Insert writes r exactly as given, server-owned fields included; a taken
// name or hold is types.ErrAlreadyExists.
func (s *Store) Insert(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	return s.reservations.Create(ctx, siteOf(r.GetName()), r)
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
		return nil, types.ErrConflict
	}
	next.Etag = cur.GetEtag()
	return s.reservations.UpdateOutputOnly(ctx, next, paths)
}

// Stuck returns up to limit reservations, across every site, left PENDING,
// CANCELLING or RESCHEDULING since before cutoff, oldest first.
func (s *Store) Stuck(ctx context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error) {
	found, _, err := s.reservations.List(ctx, acrossSites, repox.ListInput{
		PageSize: int32(limit),
		OrderBy:  "update_time",
		Where: []filterx.Condition{
			filterx.In("state", "PENDING", "CANCELLING", "RESCHEDULING"),
			{Field: "update_time", Op: filterx.OpLt, Value: cutoff.UTC().Format(time.RFC3339Nano)},
		},
	})
	return found, err
}

// siteOf is the site a reservation name sits under.
// "organizationalUnits/o/reservations/r" -> "organizationalUnits/o".
func siteOf(name string) string {
	n := 0
	for i := range len(name) {
		if name[i] == '/' {
			if n++; n == 2 {
				return name[:i]
			}
		}
	}
	return name
}
