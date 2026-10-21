package rfcsvc

import (
	"fmt"
	"strings"
	"time"

	eventpbv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/types"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/genproto/googleapis/type/dayofweek"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// expand materialises e's instances overlapping window. It uses the engine's
// RRULE expander, so an instance listed here is exactly the busy time the
// engine blocks for it; EXDATE removes instances and RDATE adds them, RFC 5545
// section 3.8.5.
//
// Instances keep the event's own time form -- a DATE stays a DATE, a zoned
// DATE-TIME keeps its zone -- and carry no name: none is a stored resource.
func expand(e *eventpbv1.Event, loc *time.Location, window engine.Span) ([]*eventpbv1.Event, error) {
	rec := e.GetRecurrence()
	if rec == nil {
		return nil, fmt.Errorf("%w: %s has no recurrence rule", types.ErrInvalidState, e.GetName())
	}
	start, startForm, err := readTime(e.GetStart(), loc)
	if err != nil {
		return nil, err
	}
	var endForm timeForm
	var dur time.Duration
	switch {
	case e.GetDuration() != nil:
		dur = e.GetDuration().AsDuration()
	case e.GetEnd() != nil:
		var end time.Time
		if end, endForm, err = readTime(e.GetEnd(), loc); err != nil {
			return nil, err
		}
		dur = end.Sub(start)
	case startForm.date:
		dur = 24 * time.Hour // RFC 5545 3.6.1: a DATE event with no end lasts the day
	}
	if dur < 0 {
		return nil, fmt.Errorf("%w: %s ends before it starts", types.ErrInvalidState, e.GetName())
	}

	parts, err := recurrenceParts(rec, loc)
	if err != nil {
		return nil, err
	}
	rule, err := engine.ParseRRule(parts.RRule(), start.Location())
	if err != nil {
		return nil, fmt.Errorf("%w: recurrence of %s: %v", types.ErrUnimplemented, e.GetName(), err)
	}

	// A zero-length event still occurs; probe with a nanosecond so the
	// expander, which skips empty occurrences, reports it.
	probe := max(dur, time.Nanosecond)
	excluded := map[int64]bool{}
	for _, x := range e.GetExcludedTimes() {
		t, _, err := readTime(x, loc)
		if err != nil {
			return nil, err
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
	for _, occ := range rule.Occurrences(start, probe, window) {
		keep(occ.Start)
	}
	for _, x := range e.GetAdditionalTimes() {
		t, _, err := readTime(x, loc)
		if err != nil {
			return nil, err
		}
		if (engine.Span{Start: t, End: t.Add(probe)}).Overlaps(window) {
			keep(t)
		}
	}
	sortBy(starts, time.Time.Before)

	out := make([]*eventpbv1.Event, 0, len(starts))
	for _, t := range starts {
		inst := proto.Clone(e).(*eventpbv1.Event)
		inst.Name, inst.Etag = "", ""
		inst.Recurrence, inst.ExcludedTimes, inst.AdditionalTimes = nil, nil, nil
		inst.Start = startForm.render(t)
		if e.GetEnd() != nil {
			inst.Ends = &eventpbv1.Event_End{End: endForm.render(t.Add(dur))}
		}
		out = append(out, inst)
	}
	return out, nil
}

// timeForm is how a stored RFC 5545 time was written, so an instance can be
// written the same way.
type timeForm struct {
	date bool
	loc  *time.Location
	// offset is set for a DATE-TIME given as a fixed UTC offset; zone for
	// one given in an IANA zone. Neither: a floating time, read in the
	// calendar's zone.
	offset *durationpb.Duration
	zone   *datetime.TimeZone
}

// readTime reads a stored time as an instant. A DATE is local midnight in the
// calendar's zone loc, and so is a floating DATE-TIME's wall clock.
func readTime(ct *eventpbv1.CalendarTime, loc *time.Location) (time.Time, timeForm, error) {
	if d := ct.GetDate(); d != nil {
		return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, loc),
			timeForm{date: true, loc: loc}, nil
	}
	dt := ct.GetDateTime()
	if dt == nil {
		return time.Time{}, timeForm{}, fmt.Errorf("%w: a calendar time with neither date nor date_time", types.ErrInvalidState)
	}
	f := timeForm{loc: loc}
	switch {
	case dt.GetTimeZone() != nil:
		f.zone = dt.GetTimeZone()
		f.loc = zoneOf(f.zone.GetId())
	case dt.GetUtcOffset() != nil:
		f.offset = dt.GetUtcOffset()
		f.loc = time.FixedZone("", int(f.offset.AsDuration()/time.Second))
	}
	return time.Date(int(dt.GetYear()), time.Month(dt.GetMonth()), int(dt.GetDay()),
		int(dt.GetHours()), int(dt.GetMinutes()), int(dt.GetSeconds()), int(dt.GetNanos()), f.loc), f, nil
}

// render writes instant t in form f.
func (f timeForm) render(t time.Time) *eventpbv1.CalendarTime {
	l := t.In(f.loc)
	if f.date {
		return &eventpbv1.CalendarTime{Value: &eventpbv1.CalendarTime_Date{Date: &date.Date{
			Year: int32(l.Year()), Month: int32(l.Month()), Day: int32(l.Day()),
		}}}
	}
	dt := &datetime.DateTime{
		Year: int32(l.Year()), Month: int32(l.Month()), Day: int32(l.Day()),
		Hours: int32(l.Hour()), Minutes: int32(l.Minute()), Seconds: int32(l.Second()), Nanos: int32(l.Nanosecond()),
	}
	switch {
	case f.zone != nil:
		dt.TimeOffset = &datetime.DateTime_TimeZone{TimeZone: f.zone}
	case f.offset != nil:
		dt.TimeOffset = &datetime.DateTime_UtcOffset{UtcOffset: f.offset}
	}
	return &eventpbv1.CalendarTime{Value: &eventpbv1.CalendarTime_DateTime{DateTime: dt}}
}

// recurrenceParts maps a stored recurrence onto the engine's rule parts. A
// part the engine cannot expand is refused rather than ignored: dropping a
// BYHOUR would list instances the rule never produces.
func recurrenceParts(r *eventpbv1.Recurrence, loc *time.Location) (allocation.RecurrenceParts, error) {
	var unsupported []string
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
	if len(unsupported) > 0 {
		return allocation.RecurrenceParts{}, fmt.Errorf("%w: rule parts %s are not expanded", types.ErrUnimplemented, strings.Join(unsupported, ", "))
	}
	p := allocation.RecurrenceParts{Frequency: strings.TrimPrefix(r.GetFrequency().String(), "FREQUENCY_")}
	if v := r.GetInterval(); v > 0 {
		p.Interval = &v
	}
	switch b := r.GetBound().(type) {
	case *eventpbv1.Recurrence_Count:
		v := b.Count
		p.Count = &v
	case *eventpbv1.Recurrence_Until:
		until, f, err := readTime(b.Until, loc)
		if err != nil {
			return p, err
		}
		if f.date {
			// UNTIL as a DATE includes that whole day.
			until = until.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
		p.Until = &until
	}
	for _, d := range r.GetWeekdays() {
		w := allocation.WeekdayPart{Day: d.GetDay().String()}
		if o := d.GetOrdinal(); o != 0 {
			w.Ordinal = &o
		}
		p.ByDay = append(p.ByDay, w)
	}
	p.MonthDays = r.GetMonthDays()
	return p, nil
}

// zoneOf loads an IANA zone, falling back to UTC as the engine does: a bad
// zone shifts wall clocks by hours, refusing to answer would be worse.
func zoneOf(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
