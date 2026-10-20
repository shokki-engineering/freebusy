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
// BYDAY without ordinals, BYMONTHDAY (including negative days), BYMONTH, and
// WKST, which is accepted and ignored since it only affects weekly rules with
// INTERVAL > 1 and BYDAY spanning a week boundary. Anything else fails with
// ErrUnsupportedRule.
type Recurrence struct {
	Freq     Freq
	Interval int
	// Count bounds the series by occurrence count; zero means unbounded.
	Count int
	// Until bounds the series by the last start instant, inclusive; zero means
	// unbounded.
	Until      time.Time
	ByDay      []time.Weekday
	ByMonthDay []int
	ByMonth    []time.Month
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
	r := Recurrence{Interval: 1}
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
				wd, ok := weekdayCodes[code]
				if !ok {
					// An ordinal ("2MO", "-1FR") lands here too: "the second
					// Monday" is real RFC 5545 but no availability pattern seen
					// so far needs it.
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
		case "WKST":
		default:
			return r, fmt.Errorf("%w: %s", ErrUnsupportedRule, key)
		}
	}
	if r.Freq == 0 {
		return r, fmt.Errorf("%w: no FREQ", ErrUnsupportedRule)
	}
	return r, nil
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
	if dur <= 0 || window.Empty() {
		return nil
	}
	loc := dtstart.Location()
	hh, mm, ss := dtstart.Clock()
	ns := dtstart.Nanosecond()
	day0 := time.Date(dtstart.Year(), dtstart.Month(), dtstart.Day(), 0, 0, 0, 0, loc)

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
		return Normalize(out)
	}
	first := 1
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
		start := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, ns, loc)
		// Past the window with no COUNT to honour: nothing later can matter.
		if r.Count == 0 && !start.Before(window.End) {
			break
		}
		if !r.Until.IsZero() && start.After(r.Until) {
			break
		}
		if !r.matches(day0, d) {
			continue
		}
		if !emit(start) {
			break
		}
	}
	return Normalize(out)
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
		weeks := daysBetween(weekStart(day0), weekStart(d)) / 7
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
		if len(r.ByMonth) == 0 && d.Month() != day0.Month() {
			return false
		}
		if len(r.ByMonthDay) == 0 && len(r.ByDay) == 0 {
			return d.Day() == day0.Day()
		}
		return r.byDayOK(d) && r.byMonthDayOK(d)
	}
	return false
}

func (r Recurrence) byDayOK(d time.Time) bool {
	return len(r.ByDay) == 0 || slices.Contains(r.ByDay, d.Weekday())
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

// weekStart returns the Monday on or before d, RFC 5545's default WKST.
func weekStart(d time.Time) time.Time {
	offset := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -offset)
}
