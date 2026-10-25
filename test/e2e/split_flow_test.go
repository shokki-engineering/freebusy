// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// The THISANDFUTURE flow of the server e2e suite: an override carrying
// original_start_range splits its series over gRPC, as a CalDAV
// RECURRENCE-ID;RANGE=THISANDFUTURE does.
package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// splitFlow creates a weekly series of six, then -- with CreateEvent and
// original_start_range -- moves it two hours later from its third instance:
// the series keeps two instances, and the call returns the new series of
// four.
func splitFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()
	id := "split-" + c.suffix
	day := time.Now().UTC().AddDate(0, 0, 40).Truncate(24 * time.Hour)
	at := func(week, h int) time.Time { return day.AddDate(0, 0, 7*week).Add(time.Duration(h) * time.Hour) }
	ct := func(t time.Time) *eventv1.CalendarTime {
		return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: utc(t)}}
	}

	cal, err := c.calendars.CreateCalendar(ctx, &calendarv1.CreateCalendarRequest{
		CalendarId: id, Calendar: &calendarv1.Calendar{DisplayName: id, TimeZone: &datetime.TimeZone{Id: "UTC"}},
	})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	t.Cleanup(func() {
		all, _, _ := c.repos.Events.List(context.Background(), cal.GetName(), repox.ListInput{PageSize: 100, ShowDeleted: true})
		for _, e := range all {
			c.purge(t, c.repos.Events.Purge, e.GetName())
		}
		c.purge(t, c.repos.Calendars.Purge, cal.GetName())
	})
	uid := id + "@example.com"
	series, err := c.events.CreateEvent(ctx, &eventv1.CreateEventRequest{
		Parent: cal.GetName(), EventId: id,
		Event: &eventv1.Event{
			IcalUid: uid, Summary: "lessons",
			Start: ct(at(0, 9)), Ends: &eventv1.Event_End{End: ct(at(0, 10))},
			Recurrence: &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_WEEKLY, Bound: &eventv1.Recurrence_Count{Count: 6}},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent series: %v", err)
	}
	override := func(orig, start time.Time) *eventv1.CreateEventRequest {
		return &eventv1.CreateEventRequest{Parent: cal.GetName(), Event: &eventv1.Event{
			IcalUid: uid, Summary: "evening lessons",
			OriginalStart: ct(orig), OriginalStartRange: eventv1.RecurrenceRange_RECURRENCE_RANGE_THIS_AND_FUTURE,
			Start: ct(start), Ends: &eventv1.Event_End{End: ct(start.Add(time.Hour))},
		}}
	}

	// Not an instance: refused, nothing written.
	_, err = c.events.CreateEvent(ctx, override(at(2, 10), at(2, 11)))
	wantReason(t, err, codes.InvalidArgument, "NOT_AN_INSTANCE")
	bad := override(at(2, 9), at(2, 11))
	bad.Event.IcalUid = "nobody@example.com"
	_, err = c.events.CreateEvent(ctx, bad)
	wantReason(t, err, codes.FailedPrecondition, "SERIES_NOT_FOUND")

	tail, err := c.events.CreateEvent(ctx, override(at(2, 9), at(2, 11)))
	if err != nil {
		t.Fatalf("CreateEvent with original_start_range: %v", err)
	}
	if tail.GetRecurrence().GetCount() != 4 || tail.GetOriginalStart() != nil || tail.GetSummary() != "evening lessons" ||
		!strings.Contains(tail.GetIcalUid(), "_R") || !strings.HasPrefix(tail.GetName(), series.GetName()+"-r") {
		t.Fatalf("the split returned %v", tail)
	}
	got, err := c.events.GetEvent(ctx, &eventv1.GetEventRequest{Name: series.GetName()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRecurrence().GetCount() != 2 || got.GetSequence() != series.GetSequence()+1 {
		t.Fatalf("the original after the split: %v", got.GetRecurrence())
	}
	window := &interval.Interval{StartTime: timestamppb.New(day), EndTime: timestamppb.New(day.AddDate(0, 3, 0))}
	exp, err := c.events.ExpandEvent(ctx, &eventv1.ExpandEventRequest{Name: tail.GetName(), Window: window})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(exp.GetEvents()); n != 4 {
		t.Fatalf("the tail expands to %d instances, want 4", n)
	}
	if h := exp.GetEvents()[0].GetStart().GetDateTime().GetHours(); h != 11 {
		t.Fatalf("the tail's first instance at %02d:00, want 11:00", h)
	}
}

// wantReason asserts err's code and ErrorInfo reason.
func wantReason(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	wantCode(t, err, code, reason)
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetReason() == reason {
			return
		}
	}
	t.Fatalf("error %v lacks reason %s", err, reason)
}
