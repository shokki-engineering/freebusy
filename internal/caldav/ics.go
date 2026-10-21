package caldav

import (
	"fmt"
	"strings"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/the-protobuf-project/runtime-go/rfc"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// iCalendar crosses the wire through runtime-go/rfc. What this file adds is
// what a CalDAV object needs and a stored message may not carry: a UID and a
// DTSTAMP on every component (RFC 5545 section 3.6.1, RFC 7953 section 3.1),
// and the splicing that puts several components in one VCALENDAR -- a
// calendar export, or the instances of an expanded series.

const (
	crlf     = "\r\n"
	utcStamp = "20060102T150405Z"
	prodID   = "-//oh-tarnished//freebusy CalDAV//EN"
)

// MediaType is what every calendar object is served as, RFC 5545 section 8.1.
const MediaType = "text/calendar; charset=utf-8"

// stamp is DTSTAMP for a stored object: when it last changed.
func stamp(ts ...*timestamppb.Timestamp) string {
	for _, t := range ts {
		if t != nil {
			return t.AsTime().UTC().Format(utcStamp)
		}
	}
	return time.Now().UTC().Format(utcStamp)
}

// uidOf is the UID an object is published under: the iCalendar UID it was
// written with, else the server's uid, else its resource name.
func uidOf(icalUID, uid, name string) string {
	switch {
	case icalUID != "":
		return icalUID
	case uid != "":
		return uid
	}
	return name
}

// eventComponent renders e as its VEVENT block, with a UID and DTSTAMP.
func eventComponent(e *eventv1.Event) (string, error) {
	c := proto.Clone(e).(*eventv1.Event)
	c.IcalUid = uidOf(e.GetIcalUid(), e.GetUid(), e.GetName())
	text, err := rfc.Event(c).ICalendar()
	if err != nil {
		return "", err
	}
	block := component(text, "VEVENT")
	if !strings.Contains(block, crlf+"DTSTAMP") {
		block = insertAfter(block, "BEGIN:VEVENT"+crlf, "DTSTAMP:"+stamp(e.GetUpdateTime(), e.GetCreateTime())+crlf)
	}
	return block, nil
}

// availabilityComponent renders a as its VAVAILABILITY block. The codec
// insists on a UID and a DTSTAMP, which it reads from an extension property
// because the schema has no field for it; a record made over the API has
// neither, so both are supplied here.
func availabilityComponent(a *availabilityv1.Availability) (string, error) {
	c := proto.Clone(a).(*availabilityv1.Availability)
	c.IcalUid = uidOf(a.GetIcalUid(), a.GetUid(), a.GetName())
	hasStamp := false
	for _, x := range c.GetExtensions() {
		hasStamp = hasStamp || strings.EqualFold(x.GetKey(), "DTSTAMP")
	}
	if !hasStamp {
		c.Extensions = append(c.Extensions, &availabilityv1.ExtensionProperty{
			Key: "DTSTAMP", Values: []string{stamp(a.GetUpdateTime(), a.GetCreateTime())},
		})
	}
	for i, p := range c.GetAvailablePeriods() {
		if p.GetIcalUid() == "" {
			p.IcalUid = fmt.Sprintf("%s-%d", c.GetIcalUid(), i)
		}
	}
	text, err := rfc.Availability(c).ICalendar()
	if err != nil {
		return "", err
	}
	return component(text, "VAVAILABILITY"), nil
}

// objectComponent renders one stored object: an availability, or an event
// followed by the overrides of its instances, each a VEVENT carrying its
// RECURRENCE-ID, RFC 5545 section 3.8.4.4.
func objectComponent(o object) (string, error) {
	if o.avail != nil {
		return availabilityComponent(o.avail)
	}
	var b strings.Builder
	for _, e := range append([]*eventv1.Event{o.event}, o.overrides...) {
		block, err := eventComponent(e)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}
	return b.String(), nil
}

// instanceComponents renders the expanded instances of master as VEVENTs,
// each carrying the master's UID and a RECURRENCE-ID naming the occurrence it
// is, in UTC, RFC 4791 section 9.6.5. The instances come from the engine's
// expander, so they are exactly the time the engine blocks.
func instanceComponents(master *eventv1.Event, instances []*eventv1.Event, loc *time.Location) ([]string, error) {
	var out []string
	for _, inst := range instances {
		c := proto.Clone(inst).(*eventv1.Event)
		c.IcalUid, c.Uid = master.GetIcalUid(), master.GetUid()
		if c.IcalUid == "" && c.Uid == "" {
			c.IcalUid = master.GetName()
		}
		if at, allDay, ok := allocation.Instant(c.GetOriginalStart(), loc); ok && !allDay {
			at = at.UTC()
			c.OriginalStart = &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: &datetime.DateTime{
				Year: int32(at.Year()), Month: int32(at.Month()), Day: int32(at.Day()),
				Hours: int32(at.Hour()), Minutes: int32(at.Minute()), Seconds: int32(at.Second()),
				TimeOffset: &datetime.DateTime_UtcOffset{UtcOffset: &durationpb.Duration{}},
			}}}
		}
		block, err := eventComponent(c)
		if err != nil {
			return nil, err
		}
		out = append(out, block)
	}
	return out, nil
}

