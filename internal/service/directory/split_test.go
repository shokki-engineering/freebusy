// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

import (
	"errors"
	"slices"
	"testing"
	"time"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/dayofweek"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

var madrid = func() *time.Location {
	l, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		panic(err)
	}
	return l
}()

// zoned is a DATE-TIME in an IANA zone; utc one in UTC; floating one with
// neither; day a DATE.
func zoned(t time.Time) *eventv1.CalendarTime {
	return timeForm{loc: t.Location(), zone: &datetime.TimeZone{Id: t.Location().String()}}.render(t)
}

func utc(t time.Time) *eventv1.CalendarTime {
	return timeForm{loc: time.UTC, offset: &durationpb.Duration{}}.render(t)
}

func floating(t time.Time) *eventv1.CalendarTime { return timeForm{loc: t.Location()}.render(t) }

func day(y, m, d int) *eventv1.CalendarTime {
	return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_Date{Date: &date.Date{Year: int32(y), Month: int32(m), Day: int32(d)}}}
}

// at reads a calendar time back as an instant in loc.
func at(t *testing.T, ct *eventv1.CalendarTime, loc *time.Location) time.Time {
	t.Helper()
	v, _, err := readTime(ct, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// weekly is a one-hour weekly series starting at start, written by mk.
func weekly(start time.Time, mk func(time.Time) *eventv1.CalendarTime, rule *eventv1.Recurrence) *eventv1.Event {
	if rule == nil {
		rule = &eventv1.Recurrence{}
	}
	if rule.Frequency == eventv1.Frequency_FREQUENCY_UNSPECIFIED {
		rule.Frequency = eventv1.Frequency_FREQUENCY_WEEKLY
	}
	return &eventv1.Event{
		Name: "calendars/c/events/maint", IcalUid: "maint@example.com", Etag: "e1", Sequence: 2,
		Summary: "Maintenance", Start: mk(start), Ends: &eventv1.Event_End{End: mk(start.Add(time.Hour))},
		Recurrence: rule,
	}
}

// edit is a THISANDFUTURE override of the instance at orig, moved to start.
func edit(orig, start time.Time, mk func(time.Time) *eventv1.CalendarTime) *eventv1.Event {
	return &eventv1.Event{
		IcalUid: "maint@example.com", Summary: "Resurfacing",
		OriginalStart: mk(orig), OriginalStartRange: eventv1.RecurrenceRange_RECURRENCE_RANGE_THIS_AND_FUTURE,
		Start: mk(start), Ends: &eventv1.Event_End{End: mk(start.Add(time.Hour))},
	}
}

// starts lists e's occurrence starts in window, as the engine reads them.
func starts(t *testing.T, e *eventv1.Event, overrides []*eventv1.Event, loc *time.Location, window engine.Span) []time.Time {
	t.Helper()
	if e == nil {
		return nil
	}
	got, _, err := allocation.Occurrences(e, overrides, loc, window)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustPlan(t *testing.T, series *eventv1.Event, overrides []*eventv1.Event, ed *eventv1.Event, loc *time.Location) (*eventv1.Event, *eventv1.Event, []*eventv1.Event, []*eventv1.Event) {
	t.Helper()
	truncated, tail, rehomed, dropped, err := planSplit(series, overrides, ed, loc)
	if err != nil {
		t.Fatal(err)
	}
	if truncated.GetOriginalStart() != nil || truncated.GetOriginalStartRange() != eventv1.RecurrenceRange_RECURRENCE_RANGE_UNSPECIFIED {
		t.Fatalf("the series kept a RECURRENCE-ID or RANGE: %v", truncated)
	}
	if tail != nil && (tail.GetOriginalStart() != nil || tail.GetOriginalStartRange() != eventv1.RecurrenceRange_RECURRENCE_RANGE_UNSPECIFIED) {
		t.Fatalf("the tail kept a RECURRENCE-ID or RANGE: %v", tail)
	}
	return truncated, tail, rehomed, dropped
}

func reasonIs(err error, reason string) bool {
	var p *problem.Problem
	return errors.As(err, &p) && p.Reason == reason
}

var mon = time.Date(2027, 1, 4, 10, 0, 0, 0, time.UTC) // a Monday

func TestSplitWeeklyUnbounded(t *testing.T) {
	series := weekly(mon, utc, nil)
	s := mon.AddDate(0, 0, 14)
	truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s.Add(2*time.Hour), utc), time.UTC)

	until := truncated.GetRecurrence().GetUntil()
	if until == nil || !at(t, until, time.UTC).Equal(mon.AddDate(0, 0, 7)) || until.GetDateTime().GetUtcOffset() == nil {
		t.Fatalf("UNTIL %v, want the second instance in UTC", until)
	}
	if truncated.GetSequence() != 3 || truncated.GetSummary() != "Maintenance" || truncated.GetEtag() != "e1" {
		t.Fatalf("truncated %v", truncated)
	}
	if tail.GetRecurrence().GetBound() != nil || tail.GetSummary() != "Resurfacing" {
		t.Fatalf("tail rule %v summary %q", tail.GetRecurrence(), tail.GetSummary())
	}
	if !at(t, tail.GetStart(), time.UTC).Equal(s.Add(2 * time.Hour)) {
		t.Fatalf("tail starts %v", tail.GetStart())
	}
	if tail.GetIcalUid() != "maint_R20270118T100000Z@example.com" || !slices.Contains(tail.GetRefids(), "maint@example.com") {
		t.Fatalf("tail uid %q refids %v", tail.GetIcalUid(), tail.GetRefids())
	}
	if tail.GetName() != "calendars/c/events/maint-r20270118t100000z" || tail.GetEtag() != "" {
		t.Fatalf("tail name %q etag %q", tail.GetName(), tail.GetEtag())
	}
}

func TestSplitCount(t *testing.T) {
	series := weekly(mon, utc, &eventv1.Recurrence{Bound: &eventv1.Recurrence_Count{Count: 10}})
	s := mon.AddDate(0, 0, 21) // the 4th
	truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s, utc), time.UTC)
	if truncated.GetRecurrence().GetCount() != 3 || tail.GetRecurrence().GetCount() != 7 {
		t.Fatalf("COUNT %d and %d, want 3 and 7", truncated.GetRecurrence().GetCount(), tail.GetRecurrence().GetCount())
	}
	window := engine.Span{Start: mon, End: mon.AddDate(1, 0, 0)}
	if a, b := len(starts(t, truncated, nil, time.UTC, window)), len(starts(t, tail, nil, time.UTC, window)); a != 3 || b != 7 {
		t.Fatalf("%d and %d instances, want 3 and 7", a, b)
	}
}

