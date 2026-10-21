package allocation

import (
	"testing"
	"time"

	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
)

func TestFreeBusySplitsByFBType(t *testing.T) {
	f := newFixture(t)
	f.share()
	all := func(a, b float64) *allocationpbv1.Hold {
		h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
			Parent: club, Hold: &allocationpbv1.Hold{Window: win(a, b), Quantity: 3},
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if _, err := f.srv.ConvertHold(ctx(), all(10, 11).GetName()); err != nil {
		t.Fatal(err)
	}
	all(12, 13) // left active

	fb, err := f.srv.FreeBusy(ctx(), "calendars/club-court-1", "", engine.Span{Start: hr(6), End: hr(23)})
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, got engine.Spans, want ...[2]float64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s %v, want %v", what, got, want)
		}
		for i, w := range want {
			if !got[i].Start.Equal(hr(w[0])) || !got[i].End.Equal(hr(w[1])) {
				t.Fatalf("%s %v, want %v", what, got, want)
			}
		}
	}
	// The converted hold and the calendar event are bookings; the open hold
	// may still lapse; outside 08:00-22:00 the court is not offered at all.
	check("busy", fb.Busy, [2]float64{10, 11}, [2]float64{18, 19})
	check("tentative", fb.Tentative, [2]float64{12, 13})
	check("unavailable", fb.Unavailable, [2]float64{6, 8}, [2]float64{22, 23})
}

func TestFreeBusyWithoutAvailabilityIsOpen(t *testing.T) {
	f := newFixture(t) // court calendars carry no VAVAILABILITY until share()
	fb, err := f.srv.FreeBusy(ctx(), "calendars/club-court-1", "", engine.Span{Start: hr(6), End: hr(23)})
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Unavailable) != 0 || len(fb.Busy) != 1 {
		t.Fatalf("got %+v", fb)
	}
}

func TestStoredDurationReadsBothForms(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT10H": 10 * time.Hour, "P1D": 24 * time.Hour, "10h0m0s": 10 * time.Hour,
		"-1h": 0, "PT": 0, "": 0, "soon": 0,
	} {
		if got := StoredDuration(in); got != want {
			t.Errorf("StoredDuration(%q) = %v, want %v", in, got, want)
		}
	}
}
