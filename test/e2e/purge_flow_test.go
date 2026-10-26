// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
	"google.golang.org/genproto/googleapis/type/datetime"
)

// purgeFlow pins that the purger's writes land on both backends: it stamps
// expire_time on a soft-deleted calendar, and clears it once the calendar is
// undeleted. The retention is long enough that the sweep purges nothing in
// the shared database.
func purgeFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()
	cal, err := c.calendars.CreateCalendar(ctx, &calendarv1.CreateCalendarRequest{
		CalendarId: "purge-" + c.suffix,
		Calendar:   &calendarv1.Calendar{DisplayName: "e2e purge", TimeZone: &datetime.TimeZone{Id: "UTC"}},
	})
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Calendars.Purge, cal.GetName()) })
	if _, err := c.calendars.DeleteCalendar(ctx, &calendarv1.DeleteCalendarRequest{Name: cal.GetName()}); err != nil {
		t.Fatalf("delete calendar: %v", err)
	}

	const retention = 1000 * 24 * time.Hour
	purger := directory.NewPurger(c.repos, retention, nil)
	stored := func() *calendarv1.Calendar {
		t.Helper()
		rows, _, err := c.repos.Calendars.List(ctx, repox.ListInput{PageSize: 1, ShowDeleted: true,
			Where: []filterx.Condition{{Field: "name", Op: filterx.OpEq, Value: cal.GetName()}}})
		if err != nil || len(rows) != 1 {
			t.Fatalf("read %s back: %v (%d rows)", cal.GetName(), err, len(rows))
		}
		return rows[0]
	}

	if _, err := purger.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := stored()
	if got.GetDeleteTime() == nil || got.GetExpireTime() == nil {
		t.Fatalf("deleted calendar after a sweep = %v, want expire_time stamped", got)
	}
	if want := got.GetDeleteTime().AsTime().Add(retention); !got.GetExpireTime().AsTime().Equal(want) {
		t.Errorf("expire_time = %v, want delete_time + retention = %v", got.GetExpireTime().AsTime(), want)
	}

	if _, err := c.calendars.UndeleteCalendar(ctx, &calendarv1.UndeleteCalendarRequest{Name: cal.GetName()}); err != nil {
		t.Fatalf("undelete calendar: %v", err)
	}
	if _, err := purger.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stored(); got.GetDeleteTime() != nil || got.GetExpireTime() != nil {
		t.Errorf("undeleted calendar after a sweep = %v, want live with no expire_time", got)
	}
}