func TestSplitUntilMoves(t *testing.T) {
	end := utc(mon.AddDate(0, 0, 70))
	series := weekly(mon, utc, &eventv1.Recurrence{Bound: &eventv1.Recurrence_Until{Until: end}})
	s := mon.AddDate(0, 0, 28)
	truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s.Add(time.Hour), utc), time.UTC)
	// UNTIL moves with the instances, so the last one is still in.
	if !proto.Equal(tail.GetRecurrence().GetUntil(), utc(mon.AddDate(0, 0, 70).Add(time.Hour))) {
		t.Fatalf("tail UNTIL %v, want %v moved by an hour", tail.GetRecurrence().GetUntil(), end)
	}
	if !at(t, truncated.GetRecurrence().GetUntil(), time.UTC).Equal(mon.AddDate(0, 0, 21)) {
		t.Fatalf("truncated UNTIL %v", truncated.GetRecurrence().GetUntil())
	}
}

func TestSplitAllDay(t *testing.T) {
	series := &eventv1.Event{
		Name: "calendars/c/events/closure", IcalUid: "closure", Etag: "e",
		Start: day(2027, 1, 4), Recurrence: &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_DAILY},
	}
	ed := &eventv1.Event{IcalUid: "closure", OriginalStart: day(2027, 1, 8), OriginalStartRange: eventv1.RecurrenceRange_RECURRENCE_RANGE_THIS_AND_FUTURE, Start: day(2027, 1, 8)}
	truncated, tail, _, _ := mustPlan(t, series, nil, ed, madrid)
	until := truncated.GetRecurrence().GetUntil().GetDate()
	if until == nil || until.GetDay() != 7 || until.GetMonth() != 1 {
		t.Fatalf("UNTIL %v, want DATE 2027-01-07", truncated.GetRecurrence().GetUntil())
	}
	if tail.GetStart().GetDate().GetDay() != 8 {
		t.Fatalf("tail start %v", tail.GetStart())
	}
}

