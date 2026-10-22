// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Freq is an RRULE FREQ value, RFC 5545 section 3.3.10.
type Freq int

// The frequencies the engine expands. SECONDLY, MINUTELY and HOURLY are
// deliberately absent: no availability pattern or booking recurs that finely,
// and accepting them would let one rule generate millions of occurrences in a
// year-long window.
const (
	FreqDaily Freq = iota + 1
	FreqWeekly
	FreqMonthly
	FreqYearly
)

// ErrUnsupportedRule reports an RRULE using a part the engine does not expand.
//
// A distinct error rather than a best guess, because the two places rules come
// from want opposite fallbacks: an availability period the engine cannot read
// should offer nothing, while a booking it cannot read should block everything.
// Only the caller knows which it is holding.
var ErrUnsupportedRule = errors.New("engine: unsupported recurrence rule")

// Recurrence is a parsed RRULE: the subset of RFC 5545 section 3.3.10 that
// availability patterns and bookings actually use.
//
// Supported: FREQ (DAILY, WEEKLY, MONTHLY, YEARLY), INTERVAL, COUNT, UNTIL,
// BYDAY (with ordinals under MONTHLY and YEARLY: "2TU", "-1FR"), BYMONTHDAY
// (including negative days), BYMONTH, BYHOUR, BYMINUTE, BYSETPOS and WKST.
// BYSECOND, BYYEARDAY and BYWEEKNO fail with ErrUnsupportedRule.
type Recurrence struct {
	Freq     Freq
	Interval int
	// Count bounds the series by occurrence count; zero means unbounded.
	Count int
	// Until bounds the series by the last start instant, inclusive; zero means
	// unbounded.
	Until      time.Time
	ByDay      []WeekdayNum
	ByMonthDay []int
	ByMonth    []time.Month
	// ByHour and ByMinute give each occurrence day several start times; an
	// empty one takes DTSTART's hour or minute.
	ByHour   []int
	ByMinute []int
	// BySetPos keeps only the nth instants of each FREQ period, counted from
	// the end when negative: "the last weekday of the month" is
	// FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1.
	BySetPos []int
	// WeekStart is WKST, the day a week begins on; Monday when unset.
	WeekStart time.Weekday
}

// WeekdayNum is one BYDAY entry: a weekday, and with N set, only the Nth of
// it in the month or year (counted from the end when negative).
type WeekdayNum struct {
	Day time.Weekday
	N   int
}

