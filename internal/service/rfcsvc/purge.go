// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

// purge.go ends a soft delete (AIP-164). Delete keeps the row, with a
// delete_time, so Undelete can bring it back; the Purger removes it for good
// once the retention has passed, and keeps expire_time saying when that will
// be.
//
// The decision rests on delete_time alone. expire_time is what a caller reads:
// Delete sets it to delete_time + retention in the same write, and Undelete
// clears it (the repositories' WithRetention). The purger only backfills: it
// stamps records deleted without one -- before retention was configured, or
// by an older server -- and clears one a restored record kept. A stale
// expire_time therefore never purges anything.
//
// Children go before parents -- events and availability, calendars,
// resources, sites, organizations -- and a parent with any child row left,
// deleted or not, waits for it, so nothing is purged out from under a record
// that still names it.

import (
	"context"
	"errors"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// purgeInterval is how often the purger sweeps. Retention is counted in days,
// so an hour late is no one's problem.
const purgeInterval = time.Hour

// purgePage is how many records one List call reads.
const purgePage = 100

// Purger purges soft-deleted RFC resources once their retention has passed.
type Purger struct {
	kinds     []purgeKind
	retention time.Duration
	now       func() time.Time
}

// deletable is what the purger reads off every soft-deletable resource.
type deletable interface {
	GetName() string
	GetEtag() string
	GetDeleteTime() *timestamppb.Timestamp
	GetExpireTime() *timestamppb.Timestamp
}

// purgeKind is one resource type as the purger handles it.
type purgeKind struct {
	list func(ctx context.Context, in repox.ListInput) ([]deletable, string, error)
	// expire writes expire_time, nil to clear it, guarded by etag.
	expire func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error
	purge  func(ctx context.Context, name string) error
	// inUse reports whether any row, deleted or not, still names it as its
	// parent; nil for a type nothing names.
	inUse func(ctx context.Context, name string) (bool, error)
}

// NewPurger returns a purger over repos that keeps deleted records for
// retention.
func NewPurger(repos Repositories, retention time.Duration) *Purger {
	// wildcard reads children across every calendar (AIP-159).
	const wildcard = "calendars/-"
	child := func(list func(ctx context.Context, in repox.ListInput) ([]deletable, string, error), field string) func(context.Context, string) (bool, error) {
		return func(ctx context.Context, name string) (bool, error) {
			rows, _, err := list(ctx, repox.ListInput{PageSize: 1, ShowDeleted: true,
				Where: []filterx.Condition{{Field: field, Op: filterx.OpEq, Value: name}}})
			return len(rows) > 0, err
		}
	}
	events := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Events.List(ctx, wildcard, in))
	}
	availabilities := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Availabilities.List(ctx, wildcard, in))
	}
	resources := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Resources.List(ctx, in))
	}
	units := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.OrganizationalUnits.List(ctx, in))
	}
	return &Purger{retention: retention, now: time.Now, kinds: []purgeKind{
		{
			list: events,
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.Events.UpdateOutputOnly(ctx, &eventv1.Event{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.Events.Purge,
		},
		{
			list: availabilities,
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.Availabilities.UpdateOutputOnly(ctx, &availabilityv1.Availability{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.Availabilities.Purge,
		},
		{
			list: func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
				return records(repos.Calendars.List(ctx, in))
			},
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.Calendars.UpdateOutputOnly(ctx, &calendarv1.Calendar{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.Calendars.Purge,
			inUse: func(ctx context.Context, name string) (bool, error) {
				first := repox.ListInput{PageSize: 1, ShowDeleted: true}
				if rows, _, err := repos.Events.List(ctx, name, first); err != nil || len(rows) > 0 {
					return len(rows) > 0, err
				}
				rows, _, err := repos.Availabilities.List(ctx, name, first)
				return len(rows) > 0, err
			},
		},
		{
			list: resources,
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.Resources.UpdateOutputOnly(ctx, &resourcev1.Resource{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.Resources.Purge,
		},
		{
			list: units,
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.OrganizationalUnits.UpdateOutputOnly(ctx, &organizational_unitv1.OrganizationalUnit{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.OrganizationalUnits.Purge,
			inUse: child(resources, "organizational_unit"),
		},
		{
			list: func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
				return records(repos.Organizations.List(ctx, in))
			},
			expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
				_, err := repos.Organizations.UpdateOutputOnly(ctx, &organizationv1.Organization{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
				return err
			},
			purge: repos.Organizations.Purge,
			inUse: child(units, "organization"),
		},
	}}
}

// records widens a typed page to the purger's view of it.
func records[T deletable](rows []T, next string, err error) ([]deletable, string, error) {
	out := make([]deletable, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out, next, err
}

// Sweep purges every deleted record past its retention and brings every
// expire_time up to date, returning how many records it purged. A record that
// changes under it is left for the next sweep.
func (p *Purger) Sweep(ctx context.Context) (int, error) {
	now := p.now()
	purged := 0
	for _, k := range p.kinds {
		n, err := p.sweepDeleted(ctx, k, now)
		purged += n
		if err != nil {
			return purged, err
		}
		if err := p.clearLive(ctx, k); err != nil {
			return purged, err
		}
	}
	return purged, nil
}

// sweepDeleted purges k's deleted records that are due and stamps the rest.
func (p *Purger) sweepDeleted(ctx context.Context, k purgeKind, now time.Time) (int, error) {
	purged := 0
	err := eachPage(ctx, k, repox.ListInput{ShowDeleted: true,
		Where: []filterx.Condition{{Field: "delete_time", Op: filterx.OpNull, Value: "false"}}},
		func(r deletable) error {
			if r.GetDeleteTime() == nil {
				return nil
			}
			due := r.GetDeleteTime().AsTime().Add(p.retention)
			if !due.After(now) {
				if k.inUse != nil {
					if used, err := k.inUse(ctx, r.GetName()); err != nil || used {
						return err
					}
				}
				if err := k.purge(ctx, r.GetName()); err != nil && !errors.Is(err, types.ErrNotFound) {
					return err
				}
				purged++
				return nil
			}
			if r.GetExpireTime() != nil && r.GetExpireTime().AsTime().Equal(due) {
				return nil
			}
			return ignoreConflict(k.expire(ctx, r.GetName(), r.GetEtag(), timestamppb.New(due)))
		})
	return purged, err
}

// clearLive clears the expire_time an Undelete left on k's live records.
func (p *Purger) clearLive(ctx context.Context, k purgeKind) error {
	return eachPage(ctx, k, repox.ListInput{
		Where: []filterx.Condition{{Field: "expire_time", Op: filterx.OpNull, Value: "false"}}},
		func(r deletable) error {
			return ignoreConflict(k.expire(ctx, r.GetName(), r.GetEtag(), nil))
		})
}

// eachPage calls fn on every record k lists for in, page by page.
func eachPage(ctx context.Context, k purgeKind, in repox.ListInput, fn func(deletable) error) error {
	in.PageSize = purgePage
	for {
		rows, next, err := k.list(ctx, in)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := fn(r); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		in.PageToken = next
	}
}

// ignoreConflict drops the error of a write whose etag no longer matches: the
// record changed since it was listed, and the next sweep sees it as it is.
func ignoreConflict(err error) error {
	if errors.Is(err, types.ErrConflict) || errors.Is(err, types.ErrNotFound) {
		return nil
	}
	return err
}

// Start sweeps now and then every interval (purgeInterval when zero) until
// ctx ends, reporting failures to logErr.
func (p *Purger) Start(ctx context.Context, interval time.Duration, logErr func(error)) {
	if interval <= 0 {
		interval = purgeInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if _, err := p.Sweep(ctx); err != nil && logErr != nil && ctx.Err() == nil {
				logErr(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