// Weekly at 10:00 Madrid from 2027-03-08, split on 29 March: the clocks went
// forward on the 28th, but every instance stays at 10:00 local and UNTIL is
// the last one before the split, in UTC.
func TestSplitAcrossDST(t *testing.T) {
	start := time.Date(2027, 3, 8, 10, 0, 0, 0, madrid)
	series := weekly(start, zoned, nil)
	s := time.Date(2027, 3, 29, 10, 0, 0, 0, madrid)
	truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s.Add(time.Hour), zoned), madrid)
	until := truncated.GetRecurrence().GetUntil()
	if got := at(t, until, time.UTC); !got.Equal(time.Date(2027, 3, 22, 9, 0, 0, 0, time.UTC)) || until.GetDateTime().GetUtcOffset() == nil {
		t.Fatalf("UNTIL %v (%v), want 2027-03-22T09:00:00Z", until, got)
	}
	window := engine.Span{Start: s, End: s.AddDate(0, 0, 21)}
	for _, got := range starts(t, tail, nil, madrid, window) {
		if l := got.In(madrid); l.Hour() != 11 {
			t.Fatalf("tail instance at %v, want 11:00 Madrid", l)
		}
	}
}

func TestSplitFloating(t *testing.T) {
	start := time.Date(2027, 1, 4, 10, 0, 0, 0, madrid)
	series := weekly(start, floating, nil)
	s := start.AddDate(0, 0, 14)
	truncated, _, _, _ := mustPlan(t, series, nil, edit(s, s, floating), madrid)
	dt := truncated.GetRecurrence().GetUntil().GetDateTime()
	if dt == nil || dt.GetTimeOffset() != nil || dt.GetDay() != 11 || dt.GetHours() != 10 {
		t.Fatalf("UNTIL %v, want floating 2027-01-11T10:00", dt)
	}
}

func TestSplitExdateRdate(t *testing.T) {
	series := weekly(mon, utc, nil)
	series.ExcludedTimes = []*eventv1.CalendarTime{utc(mon.AddDate(0, 0, 7)), utc(mon.AddDate(0, 0, 28))}
	series.AdditionalTimes = []*eventv1.CalendarTime{utc(mon.AddDate(0, 0, 2)), utc(mon.AddDate(0, 0, 30))}
	s := mon.AddDate(0, 0, 14)
	delta := 3 * time.Hour
	truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s.Add(delta), utc), time.UTC)
	if len(truncated.GetExcludedTimes()) != 1 || len(truncated.GetAdditionalTimes()) != 1 {
		t.Fatalf("truncated EXDATE %v RDATE %v", truncated.GetExcludedTimes(), truncated.GetAdditionalTimes())
	}
	if len(tail.GetExcludedTimes()) != 1 || !at(t, tail.GetExcludedTimes()[0], time.UTC).Equal(mon.AddDate(0, 0, 28).Add(delta)) {
		t.Fatalf("tail EXDATE %v", tail.GetExcludedTimes())
	}
	if len(tail.GetAdditionalTimes()) != 1 || !at(t, tail.GetAdditionalTimes()[0], time.UTC).Equal(mon.AddDate(0, 0, 30).Add(delta)) {
		t.Fatalf("tail RDATE %v", tail.GetAdditionalTimes())
	}
}

