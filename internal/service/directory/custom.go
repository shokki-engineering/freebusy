// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

// custom.go holds the methods the generated servers leave: the AIP-136 custom
// methods (QueryFreeBusy, BatchGetEvents, ExpandEvent) and the
// THISANDFUTURE overrides of CreateEvent and UpdateEvent, which split a
// series rather than write one record.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"google.golang.org/protobuf/proto"
)

const (
	calendarService = "Calendars"
	eventService    = "Events"
)

// QueryFreeBusy reports the calendar's busy time over the window, from the
// engine: opaque events and converted holds are BUSY, live holds
// BUSY_TENTATIVE, and time its availability does not offer
// BUSY_UNAVAILABLE. Free time is not listed; it is whatever the periods
// leave. Periods come back in start order and never overlap.
func (s *CalendarServer) QueryFreeBusy(ctx context.Context, req *calendarv1.QueryFreeBusyRequest) (*calendarv1.QueryFreeBusyResponse, error) {
	out := &calendarv1.QueryFreeBusyResponse{}
	err := s.Options().Run(ctx, calendarService, "QueryFreeBusy", func(ctx context.Context) error {
		window, err := windowOf(req.GetWindow())
		if err != nil {
			return err
		}
		cal, err := s.repos.Calendars.Get(ctx, req.GetName())
		if err != nil {
			return err
		}
		fb, err := s.freeBusy.FreeBusy(ctx, cal.GetName(), cal.GetTimeZone().GetId(), window)
		if err != nil {
			return err
		}
		out.Periods = freeBusyPeriods(fb)
		return nil
	})
	return out, err
}

// freeBusyPeriods flattens the engine's three disjoint sets into one
// start-ordered list.
func freeBusyPeriods(fb allocation.FreeBusy) []*calendarv1.FreeBusyPeriod {
	type typed struct {
		span engine.Span
		kind calendarv1.BusyType
	}
	var all []typed
	add := func(spans engine.Spans, kind calendarv1.BusyType) {
		for _, sp := range spans {
			all = append(all, typed{sp, kind})
		}
	}
	add(fb.Busy, calendarv1.BusyType_BUSY_TYPE_BUSY)
	add(fb.Tentative, calendarv1.BusyType_BUSY_TYPE_BUSY_TENTATIVE)
	add(fb.Unavailable, calendarv1.BusyType_BUSY_TYPE_BUSY_UNAVAILABLE)
	sortBy(all, func(a, b typed) bool { return a.span.Start.Before(b.span.Start) })
	out := make([]*calendarv1.FreeBusyPeriod, 0, len(all))
	for _, t := range all {
		out = append(out, &calendarv1.FreeBusyPeriod{Period: allocation.IntervalOf(t.span), BusyType: t.kind})
	}
	return out
}

// CreateEvent stores a THISANDFUTURE override (original_start_range) by
// splitting its series; any other event is the generated AIP-133 create.
func (s *EventServer) CreateEvent(ctx context.Context, req *eventv1.CreateEventRequest) (*eventv1.Event, error) {
	if req.GetEvent().GetOriginalStartRange() == eventv1.RecurrenceRange_RECURRENCE_RANGE_UNSPECIFIED {
		return s.EventsServer.CreateEvent(ctx, req)
	}
	var out *eventv1.Event
	err := s.Options().Run(ctx, eventService, "CreateEvent", func(ctx context.Context) error {
		if err := checkID("event_id", req.GetEventId()); err != nil {
			return err
		}
		if err := liveParent(ctx, s.repos, req.GetParent()); err != nil {
			return err
		}
		in := proto.Clone(req.GetEvent()).(*eventv1.Event)
		if id := req.GetEventId(); id != "" {
			in.Name = req.GetParent() + "/events/" + id
		}
		var err error
		out, err = s.split(ctx, req.GetParent(), in)
		return err
	})
	return out, err
}

// UpdateEvent applies a THISANDFUTURE edit by splitting the series; any
// other update is the generated AIP-134 update.
func (s *EventServer) UpdateEvent(ctx context.Context, req *eventv1.UpdateEventRequest) (*eventv1.Event, error) {
	e := req.GetEvent()
	if e.GetOriginalStartRange() == eventv1.RecurrenceRange_RECURRENCE_RANGE_UNSPECIFIED {
		return s.EventsServer.UpdateEvent(ctx, req)
	}
	var out *eventv1.Event
	err := s.Options().Run(ctx, eventService, "UpdateEvent", func(ctx context.Context) error {
		stored, err := s.repos.Events.Get(ctx, e.GetName())
		if err != nil {
			return err
		}
		edit := proto.Clone(e).(*eventv1.Event)
		if edit.GetIcalUid() == "" {
			edit.IcalUid = stored.GetIcalUid()
		}
		if edit.GetOriginalStart() == nil {
			edit.OriginalStart = stored.GetOriginalStart()
		}
		out, err = s.split(ctx, calendarOf(e.GetName()), edit)
		return err
	})
	return out, err
}

