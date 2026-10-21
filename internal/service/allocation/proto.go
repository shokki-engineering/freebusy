package allocation

import (
	"strings"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
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

// EventRecord turns a stored event into the busy period the engine reads,
// with its rule rendered for the expander. A DATE event with no end lasts the
// day, RFC 5545 section 3.6.1.
func EventRecord(e *eventv1.Event) PeriodRecord {
	start, allDay, _ := Instant(e.GetStart(), time.UTC)
	end := start
	switch {
	case e.GetDuration() != nil:
		end = start.Add(e.GetDuration().AsDuration())
	case e.GetEnd() != nil:
		end, _, _ = Instant(e.GetEnd(), time.UTC)
	case allDay:
		end = start.Add(24 * time.Hour)
	}
	rec := PeriodRecord{Start: start, End: end, AllDay: allDay}
	if r := e.GetRecurrence(); r != nil {
		parts, _ := RecurrenceFromProto(r, time.UTC)
		rec.RRule = parts.RRule()
	}
	return rec
}

// AvailabilityRecordOf turns a stored RFC 7953 component into the engine's
// record: its range, priority, and AVAILABLE periods.
func AvailabilityRecordOf(a *availabilityv1.Availability) AvailabilityRecord {
	start, startDay, _ := Instant(a.GetStart(), time.UTC)
	var end time.Time
	switch {
	case a.GetDuration() != nil && !start.IsZero():
		end = start.Add(a.GetDuration().AsDuration())
	case a.GetEnd() != nil:
		end, _, _ = Instant(a.GetEnd(), time.UTC)
	}
	rec := AvailabilityRecord{
		Range:       engine.Span{Start: start, End: end},
		RangeAllDay: startDay,
		Priority:    int(a.GetPriority()),
	}
	for _, p := range a.GetAvailablePeriods() {
		ps, allDay, _ := Instant(p.GetStart(), time.UTC)
		pe := ps
		switch {
		case p.GetDuration() != nil:
			pe = ps.Add(p.GetDuration().AsDuration())
		case p.GetEnd() != nil:
			pe, _, _ = Instant(p.GetEnd(), time.UTC)
		}
		rec.Periods = append(rec.Periods, PeriodRecord{Start: ps, End: pe, RRule: p.GetRecurrenceRule(), AllDay: allDay})
	}
	return rec
}
