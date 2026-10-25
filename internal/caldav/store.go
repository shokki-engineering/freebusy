// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"github.com/the-protobuf-project/runtime-go/rfc/caldav"
)

// store is caldav.Store over the generated repositories. Deletes are soft,
// AIP-164, so the gRPC API can still undelete what a calendar client removed.
type store struct {
	repos directory.Repositories
	fb    directory.FreeBusyQuerier
}

func (s *store) Calendars(ctx context.Context) ([]*calendarv1.Calendar, error) {
	var out []*calendarv1.Calendar
	for token := ""; ; {
		page, next, err := s.repos.Calendars.List(ctx, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if token = next; token == "" {
			return out, nil
		}
	}
}

// Calendar returns the live calendar id. A soft-deleted calendar is gone as
// far as CalDAV is concerned.
func (s *store) Calendar(ctx context.Context, id string) (*calendarv1.Calendar, error) {
	c, err := s.repos.Calendars.Get(ctx, caldav.CalendarName(id))
	if err != nil {
		return nil, err
	}
	if c.GetDeleteTime() != nil {
		return nil, repox.ErrNotFound
	}
	return c, nil
}

func (s *store) CreateCalendar(ctx context.Context, c *calendarv1.Calendar) error {
	_, err := s.repos.Calendars.Create(ctx, c)
	return err
}

func (s *store) UpdateCalendar(ctx context.Context, c *calendarv1.Calendar, paths []string) error {
	_, err := s.repos.Calendars.Update(ctx, c, paths)
	return err
}

// DeleteCalendar removes the calendar with every member in one write, so a
// failure leaves the collection whole rather than partly emptied.
func (s *store) DeleteCalendar(ctx context.Context, c *calendarv1.Calendar, members []caldav.Object) error {
	wr := s.repos.Writes()
	for _, o := range members {
		deleteObject(wr, o)
	}
	wr.Calendars.DeleteCalendar(c.GetName())
	return wr.Commit(ctx)
}

// Objects returns every live object in a calendar, events first. Overrides
// are gathered into their series' object; one whose series is not in the
// calendar stands as an object of its own.
func (s *store) Objects(ctx context.Context, calendar string) ([]caldav.Object, error) {
	var events []*eventv1.Event
	parent := caldav.CalendarName(calendar)
	for token := ""; ; {
		page, next, err := s.repos.Events.List(ctx, parent, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		if token = next; token == "" {
			break
		}
	}
	var out []caldav.Object
	series := map[string]int{}
	for _, e := range events {
		if e.GetOriginalStart() != nil {
			continue
		}
		if e.GetRecurrence() != nil && e.GetIcalUid() != "" {
			series[e.GetIcalUid()] = len(out)
		}
		out = append(out, caldav.Object{Name: lastSegment(e.GetName()), Event: e})
	}
	for _, e := range events {
		if e.GetOriginalStart() == nil {
			continue
		}
		if i, ok := series[e.GetIcalUid()]; ok {
			out[i].Overrides = append(out[i].Overrides, e)
		} else {
			out = append(out, caldav.Object{Name: lastSegment(e.GetName()), Event: e})
		}
	}
	for token := ""; ; {
		page, next, err := s.repos.Availabilities.List(ctx, parent, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, a := range page {
			out = append(out, caldav.Object{Name: lastSegment(a.GetName()), Availability: a})
		}
		if token = next; token == "" {
			return out, nil
		}
	}
}

// Object returns the live object id: an event, with the overrides of its
// instances, else an availability of that id. An override is part of its
// series' object, not one of its own, so its id is not found.
func (s *store) Object(ctx context.Context, calendar, id string) (caldav.Object, error) {
	parent := caldav.CalendarName(calendar)
	e, err := s.repos.Events.Get(ctx, parent+"/events/"+id)
	switch {
	case err == nil && e.GetDeleteTime() == nil:
		if e.GetOriginalStart() != nil {
			if attached, err := s.hasSeries(ctx, e); err != nil || attached {
				return caldav.Object{}, cmp.Or(err, repox.ErrNotFound)
			}
			return caldav.Object{Name: id, Event: e}, nil
		}
		overrides, err := s.repos.Overrides(ctx, e)
		if err != nil {
			return caldav.Object{}, err
		}
		return caldav.Object{Name: id, Event: e, Overrides: overrides}, nil
	case err != nil && !errors.Is(err, repox.ErrNotFound):
		return caldav.Object{}, err
	}
	a, err := s.repos.Availabilities.Get(ctx, parent+"/availabilities/"+id)
	switch {
	case err == nil && a.GetDeleteTime() == nil:
		return caldav.Object{Name: id, Availability: a}, nil
	case err != nil && !errors.Is(err, repox.ErrNotFound):
		return caldav.Object{}, err
	}
	return caldav.Object{}, repox.ErrNotFound
}

// ObjectByUID finds the series or single event, else the availability, that
// carries uid.
func (s *store) ObjectByUID(ctx context.Context, calendar, uid string) (caldav.Object, error) {
	parent := caldav.CalendarName(calendar)
	events, _, err := s.repos.Events.List(ctx, parent, repox.ListInput{PageSize: 1, Where: []filterx.Condition{
		{Field: "ical_uid", Op: filterx.OpEq, Value: uid},
		filterx.IsNull("original_start", true),
	}})
	if err != nil {
		return caldav.Object{}, err
	}
	if len(events) > 0 {
		return s.Object(ctx, calendar, lastSegment(events[0].GetName()))
	}
	avails, _, err := s.repos.Availabilities.List(ctx, parent, repox.ListInput{PageSize: 1, Where: []filterx.Condition{
		{Field: "ical_uid", Op: filterx.OpEq, Value: uid},
	}})
	if err != nil {
		return caldav.Object{}, err
	}
	if len(avails) > 0 {
		return caldav.Object{Name: lastSegment(avails[0].GetName()), Availability: avails[0]}, nil
	}
	return caldav.Object{}, repox.ErrNotFound
}

// hasSeries reports whether override belongs to a live recurring series in
// its calendar, which makes it part of that series' object.
func (s *store) hasSeries(ctx context.Context, override *eventv1.Event) (bool, error) {
	series, _, err := s.repos.Events.List(ctx, parentOf(override.GetName()), repox.ListInput{
		PageSize: 1,
		Where: []filterx.Condition{
			{Field: "ical_uid", Op: filterx.OpEq, Value: override.GetIcalUid()},
			filterx.IsNull("original_start", true),
			filterx.IsNull("recurrence", false),
		},
	})
	return len(series) > 0, err
}

// PutObject writes one object. The series and its overrides are one write:
// a failure part way leaves the object as it was.
func (s *store) PutObject(ctx context.Context, req caldav.PutRequest) (bool, error) {
	parent := req.Calendar.GetName()
	if req.Availability != nil {
		if req.Existing != nil {
			req.Availability.Name, req.Availability.Etag = req.Existing.Availability.GetName(), req.Existing.ETag()
			_, err := s.repos.Availabilities.Update(ctx, req.Availability, nil)
			return false, err
		}
		if err := directory.PrepareCreate(req.Availability, parent+"/availabilities/"+req.Name); err != nil {
			return false, err
		}
		_, err := s.repos.Availabilities.Create(ctx, parent, req.Availability)
		return false, err
	}

	wr := s.repos.Writes()
	loc := directory.ZoneOf(req.Calendar.GetTimeZone().GetId())
	var stored []*eventv1.Event
	if req.Existing != nil {
		req.Event.Name, req.Event.Etag = req.Existing.Event.GetName(), req.Existing.Event.GetEtag()
		stored = req.Existing.Overrides
	} else if err := directory.PrepareCreate(req.Event, parent+"/events/"+req.Name); err != nil {
		return false, err
	}
	incoming, split := req.Overrides, false
	if req.Split != nil {
		// RECURRENCE-ID;RANGE=THISANDFUTURE: the series is cut before that
		// instance and the rest becomes a series of its own, a new object.
		// The overrides the split moves to it, or ends, are left out of the
		// sync below, which would otherwise delete them.
		claimStored(stored, incoming, loc)
		done, err := directory.SplitSeries(ctx, s.repos, wr, parent, req.Event, incoming, req.Split, loc)
		if err != nil {
			return false, refusedSplit(err)
		}
		kept := map[string]bool{}
		for _, o := range done.Kept {
			kept[o.GetName()] = true
		}
		var untouched []*eventv1.Event
		for _, o := range stored {
			if kept[o.GetName()] || !slices.ContainsFunc(incoming, func(e *eventv1.Event) bool { return e.GetName() == o.GetName() }) {
				untouched = append(untouched, o)
			}
		}
		stored, incoming, split = untouched, done.Kept, true
	} else if req.Existing != nil {
		wr.Events.UpdateEvent(req.Event, nil)
	} else if _, err := wr.Events.CreateEvent(parent, req.Event); err != nil {
		return false, err
	}
	if err := syncOverrides(wr, parent, stored, incoming, loc); err != nil {
		return false, err
	}
	return split, wr.Commit(ctx)
}

func (s *store) DeleteObject(ctx context.Context, _ string, o caldav.Object) error {
	wr := s.repos.Writes()
	deleteObject(wr, o)
	return wr.Commit(ctx)
}

// Instances answers from the engine's expander, so an instance a client is
// shown is exactly the time the engine blocks.
func (s *store) Instances(_ context.Context, c *calendarv1.Calendar, o caldav.Object, window caldav.Period) ([]*eventv1.Event, error) {
	return directory.Instances(o.Event, o.Overrides, directory.ZoneOf(c.GetTimeZone().GetId()), engine.Span{Start: window.Start, End: window.End})
}

// FreeBusy answers from the engine: the same busy time QueryFreeBusy reports
// and a hold would meet.
func (s *store) FreeBusy(ctx context.Context, c *calendarv1.Calendar, window caldav.Period) (caldav.FreeBusy, error) {
	fb, err := s.fb.FreeBusy(ctx, c.GetName(), c.GetTimeZone().GetId(), engine.Span{Start: window.Start, End: window.End})
	if err != nil {
		return caldav.FreeBusy{}, err
	}
	return caldav.FreeBusy{Busy: periods(fb.Busy), Tentative: periods(fb.Tentative), Unavailable: periods(fb.Unavailable)}, nil
}

func periods(spans engine.Spans) []caldav.Period {
	out := make([]caldav.Period, len(spans))
	for i, s := range spans {
		out[i] = caldav.Period{Start: s.Start, End: s.End}
	}
	return out
}

// syncOverrides queues on wr what makes a series' stored overrides the ones
// a PUT carried: an override for an instance already overridden replaces it
// (guarded by the stored one's etag), a new one is created, and one the body
// no longer carries is deleted, so the instance it replaced comes back.
func syncOverrides(wr directory.Writes, parent string, stored, incoming []*eventv1.Event, loc *time.Location) error {
	byKey := map[int64]*eventv1.Event{}
	for _, e := range stored {
		byKey[instantKey(e, loc)] = e
	}
	for _, e := range incoming {
		if old, ok := byKey[instantKey(e, loc)]; ok {
			delete(byKey, instantKey(e, loc))
			e.Name, e.Etag = old.GetName(), old.GetEtag()
			wr.Events.UpdateEvent(e, nil)
			continue
		}
		if err := directory.PrepareCreate(e, ""); err != nil {
			return err
		}
		if _, err := wr.Events.CreateEvent(parent, e); err != nil {
			return err
		}
	}
	for _, e := range byKey {
		wr.Events.DeleteEvent(e.GetName())
	}
	return nil
}

// claimStored gives each incoming override the name and etag of the stored
// override of the same instance, so a split moves the stored record rather
// than deleting it and creating another.
func claimStored(stored, incoming []*eventv1.Event, loc *time.Location) {
	byKey := map[int64]*eventv1.Event{}
	for _, e := range stored {
		byKey[instantKey(e, loc)] = e
	}
	for _, e := range incoming {
		if old, ok := byKey[instantKey(e, loc)]; ok {
			e.Name, e.Etag = old.GetName(), old.GetEtag()
		}
	}
}

func instantKey(e *eventv1.Event, loc *time.Location) int64 {
	at, _, _ := allocation.Instant(e.GetOriginalStart(), loc)
	return at.UnixNano()
}

// refusedSplit renders a split the service refuses as the precondition a
// calendar client understands, RFC 4791 section 5.3.2.1.
func refusedSplit(err error) error {
	var p *problem.Problem
	if errors.As(err, &p) && (errors.Is(err, repox.ErrInvalidArgument) || errors.Is(err, servicex.ErrFailedPrecondition)) {
		return caldav.Errorf(http.StatusForbidden, caldav.ValidCalendarObjectResource, "%s", p.Error())
	}
	return err
}

// deleteObject queues the soft delete of an object on wr: an availability,
// or an event with the overrides of its instances.
func deleteObject(wr directory.Writes, o caldav.Object) {
	if o.Availability != nil {
		wr.Availabilities.DeleteAvailability(o.Availability.GetName())
		return
	}
	for _, e := range o.Overrides {
		wr.Events.DeleteEvent(e.GetName())
	}
	wr.Events.DeleteEvent(o.Event.GetName())
}

// parentOf is the collection an event sits in.
func parentOf(name string) string {
	if i := strings.LastIndex(name, "/events/"); i >= 0 {
		return name[:i]
	}
	return name
}

func lastSegment(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}
