package allocation

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/types"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/dayofweek"
)

// The stored RFC objects reach the engine as protos, read through the
// generated repositories; this file turns them into the engine's inputs.

// calendarTime is the shape both RFC packages give a DATE or DATE-TIME.
type calendarTime interface {
	GetDate() *date.Date
	GetDateTime() *datetime.DateTime
}

// Instant reads a stored DATE or DATE-TIME. A DATE comes back as midnight UTC
// with allDay set, which the service re-reads as local midnight in the
// calendar's zone; a DATE-TIME as the instant it names, with a floating one
// read in loc. ok is false for an absent time.
func Instant(t calendarTime, loc *time.Location) (at time.Time, allDay, ok bool) {
	if d := t.GetDate(); d != nil {
		return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC), true, true
	}
	dt := t.GetDateTime()
	if dt == nil {
		return time.Time{}, false, false
	}
	in := loc
	switch {
	case dt.GetTimeZone() != nil:
		in = zone(dt.GetTimeZone().GetId())
	case dt.GetUtcOffset() != nil:
		in = time.FixedZone("", int(dt.GetUtcOffset().AsDuration()/time.Second))
	}
	return time.Date(int(dt.GetYear()), time.Month(dt.GetMonth()), int(dt.GetDay()),
		int(dt.GetHours()), int(dt.GetMinutes()), int(dt.GetSeconds()), int(dt.GetNanos()), in), false, true
}

// RecurrenceFromProto maps a stored RFC 5545 recurrence onto the engine's rule
// parts, its UNTIL read in loc. unsupported names the parts the engine does
// not expand; the parts then carry Unsupported, so RRule renders a rule the
// engine refuses rather than one it would misread.
func RecurrenceFromProto(r *eventv1.Recurrence, loc *time.Location) (p RecurrenceParts, unsupported []string) {
	note := func(cond bool, part string) {
		if cond {
			unsupported = append(unsupported, part)
		}
	}
	note(len(r.GetSecondNumbers()) > 0, "BYSECOND")
	note(len(r.GetMinuteNumbers()) > 0, "BYMINUTE")
	note(len(r.GetHourNumbers()) > 0, "BYHOUR")
	note(len(r.GetYearDays()) > 0, "BYYEARDAY")
	note(len(r.GetWeekNumbers()) > 0, "BYWEEKNO")
	note(len(r.GetMonths()) > 0, "BYMONTH")
	note(len(r.GetSetPositions()) > 0, "BYSETPOS")
	note(r.GetRscale() != "" || len(r.GetLeapMonths()) > 0, "RSCALE")
	note(r.GetWeekStart() != dayofweek.DayOfWeek_DAY_OF_WEEK_UNSPECIFIED && r.GetWeekStart() != dayofweek.DayOfWeek_MONDAY, "WKST")

	p.Frequency = strings.TrimPrefix(r.GetFrequency().String(), "FREQUENCY_")
	p.Unsupported = len(unsupported) > 0
	if v := r.GetInterval(); v > 0 {
		p.Interval = &v
	}
	switch b := r.GetBound().(type) {
	case *eventv1.Recurrence_Count:
		v := b.Count
		p.Count = &v
	case *eventv1.Recurrence_Until:
		if until, allDay, ok := Instant(b.Until, loc); ok {
			if allDay {
				// UNTIL as a DATE includes that whole day.
				until = until.AddDate(0, 0, 1).Add(-time.Nanosecond)
			}
			p.Until = &until
		}
	}
	for _, d := range r.GetWeekdays() {
		w := WeekdayPart{Day: d.GetDay().String()}
		if o := d.GetOrdinal(); o != 0 {
			w.Ordinal = &o
		}
		p.ByDay = append(p.ByDay, w)
	}
	p.MonthDays = r.GetMonthDays()
	return p, unsupported
}

// instantIn reads a stored DATE or DATE-TIME as the instant it means on a
// calendar whose zone is loc: a DATE is local midnight there and a floating
// DATE-TIME its wall clock there, while a zoned or offset DATE-TIME keeps its
// own zone, so a rule expanded from it keeps that zone's wall clock across DST.
func instantIn(t calendarTime, loc *time.Location) (at time.Time, allDay, ok bool) {
	at, allDay, ok = Instant(t, loc)
	if allDay {
		at = time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
	}
	return at, allDay, ok
}