func TestSplitOverrides(t *testing.T) {
	series := weekly(mon, utc, nil)
	override := func(name string, week int, start time.Time, status eventv1.Confirmation) *eventv1.Event {
		orig := mon.AddDate(0, 0, 7*week)
		return &eventv1.Event{
			Name: "calendars/c/events/" + name, Etag: name, IcalUid: series.GetIcalUid(),
			OriginalStart: utc(orig), Start: utc(start), Ends: &eventv1.Event_End{End: utc(start.Add(time.Hour))},
			Confirmation: status,
		}
	}
	before := override("before", 1, mon.AddDate(0, 0, 7).Add(time.Hour), 0)
	atS := override("at", 2, mon.AddDate(0, 0, 14), 0)
	moved := override("moved", 3, mon.AddDate(0, 0, 22), 0)
	unmoved := override("unmoved", 4, mon.AddDate(0, 0, 28), 0)
	cancelled := override("cancelled", 5, mon.AddDate(0, 0, 35), eventv1.Confirmation_CONFIRMATION_CANCELLED)
	all := []*eventv1.Event{before, atS, moved, unmoved, cancelled}
	s, delta := mon.AddDate(0, 0, 14), 2*time.Hour

	_, tail, rehomed, dropped := mustPlan(t, series, all, edit(s, s.Add(delta), utc), time.UTC)
	if len(dropped) != 1 || dropped[0].GetName() != atS.GetName() {
		t.Fatalf("dropped %v, want the override at the split", dropped)
	}
	if len(rehomed) != 3 {
		t.Fatalf("rehomed %d overrides, want 3", len(rehomed))
	}
	byName := map[string]*eventv1.Event{}
	for _, o := range rehomed {
		if o.GetIcalUid() != tail.GetIcalUid() {
			t.Fatalf("override %s not re-homed: uid %q", o.GetName(), o.GetIcalUid())
		}
		byName[o.GetName()] = o
	}
	if o := byName[moved.GetName()]; !at(t, o.GetOriginalStart(), time.UTC).Equal(mon.AddDate(0, 0, 21).Add(delta)) ||
		!at(t, o.GetStart(), time.UTC).Equal(mon.AddDate(0, 0, 22)) {
		t.Fatalf("moved override %v", o)
	}
	if o := byName[unmoved.GetName()]; !at(t, o.GetStart(), time.UTC).Equal(mon.AddDate(0, 0, 28).Add(delta)) {
		t.Fatalf("unmoved override kept its start: %v", o.GetStart())
	}
	if o := byName[cancelled.GetName()]; o.GetConfirmation() != eventv1.Confirmation_CONFIRMATION_CANCELLED {
		t.Fatalf("cancelled override un-cancelled: %v", o)
	}
	if before.GetIcalUid() != series.GetIcalUid() {
		t.Fatal("the override before the split was changed")
	}
	// Re-homed onto the tail, every override still names a tail instance.
	window := engine.Span{Start: s, End: s.AddDate(0, 2, 0)}
	inst := starts(t, tail, nil, time.UTC, window)
	for _, o := range rehomed {
		if !slices.ContainsFunc(inst, at(t, o.GetOriginalStart(), time.UTC).Equal) {
			t.Fatalf("override %s names %v, not an instance of the tail", o.GetName(), o.GetOriginalStart())
		}
	}
}

func TestSplitAtFirstInstance(t *testing.T) {
	series := weekly(mon, utc, &eventv1.Recurrence{Bound: &eventv1.Recurrence_Count{Count: 5}})
	o := &eventv1.Event{Name: "calendars/c/events/o", IcalUid: series.GetIcalUid(), OriginalStart: utc(mon.AddDate(0, 0, 7)), Start: utc(mon.AddDate(0, 0, 7))}
	whole, tail, rehomed, _ := mustPlan(t, series, []*eventv1.Event{o}, edit(mon, mon.Add(time.Hour), utc), time.UTC)
	if tail != nil {
		t.Fatalf("a split at the first instance made a tail: %v", tail)
	}
	if whole.GetName() != series.GetName() || whole.GetEtag() != "e1" || whole.GetSummary() != "Resurfacing" ||
		whole.GetRecurrence().GetCount() != 5 || !at(t, whole.GetStart(), time.UTC).Equal(mon.Add(time.Hour)) || whole.GetSequence() != 3 {
		t.Fatalf("whole-series edit %v", whole)
	}
	if len(rehomed) != 1 || rehomed[0].GetIcalUid() != series.GetIcalUid() || !at(t, rehomed[0].GetOriginalStart(), time.UTC).Equal(mon.AddDate(0, 0, 7).Add(time.Hour)) {
		t.Fatalf("overrides of a whole-series edit: %v", rehomed)
	}
}

