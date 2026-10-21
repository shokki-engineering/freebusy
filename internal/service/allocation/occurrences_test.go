package allocation

import (
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"google.golang.org/genproto/googleapis/type/datetime"
)

func daily(start, end time.Time) *eventv1.Event {
	e := busy(start, end)
	e.Recurrence = &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_DAILY}
	return e
}

func TestExcludedInstanceIsFree(t *testing.T) {
	e := daily(hr(18), hr(19))
	e.ExcludedTimes = []*eventv1.CalendarTime{eventTime(hr(24 + 18))}
	got := expandEvents([]*eventv1.Event{e}, time.UTC, engine.Span{Start: hr(0), End: hr(72)})
	if len(got.Clip(engine.Span{Start: hr(24 + 18), End: hr(24 + 19)})) > 0 {
		t.Fatalf("EXDATE instance still blocks: %v", got)
	}
	for _, d := range []float64{0, 48} {
		if !got.Covers(engine.Span{Start: hr(d + 18), End: hr(d + 19)}) {
			t.Fatalf("instance at day %v missing: %v", d/24, got)
		}
	}
}

func TestAdditionalInstanceBlocks(t *testing.T) {
	e := daily(hr(18), hr(19))
	e.Recurrence.Bound = &eventv1.Recurrence_Count{Count: 1}
	e.AdditionalTimes = []*eventv1.CalendarTime{eventTime(hr(48 + 9))}
	got := expandEvents([]*eventv1.Event{e}, time.UTC, engine.Span{Start: hr(0), End: hr(72)})
	if !got.Covers(engine.Span{Start: hr(48 + 9), End: hr(48 + 10)}) {
		t.Fatalf("RDATE instance does not block: %v", got)
	}
}

func TestFloatingEventReadInCalendarZone(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, kolkata)
	e := busy(start, start.Add(time.Hour))
	e.GetStart().GetDateTime().TimeOffset = nil
	e.GetEnd().GetDateTime().TimeOffset = nil
	day := engine.Span{Start: start.Add(-12 * time.Hour), End: start.Add(12 * time.Hour)}
	got := expandEvents([]*eventv1.Event{e}, kolkata, day)
	if want := (engine.Span{Start: start, End: start.Add(time.Hour)}); !got.Covers(want) || len(got) != 1 {
		t.Fatalf("floating 10:00 on a Kolkata calendar: %v, want %v", got, want)
	}
}

func TestZonedEventKeepsItsZoneAcrossDST(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no tzdata")
	}
	// Weekly 10:00 Madrid, on a calendar kept in UTC, across 28 March 2027.
	start := time.Date(2027, 3, 22, 10, 0, 0, 0, madrid)
	e := busy(start, start.Add(time.Hour))
	e.Recurrence = &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_WEEKLY}
	got := expandEvents([]*eventv1.Event{e}, time.UTC, engine.Span{Start: start, End: start.AddDate(0, 0, 14)})
	for _, s := range []time.Time{start, start.AddDate(0, 0, 7)} {
		if !got.Covers(engine.Span{Start: s, End: s.Add(time.Hour)}) {
			t.Fatalf("instance at %v missing: %v", s, got)
		}
	}
}

func TestFloatingAvailabilityReadInCalendarZone(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	open := time.Date(2026, 11, 2, 9, 0, 0, 0, kolkata)
	a := opens(open, open.Add(8*time.Hour), "FREQ=DAILY")
	for _, ct := range []*datetime.DateTime{a.GetAvailablePeriods()[0].GetStart().GetDateTime(), a.GetAvailablePeriods()[0].GetEnd().GetDateTime()} {
		ct.TimeOffset = nil
	}
	day := engine.Span{Start: open.Add(-9 * time.Hour), End: open.Add(15 * time.Hour)}
	got := engine.Offered(toAvailabilities([]*availabilityv1.Availability{a}, kolkata), day)
	if want := (engine.Span{Start: open, End: open.Add(8 * time.Hour)}); !got.Covers(want) || got.Covers(engine.Span{Start: open.Add(-time.Hour), End: open}) {
		t.Fatalf("floating 09:00-17:00 on a Kolkata calendar offered %v, want %v", got, want)
	}
}

func TestOverridesReplaceTheirInstances(t *testing.T) {
	series := daily(hr(18), hr(19))
	series.IcalUid = "standup"
	// Day 1's instance moved to 09:00 the next morning; day 2's cancelled.
	moved := busy(hr(48+9), hr(48+10))
	moved.IcalUid, moved.OriginalStart = "standup", eventTime(hr(24+18))
	cancelled := busy(hr(48+18), hr(48+19))
	cancelled.IcalUid, cancelled.OriginalStart = "standup", eventTime(hr(48+18))
	cancelled.Confirmation = eventv1.Confirmation_CONFIRMATION_CANCELLED

	got := expandEvents([]*eventv1.Event{series, moved, cancelled}, time.UTC, engine.Span{Start: hr(0), End: hr(96)})
	for _, free := range []engine.Span{{Start: hr(24 + 18), End: hr(24 + 19)}, {Start: hr(48 + 18), End: hr(48 + 19)}} {
		if len(got.Clip(free)) > 0 {
			t.Fatalf("overridden instance %v still blocks: %v", free, got)
		}
	}
	for _, b := range []engine.Span{{Start: hr(18), End: hr(19)}, {Start: hr(48 + 9), End: hr(48 + 10)}, {Start: hr(72 + 18), End: hr(72 + 19)}} {
		if !got.Covers(b) {
			t.Fatalf("%v should be busy: %v", b, got)
		}
	}
}

func TestOverrideOfAnotherSeriesIsIgnored(t *testing.T) {
	series := daily(hr(18), hr(19))
	series.IcalUid = "standup"
	other := busy(hr(48+9), hr(48+10))
	other.IcalUid, other.OriginalStart = "retro", eventTime(hr(24+18))
	got := expandEvents([]*eventv1.Event{series, other}, time.UTC, engine.Span{Start: hr(0), End: hr(72)})
	if !got.Covers(engine.Span{Start: hr(24 + 18), End: hr(24 + 19)}) {
		t.Fatalf("an override with another UID freed this series' instance: %v", got)
	}
}