// EventSpan is the time one occurrence of e covers on a calendar in loc:
// DTSTART to DTEND, or DTSTART plus DURATION, or the whole day for a DATE with
// neither, RFC 5545 section 3.6.1.
func EventSpan(e *eventv1.Event, loc *time.Location) (engine.Span, error) {
	start, allDay, ok := instantIn(e.GetStart(), loc)
	if !ok {
		return engine.Span{}, fmt.Errorf("%w: %s has no start", types.ErrInvalidState, e.GetName())
	}
	end := start
	switch {
	case e.GetDuration() != nil:
		end = start.Add(e.GetDuration().AsDuration())
	case e.GetEnd() != nil:
		if end, _, ok = instantIn(e.GetEnd(), loc); !ok {
			return engine.Span{}, fmt.Errorf("%w: %s has an empty end", types.ErrInvalidState, e.GetName())
		}
	case allDay:
		end = start.Add(24 * time.Hour)
	}
	return engine.Span{Start: start, End: end}, nil
}

// Occurrences returns the start of every occurrence of e overlapping window,
// and how long each lasts: the RRULE's instances less EXDATE, plus RDATE, RFC
// 5545 section 3.8.5; a single event is its one occurrence. A zero-length
// occurrence overlaps a window containing its instant, RFC 4791 section 9.9.
//
// The engine's busy time, ExpandEvent and CalDAV all read events through it,
// so an instance a client is shown is exactly the time the engine blocks.
func Occurrences(e *eventv1.Event, loc *time.Location, window engine.Span) ([]time.Time, time.Duration, error) {
	first, err := EventSpan(e, loc)
	if err != nil {
		return nil, 0, err
	}
	dur := first.End.Sub(first.Start)
	if dur < 0 {
		return nil, 0, fmt.Errorf("%w: %s ends before it starts", types.ErrInvalidState, e.GetName())
	}
	// The expander skips empty occurrences; probe with a nanosecond so a
	// zero-length one is still reported.
	probe := max(dur, time.Nanosecond)
	occurs := func(t time.Time) bool { return engine.Span{Start: t, End: t.Add(probe)}.Overlaps(window) }
	if e.GetRecurrence() == nil {
		if occurs(first.Start) {
			return []time.Time{first.Start}, dur, nil
		}
		return nil, dur, nil
	}

	parts, unsupported := RecurrenceFromProto(e.GetRecurrence(), loc)
	if len(unsupported) > 0 {
		return nil, dur, fmt.Errorf("%w: rule parts %s are not expanded", types.ErrUnimplemented, strings.Join(unsupported, ", "))
	}
	rule, err := engine.ParseRRule(parts.RRule(), first.Start.Location())
	if err != nil {
		return nil, dur, fmt.Errorf("%w: recurrence of %s: %v", types.ErrUnimplemented, e.GetName(), err)
	}
	excluded := map[int64]bool{}
	for _, x := range e.GetExcludedTimes() {
		t, _, ok := instantIn(x, loc)
		if !ok {
			return nil, dur, fmt.Errorf("%w: %s has an empty EXDATE", types.ErrInvalidState, e.GetName())
		}
		excluded[t.UnixNano()] = true
	}
	var starts []time.Time
	seen := map[int64]bool{}
	keep := func(t time.Time) {
		if k := t.UnixNano(); !excluded[k] && !seen[k] {
			seen[k] = true
			starts = append(starts, t)
		}
	}
	for _, occ := range rule.Occurrences(first.Start, probe, window) {
		keep(occ.Start)
	}
	for _, x := range e.GetAdditionalTimes() {
		t, _, ok := instantIn(x, loc)
		if !ok {
			return nil, dur, fmt.Errorf("%w: %s has an empty RDATE", types.ErrInvalidState, e.GetName())
		}
		if occurs(t) {
			keep(t)
		}
	}
	slices.SortFunc(starts, time.Time.Compare)
	return starts, dur, nil
}