func TestSplitRefusesNonInstance(t *testing.T) {
	series := weekly(mon, utc, nil)
	series.ExcludedTimes = []*eventv1.CalendarTime{utc(mon.AddDate(0, 0, 14))}
	for _, s := range []time.Time{mon.AddDate(0, 0, 15), mon.AddDate(0, 0, 14)} {
		if _, _, _, _, err := planSplit(series, nil, edit(s, s, utc), time.UTC); !reasonIs(err, "NOT_AN_INSTANCE") {
			t.Fatalf("split at %v: %v", s, err)
		}
	}
	single := &eventv1.Event{Name: "calendars/c/events/x", Start: utc(mon)}
	if _, _, _, _, err := planSplit(single, nil, edit(mon, mon, utc), time.UTC); !reasonIs(err, "NOT_RECURRING") {
		t.Fatalf("split of a single event: %v", err)
	}
}

// A cancelled THISANDFUTURE deletes this and every following instance: the
// series ends before it, the later overrides go, and nothing carries on.
func TestSplitCancelled(t *testing.T) {
	series := weekly(mon, utc, nil)
	later := &eventv1.Event{Name: "calendars/c/events/later", IcalUid: series.GetIcalUid(), OriginalStart: utc(mon.AddDate(0, 0, 28)), Start: utc(mon.AddDate(0, 0, 28))}
	s := mon.AddDate(0, 0, 14)
	ed := edit(s, s, utc)
	ed.Confirmation = eventv1.Confirmation_CONFIRMATION_CANCELLED
	truncated, tail, rehomed, dropped := mustPlan(t, series, []*eventv1.Event{later}, ed, time.UTC)
	if tail != nil || len(rehomed) != 0 || len(dropped) != 1 || dropped[0] != later {
		t.Fatalf("tail %v rehomed %v dropped %v", tail, rehomed, dropped)
	}
	if got := starts(t, truncated, nil, time.UTC, engine.Span{Start: mon, End: mon.AddDate(1, 0, 0)}); len(got) != 2 {
		t.Fatalf("%d instances left, want 2", len(got))
	}
	if truncated.GetConfirmation() == eventv1.Confirmation_CONFIRMATION_CANCELLED {
		t.Fatal("the series before the split was cancelled")
	}
}

func TestSplitShiftsByday(t *testing.T) {
	rule := &eventv1.Recurrence{Weekdays: []*eventv1.WeekdayNum{{Day: dayofweek.DayOfWeek_MONDAY}, {Day: dayofweek.DayOfWeek_WEDNESDAY}}}
	series := weekly(mon, utc, rule)
	s := mon.AddDate(0, 0, 14)
	_, tail, _, _ := mustPlan(t, series, nil, edit(s, s.AddDate(0, 0, 1), utc), time.UTC)
	days := []dayofweek.DayOfWeek{tail.GetRecurrence().GetWeekdays()[0].GetDay(), tail.GetRecurrence().GetWeekdays()[1].GetDay()}
	if days[0] != dayofweek.DayOfWeek_TUESDAY || days[1] != dayofweek.DayOfWeek_THURSDAY {
		t.Fatalf("tail BYDAY %v, want TU,TH", days)
	}
	if series.GetRecurrence().GetWeekdays()[0].GetDay() != dayofweek.DayOfWeek_MONDAY {
		t.Fatal("the original's rule was changed in place")
	}
	// The second Monday of the month cannot be shifted by a day and stay
	// the same rule.
	monthly := weekly(mon.AddDate(0, 0, 7), utc, &eventv1.Recurrence{
		Frequency: eventv1.Frequency_FREQUENCY_MONTHLY,
		Weekdays:  []*eventv1.WeekdayNum{{Day: dayofweek.DayOfWeek_MONDAY, Ordinal: 2}},
	})
	s = time.Date(2027, 3, 8, 10, 0, 0, 0, time.UTC)
	if _, _, _, _, err := planSplit(monthly, nil, edit(s, s.AddDate(0, 0, 1), utc), time.UTC); !reasonIs(err, "RANGE_NOT_SUPPORTED") {
		t.Fatalf("shifting BYDAY=2MO: %v", err)
	}
}

