// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// The engine's reads: resources, their calendars' availability and events,
// and the claims held against them, each batched across every key a request
// needs, so cost grows with rows returned rather than round trips. This file
// speaks the service's types; store.go speaks the allocation repository's.

import (
	"context"
	"slices"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
)

var _ allocation.Store = (*Store)(nil)

// acrossCalendars is the AIP-159 wildcard parent for events and availability
// of every calendar, narrowed by a Where on the calendar.
const acrossCalendars = "calendars/-"

// Resources reads the resources q selects, soft-deleted ones included -- the
// engine counts those but never offers them -- with their calendars' zones.
func (s *Store) Resources(ctx context.Context, q allocation.ResourceQuery) ([]allocation.ResourceRecord, error) {
	var where []filterx.Condition
	if len(q.OrganizationalUnits) > 0 {
		where = append(where, filterx.In("organizational_unit", q.OrganizationalUnits...))
	}
	if q.Organization != "" {
		units, err := listAll(func(token string) ([]*organizational_unitv1.OrganizationalUnit, string, error) {
			return s.units.List(ctx, repox.ListInput{
				PageSize:  1000,
				PageToken: token,
				// A site stores its organization's full name, organizations/{id}.
				Where: []filterx.Condition{{Field: "organization", Op: filterx.OpEq, Value: q.Organization}},
			})
		})
		if err != nil {
			return nil, err
		}
		if len(units) == 0 {
			return nil, nil
		}
		names := make([]string, len(units))
		for i, u := range units {
			names[i] = u.GetName()
		}
		where = append(where, filterx.In("organizational_unit", names...))
	}
	if len(q.Calendars) > 0 {
		where = append(where, filterx.In("calendar", q.Calendars...))
	}
	if len(q.Names) > 0 {
		where = append(where, filterx.In("name", q.Names...))
	}
	found, err := listAll(func(token string) ([]*resourcev1.Resource, string, error) {
		return s.resources.List(ctx, repox.ListInput{PageSize: 1000, PageToken: token, OrderBy: "name", ShowDeleted: true, Where: where})
	})
	if err != nil {
		return nil, err
	}
	// RESOURCE-TYPE is a list column; "any of these types" is read here.
	if len(q.Types) > 0 {
		found = slices.DeleteFunc(found, func(r *resourcev1.Resource) bool {
			return !slices.ContainsFunc(r.GetResourceTypes(), func(t string) bool { return slices.Contains(q.Types, t) })
		})
	}

	var cals []string
	for _, r := range found {
		if c := r.GetCalendar(); c != "" && !slices.Contains(cals, c) {
			cals = append(cals, c)
		}
	}
	zones := map[string]string{}
	if len(cals) > 0 {
		got, err := listAll(func(token string) ([]*calendarv1.Calendar, string, error) {
			return s.calendars.List(ctx, repox.ListInput{
				PageSize: 1000, PageToken: token, ShowDeleted: true,
				Where: []filterx.Condition{filterx.In("name", cals...)},
			})
		})
		if err != nil {
			return nil, err
		}
		for _, c := range got {
			zones[c.GetName()] = c.GetTimeZone().GetId()
		}
	}
	out := make([]allocation.ResourceRecord, 0, len(found))
	for _, r := range found {
		out = append(out, allocation.ResourceRecord{Resource: r, TimeZone: zones[r.GetCalendar()]})
	}
	return out, nil
}

// Availability reads every live VAVAILABILITY on the calendars.
func (s *Store) Availability(ctx context.Context, calendars []string) (map[string][]*availabilityv1.Availability, error) {
	out := map[string][]*availabilityv1.Availability{}
	if len(calendars) == 0 {
		return out, nil
	}
	found, err := listAll(func(token string) ([]*availabilityv1.Availability, string, error) {
		return s.availabilities.List(ctx, acrossCalendars, repox.ListInput{
			PageSize: 1000, PageToken: token,
			Where: []filterx.Condition{filterx.In("calendar", calendars...)},
		})
	})
	if err != nil {
		return nil, err
	}
	for _, a := range found {
		cal := parentOf(a.GetName())
		out[cal] = append(out[cal], a)
	}
	return out, nil
}

