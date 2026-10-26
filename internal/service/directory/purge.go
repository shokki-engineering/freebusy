// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

// purge.go ends a soft delete (AIP-164). Delete keeps the row, with a
// delete_time, so Undelete can bring it back; the Purger removes it for good
// once the retention has passed, and keeps expire_time saying when that will
// be.
//
// The decision rests on delete_time. expire_time is what a caller reads:
// Delete sets it to delete_time + retention in the same write, and Undelete
// clears it (the repositories' WithRetention). Each sweep first brings every
// deleted record's expire_time to delete_time + retention -- stamping records
// deleted without one, before retention was configured or by an older
// server, and correcting one written under another retention -- and clears
// one a restored record kept; then the generated PurgeExpired removes what
// has expired. A stale expire_time therefore never purges anything.
//
// Children go before parents -- events and availability, calendars,
// resources -- and PurgeExpired keeps any record something still refers to
// (a child row, deleted or not, or a foreign key from freebusy's own tables,
// such as an allocation's resource), so nothing is purged
// out from under a record that names it. Kept records are counted in
// freebusy.purge.kept and wait for a later sweep.

import (
	"context"
	"errors"
	"time"

	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/the-protobuf-project/runtime-go/telemetry"
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
	purged    telemetry.Counter
	kept      telemetry.Counter
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
	// kind labels its metrics: event, availability, calendar, ...
	kind string
	list func(ctx context.Context, in repox.ListInput) ([]deletable, string, error)
	// expire writes expire_time, nil to clear it, guarded by etag.
	expire func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error
	// purgeExpired removes the deleted records whose expire_time has passed,
	// keeping those something still refers to.
	purgeExpired func(ctx context.Context, now time.Time) (repox.PurgeResult, error)
}

// NewPurger returns a purger over repos that keeps deleted records for
// retention, counting what it purges and keeps on meter (nil for none).
func NewPurger(repos Repositories, retention time.Duration, meter telemetry.Meter) *Purger {
	// wildcard reads children across every calendar (AIP-159).
	const wildcard = "calendars/-"
	events := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Events.List(ctx, wildcard, in))
	}
	availabilities := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Availabilities.List(ctx, wildcard, in))
	}
	resources := func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
		return records(repos.Resources.List(ctx, in))
	}
	if meter == nil {
		meter = telemetry.NoopMeter
	}
	return &Purger{retention: retention, now: time.Now,
		purged: meter.Counter("freebusy.purge.purged", telemetry.WithUnit("1"),
			telemetry.WithDescription("Soft-deleted records removed for good once their retention passed, by kind.")),
		kept: meter.Counter("freebusy.purge.kept", telemetry.WithUnit("1"),
			telemetry.WithDescription("Soft-deleted records due for purging but kept because something still refers to them, by kind; counted again every sweep they stay.")),
		kinds: []purgeKind{
			{
				kind: "event",
				list: events,
				expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
					_, err := repos.Events.UpdateOutputOnly(ctx, &eventv1.Event{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
					return err
				},
				purgeExpired: repos.Events.PurgeExpired,
			},
			{
				kind: "availability",
				list: availabilities,
				expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
					_, err := repos.Availabilities.UpdateOutputOnly(ctx, &availabilityv1.Availability{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
					return err
				},
				purgeExpired: repos.Availabilities.PurgeExpired,
			},
			{
				kind: "calendar",
				list: func(ctx context.Context, in repox.ListInput) ([]deletable, string, error) {
					return records(repos.Calendars.List(ctx, in))
				},
				expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
					_, err := repos.Calendars.UpdateOutputOnly(ctx, &calendarv1.Calendar{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
					return err
				},
				purgeExpired: repos.Calendars.PurgeExpired,
			},
			{
				kind: "resource",
				list: resources,
				expire: func(ctx context.Context, name, etag string, at *timestamppb.Timestamp) error {
					_, err := repos.Resources.UpdateOutputOnly(ctx, &resourcev1.Resource{Name: name, Etag: etag, ExpireTime: at}, []string{"expire_time"})
					return err
				},
				purgeExpired: repos.Resources.PurgeExpired,
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
// changes under it, or that something still refers to, is left for the next
// sweep.
func (p *Purger) Sweep(ctx context.Context) (int, error) {
	now := p.now()
	purged := 0
	for _, k := range p.kinds {
		if err := p.stampDeleted(ctx, k); err != nil {
			return purged, err
		}
		res, err := k.purgeExpired(ctx, now)
		purged += len(res.Purged)
		p.purged.Add(ctx, float64(len(res.Purged)), telemetry.Labels{"kind": k.kind})
		p.kept.Add(ctx, float64(len(res.Kept)), telemetry.Labels{"kind": k.kind})
		if err != nil {
			return purged, err
		}
		if err := p.clearLive(ctx, k); err != nil {
			return purged, err
		}
	}
	return purged, nil
}

// stampDeleted sets every deleted record of k's expire_time to delete_time +
// retention where it is anything else, so PurgeExpired removes exactly what
// is due.
func (p *Purger) stampDeleted(ctx context.Context, k purgeKind) error {
	return eachPage(ctx, k, repox.ListInput{ShowDeleted: true,
		Where: []filterx.Condition{{Field: "delete_time", Op: filterx.OpNull, Value: "false"}}},
		func(r deletable) error {
			if r.GetDeleteTime() == nil {
				return nil
			}
			due := r.GetDeleteTime().AsTime().Add(p.retention)
			if r.GetExpireTime() != nil && r.GetExpireTime().AsTime().Equal(due) {
				return nil
			}
			return ignoreConflict(k.expire(ctx, r.GetName(), r.GetEtag(), timestamppb.New(due)))
		})
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
	if errors.Is(err, repox.ErrConflict) || errors.Is(err, repox.ErrNotFound) {
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