var weekdayCodes = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// ParseRRule parses an RRULE value such as "FREQ=WEEKLY;BYDAY=MO,TU,WE". A
// leading "RRULE:" is tolerated. UNTIL in the floating or date form is read in
// loc, matching how the series' own start is interpreted.
func ParseRRule(s string, loc *time.Location) (Recurrence, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "RRULE:")
	r := Recurrence{Interval: 1, WeekStart: time.Monday}
	if s == "" {
		return r, fmt.Errorf("%w: empty rule", ErrUnsupportedRule)
	}
	for part := range strings.SplitSeq(s, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return r, fmt.Errorf("%w: malformed part %q", ErrUnsupportedRule, part)
		}
		key, val = strings.ToUpper(strings.TrimSpace(key)), strings.ToUpper(strings.TrimSpace(val))
		switch key {
		case "FREQ":
			switch val {
			case "DAILY":
				r.Freq = FreqDaily
			case "WEEKLY":
				r.Freq = FreqWeekly
			case "MONTHLY":
				r.Freq = FreqMonthly
			case "YEARLY":
				r.Freq = FreqYearly
			default:
				return r, fmt.Errorf("%w: FREQ=%s", ErrUnsupportedRule, val)
			}
		case "INTERVAL":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return r, fmt.Errorf("%w: INTERVAL=%s", ErrUnsupportedRule, val)
			}
			r.Interval = n
		case "COUNT":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return r, fmt.Errorf("%w: COUNT=%s", ErrUnsupportedRule, val)
			}
			r.Count = n
		case "UNTIL":
			t, err := parseICalTime(val, loc)
			if err != nil {
				return r, fmt.Errorf("%w: UNTIL=%s", ErrUnsupportedRule, val)
			}
			r.Until = t
		case "BYDAY":
			for code := range strings.SplitSeq(val, ",") {
				wd, ok := parseWeekdayNum(code)
				if !ok {
					return r, fmt.Errorf("%w: BYDAY=%s", ErrUnsupportedRule, code)
				}
				r.ByDay = append(r.ByDay, wd)
			}
		case "BYMONTHDAY":
			for v := range strings.SplitSeq(val, ",") {
				n, err := strconv.Atoi(v)
				if err != nil || n == 0 || n < -31 || n > 31 {
					return r, fmt.Errorf("%w: BYMONTHDAY=%s", ErrUnsupportedRule, v)
				}
				r.ByMonthDay = append(r.ByMonthDay, n)
			}
		case "BYMONTH":
			for v := range strings.SplitSeq(val, ",") {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 12 {
					return r, fmt.Errorf("%w: BYMONTH=%s", ErrUnsupportedRule, v)
				}
				r.ByMonth = append(r.ByMonth, time.Month(n))
			}
		case "BYHOUR":
			if r.ByHour, ok = parseInts(val, 0, 23, false); !ok {
				return r, fmt.Errorf("%w: BYHOUR=%s", ErrUnsupportedRule, val)
			}
		case "BYMINUTE":
			if r.ByMinute, ok = parseInts(val, 0, 59, false); !ok {
				return r, fmt.Errorf("%w: BYMINUTE=%s", ErrUnsupportedRule, val)
			}
		case "BYSETPOS":
			if r.BySetPos, ok = parseInts(val, -366, 366, true); !ok {
				return r, fmt.Errorf("%w: BYSETPOS=%s", ErrUnsupportedRule, val)
			}
		case "WKST":
			wd, ok := weekdayCodes[val]
			if !ok {
				return r, fmt.Errorf("%w: WKST=%s", ErrUnsupportedRule, val)
			}
			r.WeekStart = wd
		default:
			return r, fmt.Errorf("%w: %s", ErrUnsupportedRule, key)
		}
	}
	if r.Freq == 0 {
		return r, fmt.Errorf("%w: no FREQ", ErrUnsupportedRule)
	}
	// RFC 5545 section 3.3.10: a numbered BYDAY means "the Nth in the month or
	// year", so it has no meaning under DAILY or WEEKLY.
	if r.Freq != FreqMonthly && r.Freq != FreqYearly {
		for _, d := range r.ByDay {
			if d.N != 0 {
				return r, fmt.Errorf("%w: BYDAY=%d%s under FREQ=%s", ErrUnsupportedRule, d.N, dayCode(d.Day), freqName(r.Freq))
			}
		}
	}
	// BYSETPOS picks from the set the other BY* parts build, and alone has
	// nothing to pick from.
	if len(r.BySetPos) > 0 && len(r.ByDay)+len(r.ByMonthDay)+len(r.ByMonth)+len(r.ByHour)+len(r.ByMinute) == 0 {
		return r, fmt.Errorf("%w: BYSETPOS without another BY* part", ErrUnsupportedRule)
	}
	return r, nil
}

// parseWeekdayNum reads a BYDAY entry: "MO", "2TU", "-1FR".
func parseWeekdayNum(code string) (WeekdayNum, bool) {
	if len(code) < 2 {
		return WeekdayNum{}, false
	}
	wd, ok := weekdayCodes[code[len(code)-2:]]
	if !ok {
		return WeekdayNum{}, false
	}
	num := code[:len(code)-2]
	if num == "" {
		return WeekdayNum{Day: wd}, true
	}
	n, err := strconv.Atoi(num)
	if err != nil || n == 0 || n < -53 || n > 53 {
		return WeekdayNum{}, false
	}
	return WeekdayNum{Day: wd, N: n}, true
}

