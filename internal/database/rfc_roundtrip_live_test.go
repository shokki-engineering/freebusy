package database_test

// Live round-trip tests of the generated RFC repositories: each writes a full
// value-object graph -- nested times, repeated children, a recurrence, a
// time zone -- reads it back, replaces part of it, and deletes it. They pin
// that the generator persists the deep graphs the availability engine reads.
//
//	FREEBUSY_TEST_DATABASE_URL="host=127.0.0.1 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" \
//	  go test ./internal/database/ -run LiveRFC -v

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/availability"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/calendar"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/event"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/resource"
	"github.com/oh-tarnished/freebusy/internal/livetest"
	"github.com/oh-tarnished/runtime-go/ulid"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/dayofweek"
	"google.golang.org/protobuf/types/known/durationpb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func liveConns(t *testing.T) map[string]repox.Conn {
	t.Helper()
	dsn := livetest.Env(t, livetest.DatabaseURL)
	db, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]repox.Conn{"gorm": {Gorm: db}}
	if raw := livetest.Optional(t, livetest.GraphQLURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := freebusyql.Connect(u)
		if err != nil {
			t.Fatal(err)
		}
		out["hasura"] = repox.Conn{GraphQL: svc}
	}
	return out
}

func dt(t time.Time) *datetime.DateTime {
	u := t.UTC()
	return &datetime.DateTime{
		Year: int32(u.Year()), Month: int32(u.Month()), Day: int32(u.Day()),
		Hours: int32(u.Hour()), Minutes: int32(u.Minute()),
		TimeOffset: &datetime.DateTime_UtcOffset{UtcOffset: durationpb.New(0)},
	}
}

func instant(d *datetime.DateTime) time.Time {
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), int(d.GetHours()), int(d.GetMinutes()), int(d.GetSeconds()), 0, time.UTC).
		Add(-d.GetUtcOffset().AsDuration())
}

func TestLiveRFCRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	for backend, conn := range liveConns(t) {
		t.Run(backend, func(t *testing.T) {
			run := ulid.GenerateString()

			t.Run("calendar keeps its time zone", func(t *testing.T) {
				cals := calendar.New(conn).Calendars
				c, err := cals.Create(ctx, &calendarv1.Calendar{
					Name: "calendars/rt-" + run, DisplayName: "rt", TimeZone: &datetime.TimeZone{Id: "Asia/Kolkata"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if c.GetTimeZone().GetId() != "Asia/Kolkata" {
					t.Fatalf("time zone %v", c.GetTimeZone())
				}
				// A second calendar in the same zone shares the zone row.
				if _, err := cals.Create(ctx, &calendarv1.Calendar{
					Name: "calendars/rt2-" + run, DisplayName: "rt2", TimeZone: &datetime.TimeZone{Id: "Asia/Kolkata"},
				}); err != nil {
					t.Fatalf("second calendar in a known zone: %v", err)
				}
				if err := cals.Delete(ctx, c.GetName()); err != nil {
					t.Fatal(err)
				}
			})

			t.Run("resource keeps structured data in order", func(t *testing.T) {
				res := resource.New(conn).Resources
				r, err := res.Create(ctx, &resourcev1.Resource{
					Name: "resources/rt-" + run, OrganizationalUnit: "organizationalUnits/rt-" + run,
					StructuredData: []*resourcev1.StructuredData{
						{Schema: "https://example.com/a", Value: &resourcev1.StructuredData_Text{Text: `{"min_duration":"PT1H"}`}},
						{Schema: "https://example.com/b", Value: &resourcev1.StructuredData_Uri{Uri: "https://example.com/doc"}},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				sd := r.GetStructuredData()
				if len(sd) != 2 || sd[0].GetSchema() != "https://example.com/a" || sd[0].GetText() != `{"min_duration":"PT1H"}` || sd[1].GetUri() != "https://example.com/doc" {
					t.Fatalf("structured data %v", sd)
				}
				r.StructuredData = r.StructuredData[:1]
				up, err := res.Update(ctx, r, []string{"structured_data"})
				if err != nil {
					t.Fatal(err)
				}
				if len(up.GetStructuredData()) != 1 {
					t.Fatalf("after replacing the list: %v", up.GetStructuredData())
				}
				if err := res.Delete(ctx, r.GetName()); err != nil {
					t.Fatal(err)
				}
			})

			t.Run("availability keeps its periods", func(t *testing.T) {
				cal := "calendars/rta-" + run
				if _, err := calendar.New(conn).Calendars.Create(ctx, &calendarv1.Calendar{Name: cal, DisplayName: "rta"}); err != nil {
					t.Fatal(err)
				}
				avs := availability.New(conn).Availabilities
				a, err := avs.Create(ctx, cal, &availabilityv1.Availability{
					Priority: 5,
					AvailablePeriods: []*availabilityv1.AvailablePeriod{
						{
							Start:          &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: dt(base)}},
							EndForm:        &availabilityv1.AvailablePeriod_End{End: &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: dt(base.Add(14 * time.Hour))}}},
							RecurrenceRule: "FREQ=DAILY",
						},
						{
							Start:          &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: dt(base.Add(24 * time.Hour))}},
							EndForm:        &availabilityv1.AvailablePeriod_Duration{Duration: durationpb.New(2 * time.Hour)},
							RecurrenceRule: "FREQ=WEEKLY;BYDAY=TU",
						},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				ps := a.GetAvailablePeriods()
				if len(ps) != 2 {
					t.Fatalf("periods %v", ps)
				}
				if got := instant(ps[0].GetStart().GetDateTime()); !got.Equal(base) {
					t.Fatalf("period 0 start %v, want %v", got, base)
				}
				if got := instant(ps[0].GetEnd().GetDateTime()); !got.Equal(base.Add(14 * time.Hour)) {
					t.Fatalf("period 0 end %v", got)
				}
				if ps[0].GetRecurrenceRule() != "FREQ=DAILY" || ps[1].GetRecurrenceRule() != "FREQ=WEEKLY;BYDAY=TU" {
					t.Fatalf("rules out of order: %v / %v", ps[0].GetRecurrenceRule(), ps[1].GetRecurrenceRule())
				}
				if ps[1].GetDuration().AsDuration() != 2*time.Hour {
					t.Fatalf("period 1 duration %v", ps[1].GetDuration())
				}

				a.AvailablePeriods = a.AvailablePeriods[1:]
				up, err := avs.Update(ctx, a, []string{"available_periods"})
				if err != nil {
					t.Fatal(err)
				}
				if len(up.GetAvailablePeriods()) != 1 || up.GetAvailablePeriods()[0].GetRecurrenceRule() != "FREQ=WEEKLY;BYDAY=TU" {
					t.Fatalf("after update: %v", up.GetAvailablePeriods())
				}
				listed := func(showDeleted bool) int {
					t.Helper()
					got, _, err := avs.List(ctx, cal, repox.ListInput{ShowDeleted: showDeleted})
					if err != nil {
						t.Fatal(err)
					}
					return len(got)
				}

				// AIP-164: a delete marks the record; it stays readable, leaves
				// List, and comes back on Undelete. Purge removes it for good.
				if err := avs.Delete(ctx, a.GetName()); err != nil {
					t.Fatal(err)
				}
				if err := avs.Delete(ctx, a.GetName()); !errors.Is(err, repox.ErrNotFound) {
					t.Fatalf("second delete: %v", err)
				}
				got, err := avs.Get(ctx, a.GetName())
				if err != nil || got.GetDeleteTime() == nil {
					t.Fatalf("soft-deleted get: %v, delete_time %v", err, got.GetDeleteTime())
				}
				if n := listed(false); n != 0 {
					t.Fatalf("list shows %d deleted records", n)
				}
				if n := listed(true); n != 1 {
					t.Fatalf("show_deleted lists %d, want 1", n)
				}
				back, err := avs.Undelete(ctx, a.GetName())
				if err != nil || back.GetDeleteTime() != nil || len(back.GetAvailablePeriods()) != 1 {
					t.Fatalf("undelete: %v, %v", err, back)
				}
				if _, err := avs.Undelete(ctx, a.GetName()); !errors.Is(err, repox.ErrAlreadyExists) {
					t.Fatalf("undelete of a live record: %v", err)
				}
				if n := listed(false); n != 1 {
					t.Fatalf("list after undelete: %d", n)
				}
				if err := avs.Purge(ctx, a.GetName()); err != nil {
					t.Fatal(err)
				}
				if _, err := avs.Get(ctx, a.GetName()); !errors.Is(err, repox.ErrNotFound) {
					t.Fatalf("after purge: %v", err)
				}
			})

			t.Run("event keeps times and recurrence", func(t *testing.T) {
				cal := "calendars/rte-" + run
				if _, err := calendar.New(conn).Calendars.Create(ctx, &calendarv1.Calendar{Name: cal, DisplayName: "rte"}); err != nil {
					t.Fatal(err)
				}
				evs := event.New(conn).Events
				e, err := evs.Create(ctx, cal, &eventv1.Event{
					Summary:      "maintenance",
					Transparency: eventv1.Transparency_TRANSPARENCY_OPAQUE,
					Start:        &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dt(base)}},
					Ends:         &eventv1.Event_End{End: &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dt(base.Add(time.Hour))}}},
					Recurrence: &eventv1.Recurrence{
						Frequency: eventv1.Frequency_FREQUENCY_WEEKLY,
						Interval:  2,
						Weekdays:  []*eventv1.WeekdayNum{{Day: dayofweek.DayOfWeek_MONDAY}, {Day: dayofweek.DayOfWeek_THURSDAY}},
						Bound:     &eventv1.Recurrence_Until{Until: &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dt(base.AddDate(0, 3, 0))}}},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := instant(e.GetStart().GetDateTime()); !got.Equal(base) {
					t.Fatalf("start %v", got)
				}
				if got := instant(e.GetEnd().GetDateTime()); !got.Equal(base.Add(time.Hour)) {
					t.Fatalf("end %v", got)
				}
				rec := e.GetRecurrence()
				if rec.GetFrequency() != eventv1.Frequency_FREQUENCY_WEEKLY || rec.GetInterval() != 2 || len(rec.GetWeekdays()) != 2 ||
					rec.GetWeekdays()[0].GetDay() != dayofweek.DayOfWeek_MONDAY || rec.GetWeekdays()[1].GetDay() != dayofweek.DayOfWeek_THURSDAY {
					t.Fatalf("recurrence %v", rec)
				}
				if got := instant(rec.GetUntil().GetDateTime()); !got.Equal(base.AddDate(0, 3, 0)) {
					t.Fatalf("until %v", got)
				}
				if err := evs.Delete(ctx, e.GetName()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
