package rfcsvc

import (
	"fmt"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Instances returns e's occurrences overlapping window: for a recurring
// series, its instances with overrides applied, as expand gives them; for any
// other event, e itself when it overlaps. A CalDAV time-range filter and its
// expand element read the same answer ExpandEvent does.
func Instances(e *eventv1.Event, overrides []*eventv1.Event, loc *time.Location, window engine.Span) ([]*eventv1.Event, error) {
	if e.GetRecurrence() != nil {
		return expand(e, overrides, loc, window)
	}
	starts, _, err := allocation.Occurrences(e, nil, loc, window)
	if err != nil || len(starts) == 0 {
		return nil, err
	}
	return []*eventv1.Event{e}, nil
}

// expand materialises e's instances overlapping window, as
// allocation.Occurrences lists them -- so an instance listed here is exactly
// the busy time the engine blocks for it. Each override replaces the
// instance its original_start names: a moved one is listed at its new time,
// when that overlaps window, and a cancelled one not at all, RFC 5545
// section 3.8.4.4.
//
// Instances keep the event's own time form -- a DATE stays a DATE, a zoned
// DATE-TIME keeps its zone -- carry the original_start that identifies them
// within the series, and carry no name: none is a stored resource. An
// override keeps its own name, since it is one.
func expand(e *eventv1.Event, overrides []*eventv1.Event, loc *time.Location, window engine.Span) ([]*eventv1.Event, error) {
	if e.GetRecurrence() == nil {
		return nil, fmt.Errorf("%w: %s has no recurrence rule", types.ErrInvalidState, e.GetName())
	}
	starts, dur, err := allocation.Occurrences(e, overrides, loc, window)
	if err != nil {
		return nil, err
	}
	_, startForm, err := readTime(e.GetStart(), loc)
	if err != nil {
		return nil, err
	}
	var endForm timeForm
	if e.GetEnd() != nil {
		if _, endForm, err = readTime(e.GetEnd(), loc); err != nil {
			return nil, err
		}
	}
	type instance struct {
		start time.Time
		event *eventv1.Event
	}
	out := make([]instance, 0, len(starts)+len(overrides))
	for _, t := range starts {
		inst := proto.Clone(e).(*eventv1.Event)
		inst.Name, inst.Etag = "", ""
		inst.Recurrence, inst.ExcludedTimes, inst.AdditionalTimes = nil, nil, nil
		inst.Start = startForm.render(t)
		inst.OriginalStart = startForm.render(t)
		if e.GetEnd() != nil {
			inst.Ends = &eventv1.Event_End{End: endForm.render(t.Add(dur))}
		}
		out = append(out, instance{t, inst})
	}
	for _, o := range overrides {
		if o.GetConfirmation() == eventv1.Confirmation_CONFIRMATION_CANCELLED {
			continue
		}
		at, _, err := allocation.Occurrences(o, nil, loc, window)
		if err != nil {
			return nil, err
		}
		if len(at) > 0 {
			out = append(out, instance{at[0], o})
		}
	}
	sortBy(out, func(a, b instance) bool { return a.start.Before(b.start) })
	events := make([]*eventv1.Event, len(out))
	for i, in := range out {
		events[i] = in.event
	}
	return events, nil
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
func readTime(ct *eventv1.CalendarTime, loc *time.Location) (time.Time, timeForm, error) {
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
		f.loc = ZoneOf(f.zone.GetId())
	case dt.GetUtcOffset() != nil:
		f.offset = dt.GetUtcOffset()
		f.loc = time.FixedZone("", int(f.offset.AsDuration()/time.Second))
	}
	return time.Date(int(dt.GetYear()), time.Month(dt.GetMonth()), int(dt.GetDay()),
		int(dt.GetHours()), int(dt.GetMinutes()), int(dt.GetSeconds()), int(dt.GetNanos()), f.loc), f, nil
}

// render writes instant t in form f.
func (f timeForm) render(t time.Time) *eventv1.CalendarTime {
	l := t.In(f.loc)
	if f.date {
		return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_Date{Date: &date.Date{
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
	return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dt}}
}

// ZoneOf loads an IANA zone, falling back to UTC as the engine does: a bad
// zone shifts wall clocks by hours, refusing to answer would be worse.
func ZoneOf(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
