// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

import (
	"context"
	"fmt"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/calendar/v1/calendarv1grpc"
	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/emptypb"
)

// CalendarServer serves RFC 5545 calendars, the containers a resource's
// availability and bookings live on, and answers free/busy for them.
type CalendarServer struct {
	calendarv1grpc.UnimplementedCalendarsServer
	repos    Repositories
	freeBusy FreeBusyQuerier
}

// NewCalendarServer returns a CalendarServer over repos, answering
// QueryFreeBusy from fb.
func NewCalendarServer(repos Repositories, fb FreeBusyQuerier) *CalendarServer {
	return &CalendarServer{repos: repos, freeBusy: fb}
}

const calendarService = "Calendars"

func (s *CalendarServer) GetCalendar(ctx context.Context, req *calendarv1.GetCalendarRequest) (*calendarv1.Calendar, error) {
	var out *calendarv1.Calendar
	err := rpc.Traced(ctx, calendarService, "GetCalendar", func(ctx context.Context) (err error) {
		out, err = s.repos.Calendars.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *CalendarServer) ListCalendars(ctx context.Context, req *calendarv1.ListCalendarsRequest) (*calendarv1.ListCalendarsResponse, error) {
	out := &calendarv1.ListCalendarsResponse{}
	err := rpc.Traced(ctx, calendarService, "ListCalendars", func(ctx context.Context) (err error) {
		out.Calendars, out.NextPageToken, err = s.repos.Calendars.List(ctx,
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *CalendarServer) CreateCalendar(ctx context.Context, req *calendarv1.CreateCalendarRequest) (*calendarv1.Calendar, error) {
	var out *calendarv1.Calendar
	err := rpc.Traced(ctx, calendarService, "CreateCalendar", func(ctx context.Context) error {
		if err := checkID("calendar_id", req.GetCalendarId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetCalendar()
		name := ""
		if id := req.GetCalendarId(); id != "" {
			name = "calendars/" + id
		}
		if err := PrepareCreate(in, name); err != nil {
			return rpc.ToStatusErr(err)
		}
		var err error
		out, err = s.repos.Calendars.Create(ctx, in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *CalendarServer) UpdateCalendar(ctx context.Context, req *calendarv1.UpdateCalendarRequest) (*calendarv1.Calendar, error) {
	var out *calendarv1.Calendar
	err := rpc.Traced(ctx, calendarService, "UpdateCalendar", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetCalendar(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Calendars.Update(ctx, req.GetCalendar(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// DeleteCalendar soft-deletes a calendar. Without force it refuses one that
// still has live events or availability, as AIP-135 asks of a parent; with
// force those children are soft-deleted first, so the engine stops reading
// them in the same breath. Undelete restores only the calendar itself.
func (s *CalendarServer) DeleteCalendar(ctx context.Context, req *calendarv1.DeleteCalendarRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, calendarService, "DeleteCalendar", func(ctx context.Context) error {
		name := req.GetName()
		if _, err := s.repos.Calendars.Get(ctx, name); err != nil {
			return rpc.ToStatusErr(err)
		}
		if err := s.deleteChildren(ctx, name, req.GetForce()); err != nil {
			return rpc.ToStatusErr(err)
		}
		return rpc.ToStatusErr(s.repos.Calendars.Delete(ctx, name))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// deleteChildren soft-deletes the live events and availability under
// calendar when force is set, or reports them as a failed precondition when
// it is not.
func (s *CalendarServer) deleteChildren(ctx context.Context, calendar string, force bool) error {
	for {
		events, _, err := s.repos.Events.List(ctx, calendar, listInput(1000, "", "", false))
		if err != nil {
			return err
		}
		avs, _, err := s.repos.Availabilities.List(ctx, calendar, listInput(1000, "", "", false))
		if err != nil {
			return err
		}
		if len(events) == 0 && len(avs) == 0 {
			return nil
		}
		if !force {
			return types.Blocked("CALENDAR_NOT_EMPTY", hints.TypeOf(calendar), calendar,
				fmt.Sprintf("calendar %s still has events or availability", calendar),
				"Send force=true to soft-delete them with it, or delete them first; UndeleteCalendar restores the calendar alone, so restore its events and availability one by one")
		}
		// Each pass deletes what it listed, so the next pass lists what is
		// left; the loop ends when nothing live remains.
		for _, e := range events {
			if err := s.repos.Events.Delete(ctx, e.GetName()); err != nil && !isNotFound(err) {
				return err
			}
		}
		for _, a := range avs {
			if err := s.repos.Availabilities.Delete(ctx, a.GetName()); err != nil && !isNotFound(err) {
				return err
			}
		}
	}
}

func (s *CalendarServer) UndeleteCalendar(ctx context.Context, req *calendarv1.UndeleteCalendarRequest) (*calendarv1.Calendar, error) {
	var out *calendarv1.Calendar
	err := rpc.Traced(ctx, calendarService, "UndeleteCalendar", func(ctx context.Context) (err error) {
		out, err = s.repos.Calendars.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// QueryFreeBusy reports the calendar's busy time over the window, from the
// engine: opaque events and converted holds are BUSY, live holds
// BUSY_TENTATIVE, and time its availability does not offer
// BUSY_UNAVAILABLE. Free time is not listed; it is whatever the periods
// leave. Periods come back in start order and never overlap.
func (s *CalendarServer) QueryFreeBusy(ctx context.Context, req *calendarv1.QueryFreeBusyRequest) (*calendarv1.QueryFreeBusyResponse, error) {
	out := &calendarv1.QueryFreeBusyResponse{}
	err := rpc.Traced(ctx, calendarService, "QueryFreeBusy", func(ctx context.Context) error {
		window, err := windowOf(req.GetWindow())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		cal, err := s.repos.Calendars.Get(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		fb, err := s.freeBusy.FreeBusy(ctx, cal.GetName(), cal.GetTimeZone().GetId(), window)
		if err != nil {
			return rpc.ToStatusErr(err)
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