// parseInts reads a comma-separated list of integers within [lo, hi], zero
// refused when nonZero is set.
func parseInts(val string, lo, hi int, nonZero bool) ([]int, bool) {
	var out []int
	for v := range strings.SplitSeq(val, ",") {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi || (nonZero && n == 0) {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func dayCode(d time.Weekday) string {
	for code, wd := range weekdayCodes {
		if wd == d {
			return code
		}
	}
	return ""
}

func freqName(f Freq) string {
	return [...]string{"", "DAILY", "WEEKLY", "MONTHLY", "YEARLY"}[f]
}

// parseICalTime reads an RFC 5545 DATE ("20260101"), floating DATE-TIME
// ("20260101T090000") or UTC DATE-TIME ("20260101T090000Z").
func parseICalTime(v string, loc *time.Location) (time.Time, error) {
	switch {
	case strings.HasSuffix(v, "Z"):
		return time.Parse("20060102T150405Z", v)
	case strings.Contains(v, "T"):
		return time.ParseInLocation("20060102T150405", v, loc)
	default:
		d, err := time.ParseInLocation("20060102", v, loc)
		if err != nil {
			return d, err
		}
		// A date UNTIL bounds the whole day.
		return d.AddDate(0, 0, 1).Add(-time.Nanosecond), nil
	}
}

// maxOccurrenceScan bounds how many candidate days one expansion walks. A
// rule's first occurrence can sit years before the window (a weekly pattern
// started in 2019), and COUNT forces walking from the start, so the walk is
// bounded rather than trusted. 50 years of days is far beyond any real pattern.
const maxOccurrenceScan = 366 * 50

// Expand returns the occurrences of a series overlapping window. The series
// starts at dtstart -- whose wall-clock time and location fix every
// occurrence's local start, so "09:00 every weekday" stays 09:00 across a DST
// change -- and each occurrence lasts dur.
//
// The first occurrence is dtstart itself, as RFC 5545 section 3.8.5.3
// requires, even when dtstart does not match the BY* parts.
func (r Recurrence) Expand(dtstart time.Time, dur time.Duration, window Span) Spans {
	return Normalize(r.Occurrences(dtstart, dur, window))
}

// Occurrences is Expand without the merge: every occurrence overlapping
// window, in order, each its own span, so back-to-back instances stay two.
// It is what a caller listing instances needs; busy-time arithmetic wants
// Expand.
func (r Recurrence) Occurrences(dtstart time.Time, dur time.Duration, window Span) []Span {
	if dur <= 0 || window.Empty() {
		return nil
	}
	loc := dtstart.Location()
	day0 := time.Date(dtstart.Year(), dtstart.Month(), dtstart.Day(), 0, 0, 0, 0, loc)
	clocks := r.clocks(dtstart)
	sets := map[time.Time][]time.Time{} // BYSETPOS's picks, per FREQ period

	var out []Span
	emitted := 0
	emit := func(start time.Time) bool {
		if r.Count > 0 && emitted >= r.Count {
			return false
		}
		if !r.Until.IsZero() && start.After(r.Until) {
			return false
		}
		emitted++
		occ := Span{Start: start, End: start.Add(dur)}
		if occ.Overlaps(window) {
			out = append(out, occ)
		}
		return true
	}

	if !emit(dtstart) {
		return out
	}
	// Day 0 is walked too: with BYHOUR it can hold later occurrences than
	// DTSTART itself, which every candidate must follow.
	first := 0
	if r.Count == 0 {
		// Without COUNT each day is matched on its own, so the days before the
		// window can be skipped outright. Start one occurrence-length early so
		// an occurrence beginning before the window but running into it is
		// still found.
		lead := window.Start.Add(-dur).In(loc)
		leadDay := time.Date(lead.Year(), lead.Month(), lead.Day(), 0, 0, 0, 0, loc)
		if skip := daysBetween(day0, leadDay) - 1; skip > first {
			first = skip
		}
	}
	for i := first; i < maxOccurrenceScan; i++ {
		d := day0.AddDate(0, 0, i)
		// Past the window with no COUNT to honour: nothing later can matter.
		if r.Count == 0 && !r.at(d, clocks[0]).Before(window.End) {
			break
		}
		if !r.Until.IsZero() && r.at(d, clocks[0]).After(r.Until) {
			break
		}
		if !r.matches(day0, d) {
			continue
		}
		for _, c := range clocks {
			start := r.at(d, c)
			if !start.After(dtstart) {
				continue
			}
			if len(r.BySetPos) > 0 && !slices.Contains(r.picks(day0, d, clocks, sets), start) {
				continue
			}
			if !emit(start) {
				return out
			}
		}
	}
	return out
}

// clock is a wall-clock time of day.
type clock struct{ h, m, s, ns int }

// clocks are the start times on each occurrence day, in order: BYHOUR x
// BYMINUTE, each defaulting to DTSTART's.
func (r Recurrence) clocks(dtstart time.Time) []clock {
	hh, mm, ss := dtstart.Clock()
	hours, minutes := r.ByHour, r.ByMinute
	if len(hours) == 0 {
		hours = []int{hh}
	}
	if len(minutes) == 0 {
		minutes = []int{mm}
	}
	out := make([]clock, 0, len(hours)*len(minutes))
	for _, h := range hours {
		for _, m := range minutes {
			out = append(out, clock{h, m, ss, dtstart.Nanosecond()})
		}
	}
	slices.SortFunc(out, func(a, b clock) int {
		return (a.h*60+a.m)*60 + a.s - ((b.h*60+b.m)*60 + b.s)
	})
	return slices.Compact(out)
}

// at is local day d at clock c.
func (r Recurrence) at(d time.Time, c clock) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), c.h, c.m, c.s, c.ns, d.Location())
}

