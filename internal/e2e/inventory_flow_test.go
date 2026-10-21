// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// The inventory flow of the server e2e suite: an operator publishing what the
// engine books from, through the upstream protobuf-rfc services.
package e2e

import (
	"context"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// inventoryFlow publishes a court -- calendar, resource, opening hours 08:00 to
// 18:00 daily, a weekly maintenance hour -- and checks that QueryFreeBusy,
// ExpandEvent and the engine all read the same thing back. Then it deletes
// the calendar and checks AIP-164 soft delete end to end.
func inventoryFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()
	id := "inv-" + c.suffix
	site := c.site(t, id)
	day := time.Now().UTC().AddDate(0, 0, 40).Truncate(24 * time.Hour)
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }
	span := func(a, b time.Time) *interval.Interval {
		return &interval.Interval{StartTime: timestamppb.New(a), EndTime: timestamppb.New(b)}
	}

	_, err := c.resources.CreateResource(ctx, &resourcev1.CreateResourceRequest{ResourceId: "Not_An_Id", Resource: &resourcev1.Resource{}})
	wantCode(t, err, codes.InvalidArgument, "an id outside AIP-122")

	cal, err := c.calendars.CreateCalendar(ctx, &calendarv1.CreateCalendarRequest{
		CalendarId: id,
		Calendar: &calendarv1.Calendar{
			DisplayName: "court " + id, TimeZone: &datetime.TimeZone{Id: "UTC"},
			Etag: "client-set", // OUTPUT_ONLY: the server's, not the caller's
		},
	})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if cal.GetName() != "calendars/"+id || cal.GetEtag() == "client-set" {
		t.Fatalf("calendar %v", cal)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Calendars.Purge, cal.GetName()) })

	res, err := c.resources.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		ResourceId: id,
		Resource:   &resourcev1.Resource{DisplayName: "court", Calendar: cal.GetName(), OrganizationalUnit: site, ResourceTypes: []string{"court"}},
	})
	if err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Resources.Purge, res.GetName()) })

	av, err := c.availabilities.CreateAvailability(ctx, &availabilityv1.CreateAvailabilityRequest{
		Parent: cal.GetName(),
		Availability: &availabilityv1.Availability{
			Start: &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: utc(day)}},
			AvailablePeriods: []*availabilityv1.AvailablePeriod{{
				Start:          &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: utc(at(8))}},
				EndForm:        &availabilityv1.AvailablePeriod_Duration{Duration: durationpb.New(10 * time.Hour)},
				RecurrenceRule: "FREQ=DAILY",
			}},
		},
	})
	if err != nil {
		t.Fatalf("CreateAvailability: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Availabilities.Purge, av.GetName()) })

	ev, err := c.events.CreateEvent(ctx, &eventv1.CreateEventRequest{
		Parent: cal.GetName(),
		Event: &eventv1.Event{
			Summary:      "maintenance",
			Transparency: eventv1.Transparency_TRANSPARENCY_OPAQUE,
			Start:        &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: utc(at(10))}},
			Ends:         &eventv1.Event_End{End: &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: utc(at(11))}}},
			Recurrence: &eventv1.Recurrence{
				Frequency: eventv1.Frequency_FREQUENCY_WEEKLY,
				Bound:     &eventv1.Recurrence_Count{Count: 3},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Events.Purge, ev.GetName()) })

	// The engine reads what was just published: closed at 07:00, booked by
	// maintenance at 10:00, free at 12:00.
	free := func(a, b time.Time) int32 {
		t.Helper()
		r, err := c.availability.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Parent: site, Window: span(a, b)})
		if err != nil {
			t.Fatalf("CheckAvailability: %v", err)
		}
		return r.GetFreeCount()
	}
	if got := []int32{free(at(7), at(8)), free(at(10), at(11)), free(at(12), at(13))}; got[0] != 0 || got[1] != 0 || got[2] != 1 {
		t.Fatalf("free at 07, 10, 12: %v, want [0 0 1]", got)
	}

	hold, err := c.holds.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: site, Hold: &allocationpbv1.Hold{Window: span(at(12), at(13))}})
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	fb, err := c.calendars.QueryFreeBusy(ctx, &calendarv1.QueryFreeBusyRequest{Name: cal.GetName(), Window: span(at(6), at(20))})
	if err != nil {
		t.Fatalf("QueryFreeBusy: %v", err)
	}
	type period struct {
		a, b int
		kind calendarv1.BusyType
	}
	want := []period{
		{6, 8, calendarv1.BusyType_BUSY_TYPE_BUSY_UNAVAILABLE},
		{10, 11, calendarv1.BusyType_BUSY_TYPE_BUSY},
		{12, 13, calendarv1.BusyType_BUSY_TYPE_BUSY_TENTATIVE},
		{18, 20, calendarv1.BusyType_BUSY_TYPE_BUSY_UNAVAILABLE},
	}
	if len(fb.GetPeriods()) != len(want) {
		t.Fatalf("free/busy %v, want %v", fb.GetPeriods(), want)
	}
	for i, w := range want {
		p := fb.GetPeriods()[i]
		if !p.GetPeriod().GetStartTime().AsTime().Equal(at(w.a)) || !p.GetPeriod().GetEndTime().AsTime().Equal(at(w.b)) || p.GetBusyType() != w.kind {
			t.Fatalf("period %d: %v, want %v", i, p, w)
		}
	}
	if _, err := c.holds.ReleaseHold(ctx, &allocationpbv1.ReleaseHoldRequest{Name: hold.GetName()}); err != nil {
		t.Fatalf("ReleaseHold: %v", err)
	}

	// Three weekly instances, two to a page, each in the event's own form.
	page1, err := c.events.ExpandEvent(ctx, &eventv1.ExpandEventRequest{Name: ev.GetName(), Window: span(day, day.AddDate(0, 0, 28)), PageSize: 2})
	if err != nil {
		t.Fatalf("ExpandEvent: %v", err)
	}
	page2, err := c.events.ExpandEvent(ctx, &eventv1.ExpandEventRequest{Name: ev.GetName(), Window: span(day, day.AddDate(0, 0, 28)), PageSize: 2, PageToken: page1.GetNextPageToken()})
	if err != nil {
		t.Fatalf("ExpandEvent page 2: %v", err)
	}
	instances := append(page1.GetEvents(), page2.GetEvents()...)
	if len(page1.GetEvents()) != 2 || page2.GetNextPageToken() != "" || len(instances) != 3 {
		t.Fatalf("pages of %d and %d, next %q", len(page1.GetEvents()), len(page2.GetEvents()), page2.GetNextPageToken())
	}
	for i, inst := range instances {
		if got := instant(inst.GetStart().GetDateTime()); !got.Equal(at(10).AddDate(0, 0, 7*i)) || inst.GetName() != "" || inst.GetRecurrence() != nil {
			t.Fatalf("instance %d: start %v, name %q", i, got, inst.GetName())
		}
	}
	_, err = c.events.ExpandEvent(ctx, &eventv1.ExpandEventRequest{Name: ev.GetName(), Window: span(at(2), at(1))})
	wantCode(t, err, codes.InvalidArgument, "a window ending before it starts")

	// AIP-135: a calendar with children refuses a plain delete; force takes
	// them with it, softly, and the engine stops reading them at once.
	_, err = c.calendars.DeleteCalendar(ctx, &calendarv1.DeleteCalendarRequest{Name: cal.GetName()})
	wantCode(t, err, codes.FailedPrecondition, "deleting a calendar that has events")
	if _, err := c.calendars.DeleteCalendar(ctx, &calendarv1.DeleteCalendarRequest{Name: cal.GetName(), Force: true}); err != nil {
		t.Fatalf("forced DeleteCalendar: %v", err)
	}
	live, err := c.events.ListEvents(ctx, &eventv1.ListEventsRequest{Parent: cal.GetName()})
	if err != nil || len(live.GetEvents()) != 0 {
		t.Fatalf("live events after delete: %v %v", err, live.GetEvents())
	}
	all, err := c.events.ListEvents(ctx, &eventv1.ListEventsRequest{Parent: cal.GetName(), ShowDeleted: true})
	if err != nil || len(all.GetEvents()) != 1 || all.GetEvents()[0].GetDeleteTime() == nil {
		t.Fatalf("show_deleted events: %v %v", err, all.GetEvents())
	}
	if n := free(at(10), at(11)); n != 1 {
		t.Fatalf("maintenance still blocks after delete: %d free", n)
	}
	_, err = c.calendars.UndeleteCalendar(ctx, &calendarv1.UndeleteCalendarRequest{Name: cal.GetName()})
	if err != nil {
		t.Fatalf("UndeleteCalendar: %v", err)
	}
	_, err = c.calendars.UndeleteCalendar(ctx, &calendarv1.UndeleteCalendarRequest{Name: cal.GetName()})
	wantCode(t, err, codes.AlreadyExists, "undeleting a live calendar")
}

func utc(t time.Time) *datetime.DateTime {
	return &datetime.DateTime{
		Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day()),
		Hours: int32(t.Hour()), Minutes: int32(t.Minute()),
		TimeOffset: &datetime.DateTime_UtcOffset{UtcOffset: durationpb.New(0)},
	}
}

func instant(d *datetime.DateTime) time.Time {
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), int(d.GetHours()), int(d.GetMinutes()), int(d.GetSeconds()), 0, time.UTC).
		Add(-d.GetUtcOffset().AsDuration())
}
