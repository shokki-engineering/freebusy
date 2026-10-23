// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Zone suite: a recurring event written in a zone keeps that zone through
// storage, so its instances stay at the same wall-clock time across a DST
// change -- on both backends, through ExpandEvent and through CalDAV.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/shokki-engineering/freebusy/internal/caldav"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/rfcsvc"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestZone_Gorm(t *testing.T) {
	zoneFlow(t, &database.Connection{PgSQLConn: openTestGorm(t), Provider: database.ProviderGorm})
}

func TestZone_Hasura(t *testing.T) {
	zoneFlow(t, &database.Connection{Hasura: connectTestGraphQL(t), Provider: database.ProviderHasura})
}

func zoneFlow(t *testing.T, conn *database.Connection) {
	ctx := context.Background()
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	repos := rfcsvc.NewRepositories(conn.Repox(), 0)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	calName := "calendars/zone-" + suffix
	if _, err := repos.Calendars.Create(ctx, &calendarv1.Calendar{Name: calName, DisplayName: "zone", TimeZone: &datetime.TimeZone{Id: "Europe/Madrid"}}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repos.Calendars.Purge(ctx, calName) }()

	// Mondays at 10:00 Madrid from 15 March 2027: two in CET (+01:00), then
	// the clocks go forward on 28 March, two in CEST (+02:00).
	at := func(day int) *eventv1.CalendarTime {
		return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: &datetime.DateTime{
			Year: 2027, Month: 3, Day: int32(day), Hours: 10,
			TimeOffset: &datetime.DateTime_TimeZone{TimeZone: &datetime.TimeZone{Id: "Europe/Madrid"}},
		}}}
	}
	e, err := repos.Events.Create(ctx, calName, &eventv1.Event{
		Name:         calName + "/events/weekly-" + suffix,
		IcalUid:      "weekly-" + suffix,
		Summary:      "weekly",
		Transparency: eventv1.Transparency_TRANSPARENCY_OPAQUE,
		Start:        at(15),
		Ends:         &eventv1.Event_End{End: &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: &datetime.DateTime{Year: 2027, Month: 3, Day: 15, Hours: 11, TimeOffset: &datetime.DateTime_TimeZone{TimeZone: &datetime.TimeZone{Id: "Europe/Madrid"}}}}}},
		Recurrence: &eventv1.Recurrence{
			Frequency: eventv1.Frequency_FREQUENCY_WEEKLY,
			Bound:     &eventv1.Recurrence_Count{Count: 4},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repos.Events.Purge(ctx, e.GetName()) }()

	// Stored and read back in the zone it was written in.
	got, err := repos.Events.Get(ctx, e.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if dt := got.GetStart().GetDateTime(); dt.GetTimeZone().GetId() != "Europe/Madrid" || dt.GetHours() != 10 {
		t.Fatalf("stored start read back as %v", dt)
	}

	// ExpandEvent: every instance at 10:00 local, before and after the change.
	exp, err := rfcsvc.NewEventServer(repos).ExpandEvent(ctx, &eventv1.ExpandEventRequest{
		Name: e.GetName(),
		Window: &interval.Interval{
			StartTime: timestamppb.New(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)),
			EndTime:   timestamppb.New(time.Date(2027, 4, 30, 0, 0, 0, 0, time.UTC)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.GetEvents()) != 4 {
		t.Fatalf("%d instances, want 4", len(exp.GetEvents()))
	}
	for i, inst := range exp.GetEvents() {
		start, _, _ := allocation.Instant(inst.GetStart(), madrid)
		local := start.In(madrid)
		if local.Hour() != 10 || local.Weekday() != time.Monday {
			t.Fatalf("instance %d at %v local, want Monday 10:00", i, local)
		}
	}

	// The engine sees the same: busy 10:00-11:00 local on the CEST Monday.
	fb, err := allocation.NewServer(db.New(conn)).FreeBusy(ctx, calName, "Europe/Madrid", engine.Span{
		Start: time.Date(2027, 3, 29, 0, 0, 0, 0, madrid), End: time.Date(2027, 3, 30, 0, 0, 0, 0, madrid)})
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Busy) != 1 || !fb.Busy[0].Start.Equal(time.Date(2027, 3, 29, 10, 0, 0, 0, madrid)) {
		t.Fatalf("busy on 29 March: %v, want 10:00-11:00 Madrid", fb.Busy)
	}

	// CalDAV writes it back with its TZID, series and instances alike.
	h := caldav.New(repos, allocation.NewServer(db.New(conn)))
	mux := runtime.NewServeMux()
	if err := h.Mount(mux); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	obj := srv.URL + "/dav/calendars/zone-" + suffix + "/weekly-" + suffix
	resp, err := http.Get(obj)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "DTSTART;TZID=Europe/Madrid:20270315T100000") {
		t.Fatalf("GET lost the zone:\n%s", body)
	}
	report := `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data><C:expand start="20270301T000000Z" end="20270430T000000Z"/></C:calendar-data></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`
	req, _ := http.NewRequest("REPORT", srv.URL+"/dav/calendars/zone-"+suffix+"/", strings.NewReader(report))
	req.Header.Set("Depth", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{"DTSTART;TZID=Europe/Madrid:20270322T100000", "DTSTART;TZID=Europe/Madrid:20270329T100000"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("expand missing %s:\n%s", want, body)
		}
	}
}