// picks are the instants BYSETPOS keeps in the FREQ period holding day d:
// every instant the other parts produce there, in order, indexed from 1 (or
// from -1 at the end). Instants before DTSTART still count, as RFC 5545
// section 3.3.10 applies BYSETPOS before the series' start.
func (r Recurrence) picks(day0, d time.Time, clocks []clock, sets map[time.Time][]time.Time) []time.Time {
	from, to := r.period(d)
	if got, ok := sets[from]; ok {
		return got
	}
	var all []time.Time
	for x := from; x.Before(to); x = x.AddDate(0, 0, 1) {
		if r.matches(day0, x) {
			for _, c := range clocks {
				all = append(all, r.at(x, c))
			}
		}
	}
	var kept []time.Time
	for _, pos := range r.BySetPos {
		i := pos - 1
		if pos < 0 {
			i = len(all) + pos
		}
		if i >= 0 && i < len(all) && !slices.Contains(kept, all[i]) {
			kept = append(kept, all[i])
		}
	}
	sets[from] = kept
	return kept
}

// period is the FREQ period holding day d, as [from, to) local midnights.
func (r Recurrence) period(d time.Time) (from, to time.Time) {
	switch r.Freq {
	case FreqWeekly:
		from = weekStart(d, r.WeekStart)
		return from, from.AddDate(0, 0, 7)
	case FreqMonthly:
		from = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, d.Location())
		return from, from.AddDate(0, 1, 0)
	case FreqYearly:
		from = time.Date(d.Year(), 1, 1, 0, 0, 0, 0, d.Location())
		return from, from.AddDate(1, 0, 0)
	}
	return d, d.AddDate(0, 0, 1)
}

// matches reports whether local day d (midnight) is an occurrence day of a
// series whose first day is day0.
func (r Recurrence) matches(day0, d time.Time) bool {
	if len(r.ByMonth) > 0 && !slices.Contains(r.ByMonth, d.Month()) {
		return false
	}
	switch r.Freq {
	case FreqDaily:
		days := daysBetween(day0, d)
		if days%r.Interval != 0 {
			return false
		}
		return r.byDayOK(d) && r.byMonthDayOK(d)
	case FreqWeekly:
		weeks := daysBetween(weekStart(day0, r.WeekStart), weekStart(d, r.WeekStart)) / 7
		if weeks%r.Interval != 0 {
			return false
		}
		if len(r.ByDay) == 0 {
			return d.Weekday() == day0.Weekday()
		}
		return r.byDayOK(d)
	case FreqMonthly:
		months := (d.Year()-day0.Year())*12 + int(d.Month()-day0.Month())
		if months%r.Interval != 0 {
			return false
		}
		if len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 {
			return d.Day() == day0.Day()
		}
		return r.byDayOK(d) && r.byMonthDayOK(d)
	case FreqYearly:
		if (d.Year()-day0.Year())%r.Interval != 0 {
			return false
		}
		if len(r.ByMonth) == 0 && len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 {
			return d.Month() == day0.Month() && d.Day() == day0.Day()
		}
		// BYMONTH alone keeps DTSTART's day of the month; BYDAY and
		// BYMONTHDAY expand over the whole year, or the BYMONTH months.
		if len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 {
			return d.Day() == day0.Day()
		}
		return r.byDayOK(d) && r.byMonthDayOK(d)
	}
	return false
}

// byDayOK matches d against BYDAY. A numbered entry counts within the month
// under MONTHLY, or YEARLY with BYMONTH; within the year otherwise.
func (r Recurrence) byDayOK(d time.Time) bool {
	if len(r.ByDay) == 0 {
		return true
	}
	for _, w := range r.ByDay {
		if w.Day != d.Weekday() {
			continue
		}
		if w.N == 0 {
			return true
		}
		var nth, fromEnd int
		if r.Freq == FreqMonthly || len(r.ByMonth) > 0 {
			last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, d.Location()).Day()
			nth, fromEnd = (d.Day()-1)/7+1, (last-d.Day())/7+1
		} else {
			days := time.Date(d.Year(), 12, 31, 0, 0, 0, 0, d.Location()).YearDay()
			nth, fromEnd = (d.YearDay()-1)/7+1, (days-d.YearDay())/7+1
		}
		if w.N == nth || w.N == -fromEnd {
			return true
		}
	}
	return false
}

func (r Recurrence) byMonthDayOK(d time.Time) bool {
	if len(r.ByMonthDay) == 0 {
		return true
	}
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, d.Location()).Day()
	for _, md := range r.ByMonthDay {
		if md > 0 && d.Day() == md {
			return true
		}
		if md < 0 && d.Day() == last+md+1 {
			return true
		}
	}
	return false
}

// daysBetween counts calendar days from a to b, both local midnights. Computed
// on the date, not the duration, so a 23- or 25-hour DST day still counts one.
func daysBetween(a, b time.Time) int {
	ua := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	ub := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

// weekStart returns the wkst on or before d (RFC 5545's default is Monday).
func weekStart(d time.Time, wkst time.Weekday) time.Time {
	offset := (int(d.Weekday()) - int(wkst) + 7) % 7
	return d.AddDate(0, 0, -offset)
}