// Events reads the events on the calendars that may overlap window, and every
// override (an event with an original_start) on them, wherever it moved to. A
// recurring event begun before the window ends is returned once with its rule,
// however long ago it began; the engine expands and clips it, and decides
// which events are busy.
func (s *Store) Events(ctx context.Context, calendars []string, window engine.Span) (map[string][]*eventv1.Event, error) {
	out := map[string][]*eventv1.Event{}
	if len(calendars) == 0 {
		return out, nil
	}
	// The day of slack either side keeps an all-day event, stored at UTC
	// midnight and read later as local midnight, from being clipped a zone
	// early.
	lo, hi := window.Start.Add(-24*time.Hour), window.End.Add(24*time.Hour)
	found, err := listAll(func(token string) ([]*eventv1.Event, string, error) {
		return s.events.List(ctx, acrossCalendars, repox.ListInput{
			PageSize: 1000, PageToken: token,
			Where: append([]filterx.Condition{filterx.In("calendar", calendars...)}, windowHints(lo, hi)...),
		})
	})
	if err != nil {
		return nil, err
	}
	for _, e := range found {
		// A series that begins after the window has no instance in it. Read
		// in UTC, a floating time is off by at most a zone's offset, which the
		// day of slack covers. An event whose times cannot be read is kept for
		// the engine to judge.
		if sp, err := allocation.EventSpan(e, time.UTC); err == nil && e.GetOriginalStart() == nil &&
			(!sp.Start.Before(hi) || (e.GetRecurrence() == nil && !sp.End.After(lo))) {
			continue
		}
		cal := parentOf(e.GetName())
		out[cal] = append(out[cal], e)
	}
	return out, nil
}

// windowHints narrow the events read to those that may touch [lo, hi): begun
// before hi, and recurring, open-ended or ended after lo -- plus every
// override, whose original_start may sit in the window while it moved out. A
// DATE is compared by day, so the hints keep a superset of what Events keeps.
// They are hints: a backend that cannot reach an event's start or end ignores
// them, and the checks in Events decide.
func windowHints(lo, hi time.Time) []filterx.Condition {
	const day = "2006-01-02"
	return []filterx.Condition{
		filterx.AsHint(filterx.Or(
			filterx.IsNull("original_start", false),
			filterx.IsNull("start", true),
			filterx.Condition{Field: "start.date_time", Op: filterx.OpLt, Value: hi.UTC().Format(time.RFC3339Nano)},
			filterx.Condition{Field: "start.date", Op: filterx.OpLte, Value: hi.UTC().Format(day)},
		)),
		filterx.AsHint(filterx.Or(
			filterx.IsNull("original_start", false),
			filterx.IsNull("recurrence", false),
			filterx.IsNull("end", true),
			filterx.Condition{Field: "end.date_time", Op: filterx.OpGt, Value: lo.UTC().Format(time.RFC3339Nano)},
			filterx.Condition{Field: "end.date", Op: filterx.OpGte, Value: lo.UTC().Format(day)},
		)),
	}
}

// Claims reads the claims overlapping window on each resource as of now:
// held by an active hold not yet lapsed, or kept by a converted one.
func (s *Store) Claims(ctx context.Context, resources []string, window engine.Span, now time.Time) (allocation.Claims, error) {
	out := allocation.Claims{Held: map[string]engine.Spans{}, Converted: map[string]engine.Spans{}}
	rows, err := s.claimsOverlapping(ctx, resources, [2]time.Time{window.Start, window.End})
	if err != nil {
		return out, err
	}
	for _, a := range rows {
		sp := engine.Spans{{Start: a.GetStartTime().AsTime(), End: a.GetEndTime().AsTime()}}
		switch exp := a.GetExpireTime(); {
		case exp == nil:
			out.Converted[a.GetResource()] = out.Converted[a.GetResource()].Union(sp)
		case exp.AsTime().After(now):
			out.Held[a.GetResource()] = out.Held[a.GetResource()].Union(sp)
		}
	}
	return out, nil
}

// parentOf is the calendar an event or availability name sits under.
// "calendars/c/events/e" -> "calendars/c".
func parentOf(name string) string {
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