func TestSplitNames(t *testing.T) {
	s := time.Date(2027, 1, 25, 10, 0, 0, 0, madrid)
	for in, want := range map[string]string{"a@example.com": "a_R20270125T090000Z@example.com", "plain": "plain_R20270125T090000Z"} {
		if got := tailUID(in, s); got != want {
			t.Errorf("tailUID(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"calendars/c/events/Weekly.ics": "calendars/c/events/Weekly_R20270125T090000Z.ics",
		"calendars/c/events/weekly":     "calendars/c/events/weekly-r20270125t090000z",
	} {
		if got := TailName(in, s); got != want {
			t.Errorf("TailName(%q) = %q, want %q", in, got, want)
		}
	}
	// Planned twice, a split names the same tail both times.
	series := weekly(mon, utc, nil)
	a := mon.AddDate(0, 0, 7)
	_, t1, _, _ := mustPlan(t, series, nil, edit(a, a, utc), time.UTC)
	_, t2, _, _ := mustPlan(t, series, nil, edit(a, a, utc), time.UTC)
	if t1.GetIcalUid() != t2.GetIcalUid() || t1.GetName() != t2.GetName() {
		t.Fatal("a split is not deterministic")
	}
}

// The property a split must keep: the original's instances and the tail's
// together are the original's, with those from S on moved by the edit's shift.
func TestSplitOccurrencesProperty(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		mk    func(time.Time) *eventv1.CalendarTime
		rule  *eventv1.Recurrence
		loc   *time.Location
	}{
		{"weekly count", mon, utc, &eventv1.Recurrence{Bound: &eventv1.Recurrence_Count{Count: 12}}, time.UTC},
		{"daily until", mon, utc, &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_DAILY, Bound: &eventv1.Recurrence_Until{Until: utc(mon.AddDate(0, 0, 40))}}, time.UTC},
		{"madrid weekly", time.Date(2027, 2, 1, 10, 0, 0, 0, madrid), zoned, nil, madrid},
		{"floating daily interval", time.Date(2027, 3, 1, 9, 30, 0, 0, madrid), floating, &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_DAILY, Interval: 3}, madrid},
	}
	for _, c := range cases {
		for _, nth := range []int{0, 1, 2, 5, 9} {
			for _, delta := range []time.Duration{0, 90 * time.Minute, -2 * time.Hour, 24 * time.Hour} {
				series := weekly(c.start, c.mk, proto.Clone(orEmpty(c.rule)).(*eventv1.Recurrence))
				window := engine.Span{Start: c.start.AddDate(0, 0, -1), End: c.start.AddDate(0, 6, 0)}
				orig := starts(t, series, nil, c.loc, window)
				if nth >= len(orig) {
					continue
				}
				s := orig[nth]
				truncated, tail, _, _ := mustPlan(t, series, nil, edit(s, s.Add(delta), c.mk), c.loc)
				var want []time.Time
				for _, o := range orig {
					if !o.Before(s) {
						o = o.Add(delta)
					}
					want = append(want, o)
				}
				wide := engine.Span{Start: window.Start, End: window.End.Add(delta + 48*time.Hour)}
				got := append(starts(t, truncated, nil, c.loc, wide), starts(t, tail, nil, c.loc, wide)...)
				got = slices.DeleteFunc(got, func(g time.Time) bool { return !g.Before(window.End.Add(delta)) && !slices.ContainsFunc(want, g.Equal) })
				slices.SortFunc(got, time.Time.Compare)
				if len(got) < len(want) {
					t.Fatalf("%s split at #%d by %v: %d instances, want %d", c.name, nth, delta, len(got), len(want))
				}
				for i := range want {
					if !got[i].Equal(want[i]) {
						t.Fatalf("%s split at #%d by %v: instance %d at %v, want %v", c.name, nth, delta, i, got[i], want[i])
					}
				}
			}
		}
	}
}

func orEmpty(r *eventv1.Recurrence) *eventv1.Recurrence {
	if r == nil {
		return &eventv1.Recurrence{}
	}
	return r
}
