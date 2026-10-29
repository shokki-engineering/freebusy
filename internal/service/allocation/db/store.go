// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db is the availability engine's Store, written once against the
// generated repositories so it runs on whichever backend the connection
// carries -- GORM or Hasura -- with no query of its own.
//
// The race for the last resource is not settled here. The allocation table
// declares (store.v1.table).no_overlap, so the database refuses a second claim
// on overlapping time for one resource whatever the writers do; this package
// writes a hold and its claims in one Batch and reports the refusal as a
// conflict, which the engine answers by re-planning.
//
// An allocation row is a live claim and nothing else: it is written with its
// hold and removed when the hold is released, expires or is deleted, so the
// constraint only ever compares claims that still count. A row whose hold
// lapsed but has not been swept yet is cleared by the next hold that wants the
// time, in the same batch as that hold's insert.
//
// This file holds the writes and the hold reads, over the generated allocation
// repository; reads.go holds the engine's reads, over the service's types. The
// two packages are both named allocation, so each file imports one of them.
package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store is the engine's Store over the generated repositories.
type Store struct {
	conn           repox.Conn
	holds          allocation.HoldRepository
	allocations    allocation.AllocationRepository
	resources      resource.ResourceRepository
	calendars      calendar.CalendarRepository
	availabilities availability.AvailabilityRepository
	events         event.EventRepository
	// version moves on every write to the engine's static inputs.
	version *inputVersion
}

// New returns the Store on conn's backend, observing conn's writes so its
// InputVersion moves with them.
func New(conn *database.Connection) *Store {
	v := &inputVersion{}
	if p := conn.CacheProvider(); p != nil {
		if db, err := p.SetDatabase(context.Background(), inputsNamespace); err == nil {
			v.shared = db
		} else {
			telemetry.Warn("Engine input version is per process: the cache database could not be opened", "error", err)
		}
	}
	conn.Observe(v)
	s := On(conn.Repox())
	s.version = v
	return s
}

// On returns the Store over c alone: its reads and writes go through c, and
// nothing observes them, so its InputVersion never moves. The read model's
// writer reads through it inside the write it projects (c is that write's
// transaction on GORM).
func On(c repox.Conn) *Store {
	allocs := allocation.New(c)
	return &Store{
		conn:           c,
		holds:          allocs.Holds,
		allocations:    allocs.Allocations,
		resources:      resource.New(c).Resources,
		calendars:      calendar.New(c).Calendars,
		availabilities: availability.New(c).Availabilities,
		events:         event.New(c).Events,
		version:        &inputVersion{},
	}
}

// listAll drains a paged List.
func listAll[T any](list func(token string) ([]T, string, error)) ([]T, error) {
	var out []T
	for token := ""; ; {
		page, next, err := list(token)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if token = next; token == "" {
			return out, nil
		}
	}
}

// claimsOverlapping returns the allocation rows on resources overlapping
// window: live or lapsed, which the caller tells apart by expiry.
func (s *Store) claimsOverlapping(ctx context.Context, resources []string, window [2]time.Time) ([]*allocationpbv1.Allocation, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	return s.allocations.ListByKeys(ctx, "resource", resources, repox.ListInput{
		Where: []filterx.Condition{
			{Field: "start_time", Op: filterx.OpLt, Value: window[1].UTC().Format(time.RFC3339Nano)},
			{Field: "end_time", Op: filterx.OpGt, Value: window[0].UTC().Format(time.RFC3339Nano)},
		},
	})
}

// InsertHold writes hold and one allocation per resource it took, in one
// Batch. Lapsed claims in the way are deleted in the same batch, so a hold
// that expired unswept never blocks one that wants its time. A live claim in
// the way makes the no_overlap constraint refuse the batch: repox.ErrConflict.
func (s *Store) InsertHold(ctx context.Context, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	window := [2]time.Time{h.GetWindow().GetStartTime().AsTime(), h.GetWindow().GetEndTime().AsTime()}
	for attempt := 1; ; attempt++ {
		name, err := s.insertHold(ctx, h, window)
		if err == nil {
			return s.holds.Get(ctx, name)
		}
		if errors.Is(err, repox.ErrConflict) || errors.Is(err, repox.ErrAlreadyExists) {
			return nil, err
		}
		if rival, ok := s.rivalClaim(ctx, h, window); ok {
			return nil, fmt.Errorf("%w: %s was taken while this hold was written", repox.ErrConflict, rival)
		}
		// No rival to be seen: the insert failed for a reason the backend did
		// not name -- a GraphQL engine hides the database's. Writers of one
		// resource take turns (the no_overlap lock trigger), so this is not
		// the race; retrying once or twice rides out a transient fault. The
		// batch is atomic, so a failed attempt left nothing behind.
		if attempt == insertAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(time.Duration(attempt) * 25 * time.Millisecond):
		}
	}
}

// insertAttempts bounds InsertHold's retries of an insert that failed with no
// rival in sight.
const insertAttempts = 3

