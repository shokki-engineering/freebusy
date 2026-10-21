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
	"google.golang.org/protobuf/proto"
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

// objectComponent renders one stored object.
func objectComponent(o object) (string, error) {
	if o.event != nil {
		return eventComponent(o.event)
	}
	return availabilityComponent(o.avail)
}

// instanceComponents renders the expanded instances of master as VEVENTs,
// each carrying the master's UID and a RECURRENCE-ID naming the occurrence it
// is, RFC 4791 section 9.6.5. The instances come from the engine's expander,
// so they are exactly the time the engine blocks.
func instanceComponents(master *eventv1.Event, instances []*eventv1.Event, loc *time.Location) ([]string, error) {
	var out []string
	for _, inst := range instances {
		c := proto.Clone(inst).(*eventv1.Event)
		c.IcalUid, c.Uid = master.GetIcalUid(), master.GetUid()
		if c.IcalUid == "" && c.Uid == "" {
			c.IcalUid = master.GetName()
		}
		block, err := eventComponent(c)
		if err != nil {
			return nil, err
		}
		if master.GetRecurrence() != nil {
			sp, err := allocation.EventSpan(inst, loc)
			if err != nil {
				return nil, err
			}
			block = insertAfter(block, "BEGIN:VEVENT"+crlf, "RECURRENCE-ID:"+sp.Start.UTC().Format(utcStamp)+crlf)
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

// decoded is a PUT body read into the message it holds.
type decoded struct {
	event *eventv1.Event
	avail *availabilityv1.Availability
}

// decode reads a calendar object resource, RFC 4791 section 4.1: one
// VCALENDAR holding one VEVENT or one VAVAILABILITY. Overridden instances
// (more VEVENTs sharing the UID, each with a RECURRENCE-ID) are refused
// rather than read: the schema has no place for them, and dropping them
// would silently lose the client's edit of one occurrence.
func decode(text string) (decoded, error) {
	switch {
	case strings.Contains(text, "BEGIN:VAVAILABILITY"):
		a, err := rfc.ICalendar(text).Validate().Availability()
		if err != nil {
			return decoded{}, failf(403, "C:valid-calendar-data", "%v", err)
		}
		return decoded{avail: a}, nil
	case strings.Count(text, "BEGIN:VEVENT") == 1 && !strings.Contains(text, "RECURRENCE-ID"):
		e, err := rfc.ICalendar(text).Validate().Event()
		if err != nil {
			return decoded{}, failf(403, "C:valid-calendar-data", "%v", err)
		}
		return decoded{event: e}, nil
	case strings.Contains(text, "BEGIN:VEVENT"):
		return decoded{}, failf(403, "C:valid-calendar-object-resource",
			"overridden instances (RECURRENCE-ID) are not supported; edit the whole series")
	}
	return decoded{}, failf(403, "C:supported-calendar-component",
		"a calendar object here holds one VEVENT or one VAVAILABILITY")
}
