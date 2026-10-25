// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) // a Monday

// at returns base plus h hours.
func at(h float64) time.Time { return base.Add(time.Duration(h * float64(time.Hour))) }

func sp(a, b float64) Span { return Span{Start: at(a), End: at(b)} }

func TestNormalizeMergesTouchingAndOverlapping(t *testing.T) {
	got := Normalize([]Span{sp(5, 6), sp(1, 2), sp(2, 3), sp(2.5, 4), sp(7, 7)})
	want := Spans{sp(1, 4), sp(5, 6)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize = %v, want %v", got, want)
	}
}

func TestSubtract(t *testing.T) {
	cases := []struct {
		name string
		s, o Spans
		want Spans
	}{
		{"holes", Spans{sp(0, 10)}, Spans{sp(2, 3), sp(5, 6)}, Spans{sp(0, 2), sp(3, 5), sp(6, 10)}},
		{"covers start", Spans{sp(0, 10)}, Spans{sp(-1, 3)}, Spans{sp(3, 10)}},
		{"covers all", Spans{sp(0, 10)}, Spans{sp(-1, 11)}, nil},
		{"spans two", Spans{sp(0, 4), sp(6, 10)}, Spans{sp(3, 7)}, Spans{sp(0, 3), sp(7, 10)}},
		{"disjoint", Spans{sp(0, 1)}, Spans{sp(2, 3)}, Spans{sp(0, 1)}},
		{"touching does not remove", Spans{sp(0, 2)}, Spans{sp(2, 3)}, Spans{sp(0, 2)}},
	}
	for _, c := range cases {
		if got := c.s.Subtract(c.o); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Subtract = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIntersectAndCovers(t *testing.T) {
	a := Spans{sp(0, 4), sp(6, 10)}
	b := Spans{sp(3, 7), sp(9, 12)}
	if got, want := a.Intersect(b), (Spans{sp(3, 4), sp(6, 7), sp(9, 10)}); !reflect.DeepEqual(got, want) {
		t.Fatalf("Intersect = %v, want %v", got, want)
	}
	if !a.Covers(sp(6, 10)) || a.Covers(sp(3, 7)) || a.Covers(sp(4, 6)) {
		t.Fatal("Covers wrong")
	}
	if !a.AnyOverlap(sp(3.5, 5)) || a.AnyOverlap(sp(4, 6)) {
		t.Fatal("AnyOverlap wrong: touching endpoints must not overlap")
	}
}

func TestParseRRule(t *testing.T) {
	r, err := ParseRRule("RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;COUNT=4;WKST=MO", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r.Freq != FreqWeekly || r.Interval != 2 || r.Count != 4 || len(r.ByDay) != 2 {
		t.Fatalf("parsed %+v", r)
	}
	for _, bad := range []string{"", "FREQ=HOURLY", "FREQ=WEEKLY;BYDAY=2MO", "FREQ=DAILY;BYSETPOS=1", "BYDAY=MO"} {
		if _, err := ParseRRule(bad, time.UTC); !errors.Is(err, ErrUnsupportedRule) {
			t.Errorf("ParseRRule(%q) err = %v, want ErrUnsupportedRule", bad, err)
		}
	}
}

func TestExpandWeekdaysKeepsWallClockAcrossDST(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no tzdata")
	}
	r, _ := ParseRRule("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", london)
	// DST ends in London on Sunday 2026-10-25.
	dtstart := time.Date(2026, 1, 5, 9, 0, 0, 0, london)
	window := Span{Start: time.Date(2026, 10, 22, 0, 0, 0, 0, london), End: time.Date(2026, 10, 28, 0, 0, 0, 0, london)}
	got := r.Expand(dtstart, 8*time.Hour, window)
	// Thu 22, Fri 23, Mon 26, Tue 27.
	if len(got) != 4 {
		t.Fatalf("got %d occurrences: %v", len(got), got)
	}
	for _, occ := range got {
		if l := occ.Start.In(london); l.Hour() != 9 {
			t.Errorf("occurrence %v starts at %d:00 local, want 09:00", l, l.Hour())
		}
	}
	if got[1].Start.UTC().Hour() != 8 || got[2].Start.UTC().Hour() != 9 {
		t.Errorf("UTC offsets did not shift across DST: %v", got)
	}
}

func TestExpandCountAndUntil(t *testing.T) {
	r, _ := ParseRRule("FREQ=DAILY;COUNT=3", time.UTC)
	got := r.Expand(at(9), time.Hour, Span{Start: at(0), End: at(24 * 10)})
	if len(got) != 3 {
		t.Fatalf("COUNT=3 gave %d", len(got))
	}
	r, _ = ParseRRule("FREQ=DAILY;UNTIL=20261021T235959Z", time.UTC)
	got = r.Expand(at(9), time.Hour, Span{Start: at(0), End: at(24 * 10)})
	if len(got) != 3 { // 19th, 20th, 21st
		t.Fatalf("UNTIL gave %d: %v", len(got), got)
	}
}

func TestExpandMonthlyNegativeDay(t *testing.T) {
	r, _ := ParseRRule("FREQ=MONTHLY;BYMONTHDAY=-1", time.UTC)
	start := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	win := Span{Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	got := r.Expand(start, time.Hour, win)
	var days []int
	for _, o := range got {
		days = append(days, o.Start.Day())
	}
	if !reflect.DeepEqual(days, []int{28, 31, 30}) {
		t.Fatalf("last-day-of-month = %v", days)
	}
}

func TestOfferedNoAvailabilityIsOpen(t *testing.T) {
	w := sp(0, 24)
	if got := Offered(nil, w); !reflect.DeepEqual(got, Spans{w}) {
		t.Fatalf("Offered(nil) = %v", got)
	}
}

func TestOfferedPriorityOverride(t *testing.T) {
	weekdays, _ := ParseRRule("FREQ=DAILY", time.UTC)
	// Low priority: open 09-17 every day, forever.
	normal := Availability{Priority: 9, Periods: []Period{{Start: at(9), End: at(17), Rule: &weekdays}}}
	// High priority: Tuesday is a holiday -- covered, no periods.
	holiday := Availability{Priority: 1, Range: sp(24, 48)}
	got := Offered([]Availability{normal, holiday}, sp(0, 72))
	want := Spans{sp(9, 17), sp(48+9, 48+17)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Offered = %v, want %v", got, want)
	}
}

func TestOfferedRangeLeavesOutsideOpen(t *testing.T) {
	// A pattern bounded to day 2 says nothing about days 1 and 3.
	a := Availability{Range: sp(24, 48), Periods: []Period{{Start: at(30), End: at(32)}}}
	got := Offered([]Availability{a}, sp(0, 72))
	want := Spans{sp(0, 24), sp(30, 32), sp(48, 72)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Offered = %v, want %v", got, want)
	}
}

func TestOfferedOpenEndedRangeStartsAtDTSTART(t *testing.T) {
	daily, _ := ParseRRule("FREQ=DAILY", time.UTC)
	// Open 09-17 daily from day 1 on, with no end: nothing before day 1.
	a := Availability{Range: Span{Start: at(24)}, Periods: []Period{{Start: at(24 + 9), End: at(24 + 17), Rule: &daily}}}
	got := Offered([]Availability{a}, sp(0, 72))
	want := Spans{sp(24+9, 24+17), sp(48+9, 48+17)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Offered = %v, want %v", got, want)
	}
	// Another component covering time before it still offers that time.
	early := Availability{Range: sp(0, 12), Periods: []Period{{Start: at(10), End: at(11)}}}
	got = Offered([]Availability{a, early}, sp(0, 72))
	want = Spans{sp(10, 11), sp(24+9, 24+17), sp(48+9, 48+17)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Offered with an earlier component = %v, want %v", got, want)
	}
}

func TestBucketsSlotDropsPartial(t *testing.T) {
	got, err := Buckets(sp(9, 11.5), GranularitySlot, time.Hour, time.UTC)
	if err != nil || len(got) != 2 || got[1] != sp(10, 11) {
		t.Fatalf("Buckets = %v, %v", got, err)
	}
}

func TestBucketsDayAnchorsAtWindowStart(t *testing.T) {
	// Check-in 15:00 day 0, check-out 11:00 day 3: three nights.
	got, err := Buckets(sp(15, 72+11), GranularityAuto, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := []Span{sp(15, 39), sp(39, 63), sp(63, 83)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Buckets = %v, want %v", got, want)
	}
}

func TestBucketsCap(t *testing.T) {
	if _, err := Buckets(sp(0, 24*365), GranularitySlot, time.Minute, time.UTC); !errors.Is(err, ErrTooManyBuckets) {
		t.Fatalf("err = %v", err)
	}
}

func courts() []Resource {
	open := Spans{sp(8, 22)}
	return []Resource{
		{Name: "resources/court-1", Offered: open, Booked: Spans{sp(17, 18)}},
		{Name: "resources/court-2", Offered: open, Held: Spans{sp(19, 20)}},
		{Name: "resources/court-3", Offered: open},
		{Name: "resources/court-4", Offered: Spans{sp(8, 12)}},
		{Name: "resources/court-5", Offered: open, Inactive: true},
	}
}

func TestCompute(t *testing.T) {
	got := Compute(courts(), []Span{sp(17, 18), sp(18, 19), sp(19, 20)})
	counts := []int{len(got[0].Free), len(got[1].Free), len(got[2].Free)}
	if !reflect.DeepEqual(counts, []int{2, 3, 2}) {
		t.Fatalf("free counts = %v", counts)
	}
	if got[0].Total != 5 {
		t.Fatalf("total = %d", got[0].Total)
	}
}

func TestCheckReasons(t *testing.T) {
	res := Check(courts(), sp(19, 20), 3, at(0))
	if res.Bookable {
		t.Fatal("3 courts at 19:00 should not be bookable")
	}
	got := map[string]Reason{}
	for _, r := range res.Reasons {
		got[r.Resource] = r.Reason
	}
	want := map[string]Reason{
		"":                  ReasonNoCapacity,
		"resources/court-2": ReasonHeld,
		"resources/court-4": ReasonOutsideAvailability,
		"resources/court-5": ReasonResourceInactive,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
}

func TestRankBestFit(t *testing.T) {
	rs := courts()
	// court-1 is free 18-22 (4h gap); court-3 is free 8-22 (14h gap).
	got := Rank(rs, sp(18, 19), at(0))
	if len(got) < 2 || got[0] != "resources/court-1" {
		t.Fatalf("Rank = %v, want court-1 first", got)
	}
}

func TestPolicy(t *testing.T) {
	p := Policy{MinDuration: time.Hour, MaxDuration: 3 * time.Hour, MinNotice: 2 * time.Hour, MaxAdvance: 30 * 24 * time.Hour}
	now := at(0)
	cases := map[Span]Reason{
		sp(10, 10.5):       ReasonWindowTooShort,
		sp(10, 14):         ReasonWindowTooLong,
		sp(1, 2):           ReasonLeadTime,
		sp(24*40, 24*40+1): ReasonLeadTime,
		sp(10, 11):         ReasonNone,
	}
	for w, want := range cases {
		if got := p.Check(w, now); got != want {
			t.Errorf("Check(%v) = %v, want %v", w, got, want)
		}
	}
}

// days lists the occurrence starts of rule from dtstart over window, as
// "2006-01-02 15:04" in dtstart's zone.
func days(t *testing.T, rule string, dtstart time.Time, window Span) []string {
	t.Helper()
	r, err := ParseRRule(rule, dtstart.Location())
	if err != nil {
		t.Fatalf("ParseRRule(%q): %v", rule, err)
	}
	var out []string
	for _, o := range r.Occurrences(dtstart, time.Hour, window) {
		out = append(out, o.Start.In(dtstart.Location()).Format("2006-01-02 15:04"))
	}
	return out
}

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

// The cases are RFC 5545 section 3.3.10's own examples, or follow from them.
func TestExpandRuleParts(t *testing.T) {
	year := Span{Start: utc(2026, 1, 1, 0), End: utc(2027, 1, 1, 0)}
	cases := []struct {
		name, rule string
		dtstart    time.Time
		window     Span
		want       []string
	}{
		{"second Tuesday monthly", "FREQ=MONTHLY;BYDAY=2TU;COUNT=3", utc(2026, 1, 13, 10), year,
			[]string{"2026-01-13 10:00", "2026-02-10 10:00", "2026-03-10 10:00"}},
		{"last Friday monthly", "FREQ=MONTHLY;BYDAY=-1FR;COUNT=3", utc(2026, 1, 30, 10), year,
			[]string{"2026-01-30 10:00", "2026-02-27 10:00", "2026-03-27 10:00"}},
		{"last weekday of the month", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3", utc(2026, 1, 30, 9), year,
			[]string{"2026-01-30 09:00", "2026-02-27 09:00", "2026-03-31 09:00"}},
		{"first and last workday", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1,-1;COUNT=4", utc(2026, 1, 1, 9), year,
			[]string{"2026-01-01 09:00", "2026-01-30 09:00", "2026-02-02 09:00", "2026-02-27 09:00"}},
		{"BYMONTH with day", "FREQ=YEARLY;BYMONTH=6,7;BYMONTHDAY=1;COUNT=3", utc(2026, 6, 1, 9), Span{Start: utc(2026, 1, 1, 0), End: utc(2028, 1, 1, 0)},
			[]string{"2026-06-01 09:00", "2026-07-01 09:00", "2027-06-01 09:00"}},
		{"yearly BYDAY spans the year", "FREQ=YEARLY;BYDAY=20MO;COUNT=2", utc(2026, 5, 18, 9), Span{Start: utc(2026, 1, 1, 0), End: utc(2028, 1, 1, 0)},
			[]string{"2026-05-18 09:00", "2027-05-17 09:00"}},
		{"several slots a day", "FREQ=DAILY;BYHOUR=9,14;BYMINUTE=0,30;COUNT=6", utc(2026, 3, 2, 9), year,
			[]string{"2026-03-02 09:00", "2026-03-02 09:30", "2026-03-02 14:00", "2026-03-02 14:30", "2026-03-03 09:00", "2026-03-03 09:30"}},
		{"BYHOUR keeps later slots on day one", "FREQ=WEEKLY;BYDAY=MO;BYHOUR=9,15;COUNT=3", utc(2026, 3, 2, 9), year,
			[]string{"2026-03-02 09:00", "2026-03-02 15:00", "2026-03-09 09:00"}},
		// RFC 5545's WKST example: the week boundary moves which Tuesday and
		// Sunday share a fortnight.
		{"WKST=MO", "FREQ=WEEKLY;INTERVAL=2;COUNT=4;BYDAY=TU,SU;WKST=MO", utc(1997, 8, 5, 9), Span{Start: utc(1997, 1, 1, 0), End: utc(1998, 1, 1, 0)},
			[]string{"1997-08-05 09:00", "1997-08-10 09:00", "1997-08-19 09:00", "1997-08-24 09:00"}},
		{"WKST=SU", "FREQ=WEEKLY;INTERVAL=2;COUNT=4;BYDAY=TU,SU;WKST=SU", utc(1997, 8, 5, 9), Span{Start: utc(1997, 1, 1, 0), End: utc(1998, 1, 1, 0)},
			[]string{"1997-08-05 09:00", "1997-08-17 09:00", "1997-08-19 09:00", "1997-08-31 09:00"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := days(t, c.rule, c.dtstart, c.window); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s\n got %v\nwant %v", c.rule, got, c.want)
			}
		})
	}
}

func TestParseRRuleRefusesWhatItCannotExpand(t *testing.T) {
	for _, bad := range []string{
		"FREQ=WEEKLY;BYDAY=2MO", // numbered BYDAY means nothing weekly
		"FREQ=DAILY;BYSETPOS=1", // nothing to pick from
		"FREQ=YEARLY;BYWEEKNO=20",
		"FREQ=YEARLY;BYYEARDAY=100",
		"FREQ=DAILY;BYSECOND=30",
		"FREQ=DAILY;BYHOUR=24",
		"FREQ=MONTHLY;BYDAY=0MO",
		"FREQ=WEEKLY;WKST=XX",
	} {
		if _, err := ParseRRule(bad, time.UTC); !errors.Is(err, ErrUnsupportedRule) {
			t.Errorf("ParseRRule(%q) err = %v, want ErrUnsupportedRule", bad, err)
		}
	}
}

func TestPickSpreadsAcrossTies(t *testing.T) {
	fits := []Fit{{"a", time.Hour}, {"b", 2 * time.Hour}, {"c", 2 * time.Hour}, {"d", 2 * time.Hour}, {"e", 3 * time.Hour}}
	if got := Pick(fits, 2, nil); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("without shuffle: %v", got)
	}
	// The tightest fit is always taken; the second place is drawn from the
	// three-way tie, never from the looser e.
	seen := map[string]bool{}
	r := rand.New(rand.NewPCG(1, 2))
	for range 100 {
		got := Pick(fits, 2, r.Shuffle)
		if len(got) != 2 || got[0] != "a" || got[1] == "e" {
			t.Fatalf("pick %v", got)
		}
		seen[got[1]] = true
	}
	if len(seen) != 3 {
		t.Fatalf("second place drawn from %v, want all of b, c, d", seen)
	}
	if got := Pick(fits, 9, r.Shuffle); len(got) != 5 {
		t.Fatalf("more than there are: %v", got)
	}
}
