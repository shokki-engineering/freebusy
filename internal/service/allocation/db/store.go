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
	"time"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/filterx"
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/availability"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/calendar"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/event"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/orgunit"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/resource"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
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
	units          orgunit.OrganizationalUnitRepository
}

// New returns the Store on conn's backend.
func New(conn *database.Connection) *Store {
	c := conn.Repox()
	allocs := allocation.New(c)
	return &Store{
		conn:           c,
		holds:          allocs.Holds,
		allocations:    allocs.Allocations,
		resources:      resource.New(c).Resources,
		calendars:      calendar.New(c).Calendars,
		availabilities: availability.New(c).Availabilities,
		events:         event.New(c).Events,
		units:          orgunit.New(c).OrganizationalUnits,
	}
}

// acrossSites is the AIP-159 wildcard parent: allocations and holds of every
// site, narrowed by a Where.
const acrossSites = "organizationalUnits/-"

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
	return listAll(func(token string) ([]*allocationpbv1.Allocation, string, error) {
		return s.allocations.List(ctx, acrossSites, repox.ListInput{
			PageSize:  1000,
			PageToken: token,
			Where: []filterx.Condition{
				filterx.In("resource", resources...),
				{Field: "start_time", Op: filterx.OpLt, Value: window[1].UTC().Format(time.RFC3339Nano)},
				{Field: "end_time", Op: filterx.OpGt, Value: window[0].UTC().Format(time.RFC3339Nano)},
			},
		})
	})
}

// InsertHold writes hold and one allocation per resource it took, in one
// Batch. Lapsed claims in the way are deleted in the same batch, so a hold
// that expired unswept never blocks one that wants its time. A live claim in
// the way makes the no_overlap constraint refuse the batch: types.ErrConflict.
func (s *Store) InsertHold(ctx context.Context, parent string, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	window := [2]time.Time{h.GetWindow().GetStartTime().AsTime(), h.GetWindow().GetEndTime().AsTime()}
	inWay, err := s.claimsOverlapping(ctx, h.GetResources(), window)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	b := allocation.NewBatch(s.conn)
	for _, a := range inWay {
		if a.GetExpireTime() != nil && !a.GetExpireTime().AsTime().After(now) {
			b.DeleteAllocation(a.GetName())
		}
	}
	name, err := b.CreateHold(parent, h)
	if err != nil {
		return nil, err
	}
	for _, r := range h.GetResources() {
		if _, err := b.CreateAllocation(parent, &allocationpbv1.Allocation{
			Resource:   r,
			StartTime:  h.GetWindow().GetStartTime(),
			EndTime:    h.GetWindow().GetEndTime(),
			ExpireTime: h.GetExpireTime(),
			Hold:       name,
		}); err != nil {
			return nil, err
		}
	}
	if err := b.Commit(ctx); err != nil {
		return nil, s.lostRace(ctx, h, window, err)
	}
	return s.holds.Get(ctx, name)
}

// lostRace reports a failed hold insert as the conflict it was when another
// hold now has the time: the backend does not always say so itself -- two
// inserts racing under the exclusion constraint can deadlock, and a GraphQL
// engine may hide the database's reason -- but the claims it left behind do.
func (s *Store) lostRace(ctx context.Context, h *allocationpbv1.Hold, window [2]time.Time, err error) error {
	if errors.Is(err, types.ErrConflict) || errors.Is(err, types.ErrAlreadyExists) {
		return err
	}
	now := time.Now()
	claims, cerr := s.claimsOverlapping(ctx, h.GetResources(), window)
	if cerr != nil {
		return err
	}
	for _, a := range claims {
		live := a.GetExpireTime() == nil || a.GetExpireTime().AsTime().After(now)
		if live && a.GetHold() != h.GetName() {
			return fmt.Errorf("%w: %s was taken while this hold was written", types.ErrConflict, a.GetResource())
		}
	}
	return err
}

// GetHold reads one hold, as stored.
func (s *Store) GetHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	return s.holds.Get(ctx, name)
}

// FindHold returns the oldest hold at parent carrying idempotency key.
func (s *Store) FindHold(ctx context.Context, parent, key string) (*allocationpbv1.Hold, error) {
	found, _, err := s.holds.List(ctx, parent, repox.ListInput{
		PageSize: 1,
		OrderBy:  "create_time",
		Where:    []filterx.Condition{{Field: "idempotency_key", Op: filterx.OpEq, Value: key}},
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, types.ErrNotFound
	}
	return found[0], nil
}

// ListHolds pages a site's holds.
func (s *Store) ListHolds(ctx context.Context, parent string, in repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	return s.holds.List(ctx, parent, in)
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
		return s.holds.List(ctx, acrossSites, repox.ListInput{
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
		case errors.Is(err, types.ErrConflict), errors.Is(err, types.ErrNotFound):
			// Changed or gone under us: whoever did it owns it now.
		default:
			return n, err
		}
	}
	stale, err := listAll(func(token string) ([]*allocationpbv1.Allocation, string, error) {
		return s.allocations.List(ctx, acrossSites, repox.ListInput{
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
		return s.allocations.List(ctx, acrossSites, repox.ListInput{
			PageSize:  1000,
			PageToken: token,
			Where:     []filterx.Condition{{Field: "hold", Op: filterx.OpEq, Value: hold}},
		})
	})
}
