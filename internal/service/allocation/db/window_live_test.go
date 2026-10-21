// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// Live test of Events' window: events far before or after the window are not
// returned, on either backend, and on GORM the database already leaves them
// out. Skipped unless FREEBUSY_TEST_DATABASE_URL names a database.

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestLiveEventsWindow(t *testing.T) {
	g := liveDB(t)
	for backend, open := range stores(t) {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := open(g).(*Store)
			cal := fmt.Sprintf("calendars/window-%s-%d", backend, time.Now().UnixNano()%1_000_000_000)
			if _, err := s.calendars.Create(ctx, &calendarv1.Calendar{Name: cal, DisplayName: "window"}); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.calendars.Purge(ctx, cal) }()

			utc := func(y, m, d, h int) *eventv1.CalendarTime {
				return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: &datetime.DateTime{
					Year: int32(y), Month: int32(m), Day: int32(d), Hours: int32(h),
					TimeOffset: &datetime.DateTime_TimeZone{TimeZone: &datetime.TimeZone{Id: "UTC"}},
				}}}
			}
			allDay := func(y, m, d int) *eventv1.CalendarTime {
				return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_Date{Date: &date.Date{Year: int32(y), Month: int32(m), Day: int32(d)}}}
			}
			end := func(c *eventv1.CalendarTime) *eventv1.Event_End { return &eventv1.Event_End{End: c} }
			// The window is 1 June 2030; each event is named for whether
			// Events keeps it.
			events := map[string]*eventv1.Event{
				"keep-inside":       {Start: utc(2030, 6, 1, 10), Ends: end(utc(2030, 6, 1, 11))},
				"keep-allday":       {Start: allDay(2030, 6, 1)},
				"keep-spanning":     {Start: utc(2030, 5, 1, 0), Ends: end(utc(2030, 7, 1, 0))},
				"keep-weekly-old":   {Start: utc(2001, 1, 1, 10), Ends: end(utc(2001, 1, 1, 11)), Recurrence: &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_WEEKLY}},
				"drop-past":         {Start: utc(2001, 1, 1, 10), Ends: end(utc(2001, 1, 1, 11))},
				"drop-past-allday":  {Start: allDay(2001, 1, 1), Ends: end(allDay(2001, 1, 2))},
				"drop-past-lasting": {Start: utc(2001, 1, 1, 10), Ends: &eventv1.Event_Duration{Duration: durationpb.New(time.Hour)}},
				"drop-future":       {Start: utc(2099, 1, 1, 10), Ends: end(utc(2099, 1, 1, 11))},
				"drop-future-week":  {Start: allDay(2099, 1, 1), Recurrence: &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_WEEKLY}},
			}
			for id, e := range events {
				e.Name, e.IcalUid, e.Summary = cal+"/events/"+id, id, id
				if _, err := s.events.Create(ctx, cal, e); err != nil {
					t.Fatalf("%s: %v", id, err)
				}
				defer func() { _ = s.events.Purge(ctx, e.GetName()) }()
			}

			window := engine.Span{Start: time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2030, 6, 2, 0, 0, 0, 0, time.UTC)}
			got, err := s.Events(ctx, []string{cal}, window)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(got[cal]); n != 4 {
				t.Fatalf("Events returned %d events, want the 4 keep-* ones", n)
			}

			// What the database itself returns for the hinted read.
			lo, hi := window.Start.Add(-24*time.Hour), window.End.Add(24*time.Hour)
			rows, _, err := s.events.List(ctx, acrossCalendars, repox.ListInput{
				PageSize: 100,
				Where:    append([]filterx.Condition{filterx.In("calendar", cal)}, windowHints(lo, hi)...),
			})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rows {
				ids = append(ids, r.GetIcalUid())
			}
			slices.Sort(ids)
			want := []string{"drop-past-lasting", "keep-allday", "keep-inside", "keep-spanning", "keep-weekly-old"}
			if backend == "hasura" {
				// DDN exposes no path from an event to its times, so the
				// hints are skipped and Events' own checks narrow.
				if len(ids) != len(events) {
					t.Fatalf("hasura read %d rows, want all %d: %v", len(ids), len(events), ids)
				}
				return
			}
			// An open-ended event (DURATION, no DTEND) passes the database
			// and is dropped by Events.
			if !slices.Equal(ids, want) {
				t.Fatalf("gorm read %v, want %v", ids, want)
			}
		})
	}
}
