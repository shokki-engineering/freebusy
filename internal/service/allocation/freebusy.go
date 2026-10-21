package allocation

import (
	"context"
	"time"

	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
)

// FreeBusy is one calendar's time over a window, split the way RFC 5545
// FBTYPE splits it. The three sets are disjoint and clipped to the window;
// whatever none of them covers is free.
type FreeBusy struct {
	// Busy is taken for good: opaque events on the calendar, and converted
	// holds on any resource scheduled on it.
	Busy engine.Spans
	// Tentative is claimed by live holds that have not been converted yet. It
	// becomes Busy or free again when the hold settles.
	Tentative engine.Spans
	// Unavailable is time the calendar's RFC 7953 components do not offer. A
	// calendar with no VAVAILABILITY has none: RFC 7953 section 4 reads a
	// calendar without one as available throughout.
	Unavailable engine.Spans
}

// FreeBusy answers RFC 4791 free-busy-query for one calendar, from the same
// inputs the engine allocates from, so a period QueryFreeBusy reports free is
// one a hold could take. zoneName is the calendar's IANA zone, in which
// recurring availability keeps its wall-clock time.
func (s *Server) FreeBusy(ctx context.Context, calendar, zoneName string, window engine.Span) (FreeBusy, error) {
	now := s.now()
	loc := zone(zoneName)
	cals := []string{calendar}
	avs, err := s.store.Availability(ctx, cals)
	if err != nil {
		return FreeBusy{}, err
	}
	events, err := s.store.Events(ctx, cals, window)
	if err != nil {
		return FreeBusy{}, err
	}
	recs, err := s.store.Resources(ctx, ResourceQuery{Calendars: cals})
	if err != nil {
		return FreeBusy{}, err
	}
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Resource.GetName())
	}
	claims := Claims{}
	if len(names) > 0 {
		if claims, err = s.store.Claims(ctx, names, window, now); err != nil {
			return FreeBusy{}, err
		}
	}

	busy := expandEvents(events[calendar], loc, window)
	var held engine.Spans
	for _, name := range names {
		busy = busy.Union(claims.Converted[name])
		held = held.Union(claims.Held[name])
	}
	busy = busy.Clip(window)
	out := FreeBusy{Busy: busy, Tentative: held.Clip(window).Subtract(busy)}
	if comps := avs[calendar]; len(comps) > 0 {
		offered := engine.Offered(toAvailabilities(comps, loc), window)
		out.Unavailable = engine.Spans{window}.Subtract(offered).Subtract(busy).Subtract(out.Tentative)
	}
	return out, nil
}

// MaxFreeBusyWindow bounds one QueryFreeBusy or ExpandEvent window. Expansion
// costs grow with the window, and a year and a bit covers every planning
// horizon a booking engine is asked about.
const MaxFreeBusyWindow = 400 * 24 * time.Hour