// insertHold is one attempt: the hold and its allocations, in one batch with
// the deletion of lapsed claims in the way, so the exclusion constraint never
// sees them.
func (s *Store) insertHold(ctx context.Context, h *allocationpbv1.Hold, window [2]time.Time) (string, error) {
	inWay, err := s.claimsOverlapping(ctx, h.GetResources(), window)
	if err != nil {
		return "", err
	}
	now := time.Now()
	b := allocation.NewBatch(s.conn)
	// What a replaced booking keeps stays its own until HandOver: this hold
	// claims the rest of its window.
	kept := map[string]engine.Spans{}
	for _, a := range inWay {
		switch {
		case h.GetReplaces() != "" && a.GetHold() == h.GetReplaces():
			kept[a.GetResource()] = kept[a.GetResource()].Union(engine.Spans{spanOf(a)})
		case a.GetExpireTime() != nil && !a.GetExpireTime().AsTime().After(now):
			b.DeleteAllocation(a.GetName())
		}
	}
	name, err := b.CreateHold(h)
	if err != nil {
		return "", err
	}
	for _, r := range h.GetResources() {
		for _, sp := range (engine.Spans{{Start: window[0], End: window[1]}}).Subtract(kept[r]) {
			if _, err := b.CreateAllocation(&allocationpbv1.Allocation{
				Resource:   r,
				StartTime:  timestamppb.New(sp.Start),
				EndTime:    timestamppb.New(sp.End),
				ExpireTime: h.GetExpireTime(),
				Hold:       name,
			}); err != nil {
				return "", err
			}
		}
	}
	return name, b.Commit(ctx)
}

// spanOf is the time an allocation row claims.
func spanOf(a *allocationpbv1.Allocation) engine.Span {
	return engine.Span{Start: a.GetStartTime().AsTime(), End: a.GetEndTime().AsTime()}
}

// HandOver ends the converted hold from as of at, guarded by etag, and gives
// hold to the rest of its window, in one batch: from's claims on to's
// resources across to's window are deleted -- they would collide with to's
// under the no_overlap constraint -- its other claims take the expiry at,
// and to gains a claim for every part of its window it does not yet hold on
// each of its resources.
func (s *Store) HandOver(ctx context.Context, from, etag, to string, at time.Time) (*allocationpbv1.Hold, error) {
	next, err := s.holds.Get(ctx, to)
	if err != nil {
		return nil, err
	}
	window := engine.Span{Start: next.GetWindow().GetStartTime().AsTime(), End: next.GetWindow().GetEndTime().AsTime()}
	old, err := s.claimsOf(ctx, from)
	if err != nil {
		return nil, err
	}
	have, err := s.claimsOf(ctx, to)
	if err != nil {
		return nil, err
	}
	end := timestamppb.New(at)
	b := allocation.NewBatch(s.conn)
	b.UpdateHoldOutputOnly(&allocationpbv1.Hold{Name: from, Etag: etag, State: allocationpbv1.HoldState_HOLD_STATE_CONVERTED, ExpireTime: end}, []string{"state", "expire_time"})
	for _, a := range old {
		if slices.Contains(next.GetResources(), a.GetResource()) && spanOf(a).Overlaps(window) {
			b.DeleteAllocation(a.GetName())
		} else {
			b.UpdateAllocationOutputOnly(&allocationpbv1.Allocation{Name: a.GetName(), ExpireTime: end}, []string{"expire_time"})
		}
	}
	held := map[string]engine.Spans{}
	for _, a := range have {
		held[a.GetResource()] = held[a.GetResource()].Union(engine.Spans{spanOf(a)})
	}
	for _, r := range next.GetResources() {
		for _, sp := range (engine.Spans{window}).Subtract(held[r]) {
			if _, err := b.CreateAllocation(&allocationpbv1.Allocation{
				Resource:   r,
				StartTime:  timestamppb.New(sp.Start),
				EndTime:    timestamppb.New(sp.End),
				ExpireTime: next.GetExpireTime(),
				Hold:       to,
			}); err != nil {
				return nil, err
			}
		}
	}
	if err := b.Commit(ctx); err != nil {
		return nil, err
	}
	return s.holds.Get(ctx, from)
}

// rivalClaim names a resource another hold now claims, live, across window.
func (s *Store) rivalClaim(ctx context.Context, h *allocationpbv1.Hold, window [2]time.Time) (string, bool) {
	now := time.Now()
	claims, err := s.claimsOverlapping(ctx, h.GetResources(), window)
	if err != nil {
		return "", false
	}
	for _, a := range claims {
		live := a.GetExpireTime() == nil || a.GetExpireTime().AsTime().After(now)
		if live && a.GetHold() != h.GetName() && (h.GetReplaces() == "" || a.GetHold() != h.GetReplaces()) {
			return a.GetResource(), true
		}
	}
	return "", false
}

// GetHold reads one hold, as stored.
func (s *Store) GetHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	return s.holds.Get(ctx, name)
}

