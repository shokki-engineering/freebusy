package engine

import (
	"sort"
	"time"
)

// Availability is one RFC 7953 VAVAILABILITY component, reduced to what the
// engine needs to decide whether time was on offer.
//
// The polarity is RFC 7953's, not the calendar's: inside Range everything is
// unavailable by default, and each Period carves out time that is available.
// Outside Range the component says nothing at all.
type Availability struct {
	// Range is DTSTART..DTEND. A zero Start or End is open-ended on that side,
	// which RFC 7953 section 3.1 permits.
	Range Span

	// Priority is PRIORITY, 1 highest to 9 lowest, 0 undefined. Where two
	// components' ranges overlap the higher priority alone decides, RFC 7953
	// section 4; undefined ranks below 9.
	Priority int

	// Periods are the AVAILABLE sub-components.
	Periods []Period
}

// Period is one RFC 7953 AVAILABLE sub-component: a span of offered time,
// optionally recurring.
type Period struct {
	// Start is DTSTART, in the location its wall-clock time is meant in. For a
	// recurring period this location is what keeps "09:00" at 09:00 across DST.
	Start time.Time

	// End is DTEND, already resolved from DURATION when that was the form used.
	End time.Time

	// Rule is the RRULE, nil for a single occurrence.
	Rule *Recurrence
}

// rank orders priorities so that larger is stronger: 1 becomes 9, 9 becomes 1,
// and undefined (0) sinks below them all.
func rank(p int) int {
	if p < 1 || p > 9 {
		return 0
	}
	return 10 - p
}

// Offered returns the time inside window that a resource's availability
// components put on offer.
//
// RFC 7953 section 4's composition, applied band by band from the highest
// priority down: each band claims the time its ranges cover and has not been
// claimed already, and inside that time only its periods are offered. Time no
// component covers is offered -- a resource that publishes no availability at
// all is bookable whenever it is not taken, which is the right default for a
// meeting room or a parking bay and the reason this engine works before anyone
// has written a VAVAILABILITY.
//
// Components of equal priority whose ranges overlap are unioned. RFC 7953 leaves
// that case to the implementation, and union is the reading that never hides
// time an operator deliberately published.
func Offered(avs []Availability, window Span) Spans {
	if window.Empty() {
		return nil
	}
	bands := map[int][]Availability{}
	for _, a := range avs {
		bands[rank(a.Priority)] = append(bands[rank(a.Priority)], a)
	}
	ranks := make([]int, 0, len(bands))
	for r := range bands {
		ranks = append(ranks, r)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ranks)))

	unclaimed := Spans{window}
	var offered Spans
	for _, r := range ranks {
		var claimed, open Spans
		for _, a := range bands[r] {
			rng := openEnded(a.Range, window)
			if rng.Empty() {
				continue
			}
			claimed = claimed.Union(Spans{rng})
			open = open.Union(a.periodsIn(rng))
		}
		claimed = claimed.Intersect(unclaimed)
		offered = offered.Union(open.Intersect(claimed))
		unclaimed = unclaimed.Subtract(claimed)
	}
	return offered.Union(unclaimed)
}

// periodsIn expands the component's periods inside rng. A period whose rule the
// engine could not parse was dropped by the caller; here every period is
// trusted to be well-formed.
func (a Availability) periodsIn(rng Span) Spans {
	var out []Span
	for _, p := range a.Periods {
		dur := p.End.Sub(p.Start)
		if dur <= 0 {
			continue
		}
		if p.Rule == nil {
			if s := (Span{Start: p.Start, End: p.End}).Intersect(rng); !s.Empty() {
				out = append(out, s)
			}
			continue
		}
		for _, occ := range p.Rule.Expand(p.Start, dur, rng) {
			if s := occ.Intersect(rng); !s.Empty() {
				out = append(out, s)
			}
		}
	}
	return Normalize(out)
}

// openEnded resolves a range's zero ends against the window, then clips to it.
func openEnded(r, window Span) Span {
	if r.Start.IsZero() {
		r.Start = window.Start
	}
	if r.End.IsZero() {
		r.End = window.End
	}
	return r.Intersect(window)
}