// BatchGetEvents returns the named events in request order, all or nothing
// (AIP-231): one missing name fails the call.
func (s *EventServer) BatchGetEvents(ctx context.Context, req *eventv1.BatchGetEventsRequest) (*eventv1.BatchGetEventsResponse, error) {
	out := &eventv1.BatchGetEventsResponse{}
	err := s.Options().Run(ctx, eventService, "BatchGetEvents", func(ctx context.Context) error {
		prefix := req.GetParent() + "/events/"
		for _, name := range req.GetNames() {
			if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
				return rpc.ToStatusErr(servicex.BadField("NAME_NOT_UNDER_PARENT", "names",
					fmt.Sprintf("%s is not an event of %s", name, req.GetParent()),
					"Every name must be "+req.GetParent()+"/events/{event}; ask for another calendar's events with its own parent"))
			}
			e, err := s.repos.Events.Get(ctx, name)
			if err != nil {
				return err
			}
			out.Events = append(out.Events, e)
		}
		return nil
	})
	return out, err
}

// split stores a THISANDFUTURE override, edit, by splitting the series it
// names in parent (see SplitSeries) in one write, and returns the event the
// change now lives in: the new series from the split instance on, or the
// original when the split edited it in place or cancelled the rest of it.
func (s *EventServer) split(ctx context.Context, parent string, edit *eventv1.Event) (*eventv1.Event, error) {
	if edit.GetIcalUid() == "" {
		return nil, servicex.BadField("REQUIRED_FIELD", "event.ical_uid",
			"an override with original_start_range needs the ical_uid of the series it changes",
			"Set ical_uid to the recurring event's ical_uid")
	}
	cal, err := s.repos.Calendars.Get(ctx, parent)
	if err != nil {
		return nil, err
	}
	series, err := FindSeries(ctx, s.repos, parent, edit.GetIcalUid())
	if err != nil {
		return nil, err
	}
	overrides, err := s.repos.Overrides(ctx, series)
	if err != nil {
		return nil, err
	}
	wr := s.repos.Writes()
	done, err := SplitSeries(ctx, s.repos, wr, parent, series, overrides, edit, ZoneOf(cal.GetTimeZone().GetId()))
	if err != nil {
		return nil, err
	}
	if err := wr.Commit(ctx); err != nil {
		return nil, err
	}
	if done.Tail != nil {
		return s.repos.Events.Get(ctx, done.Tail.GetName())
	}
	return s.repos.Events.Get(ctx, done.Series.GetName())
}

// expandPageSize is ExpandEvent's default page, which upstream sets apart
// from List's.
const expandPageSize = 250

// ExpandEvent materialises a recurring event's instances in the window, a
// page at a time.
func (s *EventServer) ExpandEvent(ctx context.Context, req *eventv1.ExpandEventRequest) (*eventv1.ExpandEventResponse, error) {
	out := &eventv1.ExpandEventResponse{}
	err := s.Options().Run(ctx, eventService, "ExpandEvent", func(ctx context.Context) error {
		window, err := windowOf(req.GetWindow())
		if err != nil {
			return err
		}
		e, err := s.repos.Events.Get(ctx, req.GetName())
		if err != nil {
			return err
		}
		cal, err := s.repos.Calendars.Get(ctx, calendarOf(req.GetName()))
		if err != nil {
			return err
		}
		overrides, err := s.repos.Overrides(ctx, e)
		if err != nil {
			return err
		}
		instances, err := expand(e, overrides, ZoneOf(cal.GetTimeZone().GetId()), window)
		if err != nil {
			return err
		}
		size := req.GetPageSize()
		if size <= 0 {
			size = expandPageSize
		}
		limit, offset := rpc.PageBounds(size, req.GetPageToken())
		if offset > len(instances) {
			offset = len(instances)
		}
		end := min(offset+limit, len(instances))
		out.Events = instances[offset:end]
		if end < len(instances) {
			out.NextPageToken = rpc.EncodeOffset(end)
		}
		return nil
	})
	return out, err
}

// calendarOf returns the calendar an event name sits under.
// "calendars/c/events/e" -> "calendars/c".
func calendarOf(event string) string {
	n := 0
	for i := range len(event) {
		if event[i] == '/' {
			n++
			if n == 2 {
				return event[:i]
			}
		}
	}
	return event
}

// liveParent checks that calendar exists and is not soft-deleted: a child
// written under a deleted calendar would be invisible to List and to the
// engine both, and only reappear by surprise on undelete.
func liveParent(ctx context.Context, repos Repositories, calendar string) error {
	cal, err := repos.Calendars.Get(ctx, calendar)
	if errors.Is(err, repox.ErrNotFound) {
		return servicex.Missing("CALENDAR_NOT_FOUND", hints.TypeOf(calendar), calendar,
			fmt.Sprintf("calendar %q does not exist", calendar),
			"Check the parent, or list calendars with "+hints.Call("ListCalendars", nil)+
				"; create one first with "+hints.Call("CreateCalendar", nil))
	}
	if err != nil {
		return err
	}
	if cal.GetDeleteTime() != nil {
		return servicex.Blocked("CALENDAR_DELETED", hints.TypeOf(calendar), calendar,
			fmt.Sprintf("calendar %s is deleted, and nothing can be added to it", calendar),
			"Restore it with "+hints.Call("UndeleteCalendar", map[string]string{"name": calendar})+", or use a live calendar")
	}
	return nil
}

func isNotFound(err error) bool { return errors.Is(err, repox.ErrNotFound) }

// sortBy sorts s stably by less.
func sortBy[T any](s []T, less func(a, b T) bool) {
	slices.SortStableFunc(s, func(a, b T) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return 0
	})
}