// calendarObject wraps components in one VCALENDAR.
func calendarObject(components ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR" + crlf + "VERSION:2.0" + crlf + "PRODID:" + prodID + crlf)
	for _, c := range components {
		b.WriteString(c)
	}
	b.WriteString("END:VCALENDAR" + crlf)
	return b.String()
}

// freeBusyObject renders the engine's free/busy as one VFREEBUSY, the body
// of a free-busy-query REPORT, RFC 4791 section 7.10. Periods are UTC, as
// section 3.8.2.6 of RFC 5545 requires of FREEBUSY.
func freeBusyObject(fb allocation.FreeBusy, window engine.Span, now time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VFREEBUSY" + crlf)
	b.WriteString("DTSTAMP:" + now.UTC().Format(utcStamp) + crlf)
	b.WriteString("DTSTART:" + window.Start.UTC().Format(utcStamp) + crlf)
	b.WriteString("DTEND:" + window.End.UTC().Format(utcStamp) + crlf)
	for _, set := range []struct {
		kind  string
		spans engine.Spans
	}{{"BUSY", fb.Busy}, {"BUSY-TENTATIVE", fb.Tentative}, {"BUSY-UNAVAILABLE", fb.Unavailable}} {
		for _, s := range set.spans {
			b.WriteString("FREEBUSY;FBTYPE=" + set.kind + ":" + s.Start.UTC().Format(utcStamp) + "/" + s.End.UTC().Format(utcStamp) + crlf)
		}
	}
	b.WriteString("END:VFREEBUSY" + crlf)
	return calendarObject(b.String())
}

// component cuts the named component, BEGIN through END, out of a VCALENDAR.
// The last END is used so a VAVAILABILITY keeps its nested AVAILABLEs.
func component(text, name string) string {
	begin := strings.Index(text, "BEGIN:"+name+crlf)
	end := strings.LastIndex(text, "END:"+name+crlf)
	if begin < 0 || end < begin {
		return ""
	}
	return text[begin : end+len("END:"+name+crlf)]
}

func insertAfter(block, marker, line string) string {
	i := strings.Index(block, marker)
	if i < 0 {
		return block
	}
	i += len(marker)
	return block[:i] + line + block[i:]
}

// decoded is a PUT body read into the messages it holds.
type decoded struct {
	event     *eventv1.Event
	overrides []*eventv1.Event
	avail     *availabilityv1.Availability
}

// decode reads a calendar object resource, RFC 4791 section 4.1: one
// VCALENDAR holding one VAVAILABILITY, or VEVENTs sharing one UID -- a single
// event, or a recurring series with overrides of its instances, each carrying
// a RECURRENCE-ID. An override on its own, for an instance of a series kept
// elsewhere, is one event too.
func decode(text string) (decoded, error) {
	switch {
	case strings.Contains(text, "BEGIN:VAVAILABILITY"):
		a, err := rfc.ICalendar(text).Validate().Availability()
		if err != nil {
			return decoded{}, failf(403, "C:valid-calendar-data", "%v", err)
		}
		return decoded{avail: a}, nil
	case !strings.Contains(text, "BEGIN:VEVENT"):
		return decoded{}, failf(403, "C:supported-calendar-component",
			"a calendar object here holds VEVENTs or one VAVAILABILITY")
	}
	events, err := rfc.ICalendar(text).Validate().Events()
	if err != nil {
		return decoded{}, failf(403, "C:valid-calendar-data", "%v", err)
	}
	invalid := func(format string, args ...any) (decoded, error) {
		return decoded{}, failf(403, "C:valid-calendar-object-resource", format, args...)
	}
	var d decoded
	for _, e := range events {
		switch {
		case e.GetIcalUid() != events[0].GetIcalUid():
			return invalid("every component of one calendar object shares a UID")
		case e.GetOriginalStart() == nil && d.event != nil && d.event.GetOriginalStart() == nil:
			return invalid("a calendar object holds one series; its other VEVENTs need a RECURRENCE-ID")
		case e.GetOriginalStart() == nil:
			if d.event != nil {
				d.overrides = append(d.overrides, d.event)
			}
			d.event = e
		case e.GetRecurrence() != nil:
			return invalid("an overridden instance cannot carry its own RRULE")
		case d.event == nil:
			d.event = e
		default:
			d.overrides = append(d.overrides, e)
		}
	}
	switch {
	case len(d.overrides) > 0 && d.event.GetOriginalStart() != nil:
		return invalid("overridden instances need their series in the same object")
	case len(d.overrides) > 0 && d.event.GetRecurrence() == nil:
		return invalid("only a recurring event has instances to override")
	}
	return d, nil
}