// FindHold returns the latest hold carrying idempotency key: a key's earlier
// holds are ones that lapsed before it was retried.
func (s *Store) FindHold(ctx context.Context, key string) (*allocationpbv1.Hold, error) {
	found, _, err := s.holds.List(ctx, repox.ListInput{
		PageSize: 1,
		OrderBy:  "create_time desc",
		Where:    []filterx.Condition{{Field: "idempotency_key", Op: filterx.OpEq, Value: key}},
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, repox.ErrNotFound
	}
	return found[0], nil
}

// ListHolds pages holds.
func (s *Store) ListHolds(ctx context.Context, in repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	return s.holds.List(ctx, in)
}

// UpdateHold moves a hold to state with expiry expire, guarded by etag, and
// brings its claims along in the same batch: an active or converted hold's
// claims take the new expiry (nil for a conversion, which keeps them for
// good); any other state gives them up.
func (s *Store) UpdateHold(ctx context.Context, name, etag string, state allocationpbv1.HoldState, expire *time.Time) (*allocationpbv1.Hold, error) {
	claims, err := s.claimsOf(ctx, name)
	if err != nil {
		return nil, err
	}
	var exp *timestamppb.Timestamp
	if expire != nil {
		exp = timestamppb.New(*expire)
	}
	b := allocation.NewBatch(s.conn)
	b.UpdateHoldOutputOnly(&allocationpbv1.Hold{Name: name, Etag: etag, State: state, ExpireTime: exp}, []string{"state", "expire_time"})
	keep := state == allocationpbv1.HoldState_HOLD_STATE_ACTIVE || state == allocationpbv1.HoldState_HOLD_STATE_CONVERTED
	for _, a := range claims {
		if keep {
			b.UpdateAllocationOutputOnly(&allocationpbv1.Allocation{Name: a.GetName(), ExpireTime: exp}, []string{"expire_time"})
		} else {
			b.DeleteAllocation(a.GetName())
		}
	}
	if err := b.Commit(ctx); err != nil {
		return nil, err
	}
	return s.holds.Get(ctx, name)
}

// DeleteHold removes a hold and its claims, guarded by etag.
func (s *Store) DeleteHold(ctx context.Context, name, etag string) error {
	h, err := s.holds.Get(ctx, name)
	if err != nil {
		return err
	}
	claims, err := s.claimsOf(ctx, name)
	if err != nil {
		return err
	}
	b := allocation.NewBatch(s.conn)
	if etag != "" {
		// The guard: an update that changes nothing but fails on a stale
		// etag, ahead of the deletes.
		b.UpdateHoldOutputOnly(&allocationpbv1.Hold{Name: name, Etag: etag, State: h.GetState()}, []string{"state"})
	}
	for _, a := range claims {
		b.DeleteAllocation(a.GetName())
	}
	b.DeleteHold(name)
	return b.Commit(ctx)
}

// ExpireHolds marks every ACTIVE hold whose expiry has passed EXPIRED and
// drops its claims, then sweeps any claim whose expiry has passed whatever its
// hold says -- the remains of a batch that failed half way. Both steps are
// idempotent, so a sweep interrupted is finished by the next.
func (s *Store) ExpireHolds(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC().Format(time.RFC3339Nano)
	lapsed, err := listAll(func(token string) ([]*allocationpbv1.Hold, string, error) {
		return s.holds.List(ctx, repox.ListInput{
			PageSize:  1000,
			PageToken: token,
			Where: []filterx.Condition{
				{Field: "state", Op: filterx.OpEq, Value: "ACTIVE"},
				{Field: "expire_time", Op: filterx.OpLte, Value: cutoff},
			},
		})
	})
	if err != nil {
		return 0, err
	}
	var n int64
	for _, h := range lapsed {
		b := allocation.NewBatch(s.conn)
		b.UpdateHoldOutputOnly(&allocationpbv1.Hold{
			Name: h.GetName(), Etag: h.GetEtag(), State: allocationpbv1.HoldState_HOLD_STATE_EXPIRED, ExpireTime: h.GetExpireTime(),
		}, []string{"state"})
		switch err := b.Commit(ctx); {
		case err == nil:
			n++
		case errors.Is(err, repox.ErrConflict), errors.Is(err, repox.ErrNotFound):
			// Changed or gone under us: whoever did it owns it now.
		default:
			return n, err
		}
	}
	stale, err := listAll(func(token string) ([]*allocationpbv1.Allocation, string, error) {
		return s.allocations.List(ctx, repox.ListInput{
			PageSize:  1000,
			PageToken: token,
			Where:     []filterx.Condition{{Field: "expire_time", Op: filterx.OpLte, Value: cutoff}},
		})
	})
	if err != nil {
		return n, err
	}
	if len(stale) > 0 {
		b := allocation.NewBatch(s.conn)
		for _, a := range stale {
			b.DeleteAllocation(a.GetName())
		}
		if err := b.Commit(ctx); err != nil {
			return n, err
		}
	}
	return n, nil
}

// claimsOf returns the allocation rows a hold holds.
func (s *Store) claimsOf(ctx context.Context, hold string) ([]*allocationpbv1.Allocation, error) {
	return listAll(func(token string) ([]*allocationpbv1.Allocation, string, error) {
		return s.allocations.List(ctx, repox.ListInput{
			PageSize:  1000,
			PageToken: token,
			Where:     []filterx.Condition{{Field: "hold", Op: filterx.OpEq, Value: hold}},
		})
	})
}
