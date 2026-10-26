// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/shokki-engineering/freebusy/internal/rfc"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
)

// horizonPad widens the span read around a requested window. The engine only
// needs the window to say yes or no, but best-fit allocation measures the free
// gap around it, and a gap clipped at the window's own edges would make every
// candidate look equally tight.
const horizonPad = 24 * time.Hour

// inputs are one request's inputs, gathered and converted.
type inputs struct {
	resources []engine.Resource
	// records are parallel to resources.
	records []ResourceRecord
	// statics are parallel to resources: what each was built from.
	statics []staticResource
	// zone is the first resource's calendar zone, the zone requests default
	// to.
	zone *time.Location
}

// gather reads everything the engine needs about the resources q selects, over
// span, and converts it.
//
// The static inputs -- resources, calendars, availability -- come from the
// per-resource cache when the store is versioned (statics.go), unless fresh
// is set. Bookings are read on every call: events and claims, each batched
// across every resource and read side by side, so cost grows with the rows
// returned, not with round trips.
func (s *Server) gather(ctx context.Context, q ResourceQuery, span engine.Span, now time.Time) (*inputs, error) {
	return s.gatherFrom(ctx, q, span, now, false, nil)
}

// gatherFrom is gather, reading the static inputs from the database when
// fresh is set, and leaving kept -- a replaced booking's own claims, by
// resource -- out of what is booked.
func (s *Server) gatherFrom(ctx context.Context, q ResourceQuery, span engine.Span, now time.Time, fresh bool, kept map[string]engine.Spans) (*inputs, error) {
	statics, err := s.staticInputs(ctx, q, fresh)
	if err != nil {
		return nil, err
	}
	return s.assemble(ctx, statics, span, now, kept)
}

// assemble reads the bookings on statics' resources over span and builds the
// engine's view of them, less kept. Claims of different holds on one resource
// never overlap (the no_overlap constraint), so taking kept out of the
// converted claims removes exactly the replaced booking's.
func (s *Server) assemble(ctx context.Context, statics []staticResource, span engine.Span, now time.Time, kept map[string]engine.Spans) (*inputs, error) {
	out := &inputs{zone: time.UTC, statics: statics}
	if len(statics) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(statics))
	var calendars []string
	seen := map[string]bool{}
	for _, r := range statics {
		names = append(names, r.rec.Resource.GetName())
		if c := r.rec.Resource.GetCalendar(); c != "" && !seen[c] {
			seen[c] = true
			calendars = append(calendars, c)
		}
	}
	var (
		events   map[string][]*eventv1.Event
		claims   Claims
		evErr    error
		claimErr error
		wg       sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		events, evErr = s.store.Events(ctx, calendars, span)
	}()
	claims, claimErr = s.store.Claims(ctx, names, span, now)
	wg.Wait()
	if evErr != nil {
		return nil, evErr
	}
	if claimErr != nil {
		return nil, claimErr
	}

	out.zone = statics[0].loc
	out.resources = make([]engine.Resource, 0, len(statics))
	out.records = make([]ResourceRecord, 0, len(statics))
	// A calendar's events are expanded once, however many resources share it.
	expanded := map[string]engine.Spans{}
	for _, r := range statics {
		cal := r.rec.Resource.GetCalendar()
		name := r.rec.Resource.GetName()
		key := cal + "\x00" + r.loc.String()
		evs, ok := expanded[key]
		if !ok {
			evs = expandEvents(events[cal], r.loc, span)
			expanded[key] = evs
		}
		out.resources = append(out.resources, engine.Resource{
			Name:     name,
			Inactive: r.inactive,
			Offered:  engine.Offered(r.avs, span),
			Held:     claims.Held[name],
			Booked:   evs.Union(claims.Converted[name].Subtract(kept[name])),
			Policy:   r.policy,
		})
		out.records = append(out.records, r.rec)
	}
	return out, nil
}

// zones caches loaded time zones: time.LoadLocation reads the zone database
// on every call.
var zones sync.Map

// zone loads an IANA zone, falling back to UTC. A bad zone name shifts day
// boundaries by hours; refusing to answer would be worse.
func zone(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	zones.Store(name, loc)
	return loc
}

// toAvailabilities converts stored components for a calendar in loc. Each
// time keeps its own zone, a floating time or a DATE taking loc's, so a
// recurring period keeps its wall-clock time across DST.
//
// A period whose rule the engine cannot expand is dropped, which offers less
// time rather than more: a misread opening-hours rule should make a court look
// closed, never make it bookable at 3 a.m.
func toAvailabilities(comps []*availabilityv1.Availability, loc *time.Location) []engine.Availability {
	out := make([]engine.Availability, 0, len(comps))
	for _, c := range comps {
		// An absent DTSTART or DTEND reads as the zero time: open-ended.
		start, _, _ := instantIn(c.GetStart(), loc)
		var end time.Time
		switch {
		case c.GetDuration() != nil && !start.IsZero():
			end = start.Add(c.GetDuration().AsDuration())
		case c.GetEnd() != nil:
			end, _, _ = instantIn(c.GetEnd(), loc)
		}
		a := engine.Availability{Range: engine.Span{Start: start, End: end}, Priority: int(c.GetPriority())}
		for _, p := range c.GetAvailablePeriods() {
			ps, _, ok := instantIn(p.GetStart(), loc)
			if !ok {
				continue
			}
			pe := ps
			switch {
			case p.GetDuration() != nil:
				pe = ps.Add(p.GetDuration().AsDuration())
			case p.GetEnd() != nil:
				pe, _, _ = instantIn(p.GetEnd(), loc)
			}
			period := engine.Period{Start: ps, End: pe}
			if r := p.GetRecurrenceRule(); r != "" {
				rule, err := engine.ParseRRule(r, ps.Location())
				if err != nil {
					continue
				}
				period.Rule = &rule
			}
			a.Periods = append(a.Periods, period)
		}
		out = append(out, a)
	}
	return out
}

// expandEvents turns stored events on a calendar in loc into busy time over
// span, through Occurrences: each series less the instances its overrides
// replace, and each override, which stands for its instance, on its own. A
// cancelled or transparent override frees its instance and blocks nothing.
//
// The opposite fallback to toAvailabilities: an event whose occurrences cannot
// be listed blocks the whole span from its first occurrence on, because
// overselling a resource someone already booked is the failure a booking engine
// exists to prevent.
func expandEvents(events []*eventv1.Event, loc *time.Location, span engine.Span) engine.Spans {
	var out []engine.Span
	for _, e := range events {
		if !Busy(e) {
			continue
		}
		starts, dur, err := Occurrences(e, OverridesOf(e, events), loc, span)
		if err != nil {
			if first, err := EventSpan(e, loc); err == nil {
				out = append(out, engine.Span{Start: first.Start, End: span.End}.Intersect(span))
			}
			continue
		}
		for _, s := range starts {
			out = append(out, engine.Span{Start: s, End: s.Add(dur)}.Intersect(span))
		}
	}
	return engine.Normalize(out)
}

// policyOf reads the booking policy off a resource's STRUCTURED-DATA. A
// missing or unreadable profile imposes no policy: the resource is still
// bookable, just without rules nobody could read.
func policyOf(rec ResourceRecord) engine.Policy {
	p, err := rfc.DecodeProfile(rec.Resource)
	if err != nil {
		return engine.Policy{}
	}
	return engine.Policy{
		MinDuration: icalDuration(p.MinDuration),
		MaxDuration: icalDuration(p.MaxDuration),
		MinNotice:   icalDuration(p.MinNotice),
		MaxAdvance:  icalDuration(p.MaxAdvance),
	}
}

// durationRE matches an RFC 5545 section 3.3.6 DURATION: weeks alone, or days
// and/or a time part. Signs are rejected: a negative minimum stay means nothing.
var durationRE = regexp.MustCompile(`^P(?:(\d+)W|(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?)$`)

// icalDuration parses an RFC 5545 DURATION, zero when empty or malformed.
//
// A day is taken as 24 hours. RFC 5545 says a day is nominal -- 23 or 25
// hours across DST -- but these values are policy bounds ("at least 2 days
// ahead"), where an hour either way is not a difference anyone has priced.
func icalDuration(s string) time.Duration {
	m := durationRE.FindStringSubmatch(s)
	if m == nil || s == "P" || s == "PT" {
		return 0
	}
	n := func(i int) time.Duration {
		v, _ := strconv.Atoi(m[i])
		return time.Duration(v)
	}
	return n(1)*7*24*time.Hour + n(2)*24*time.Hour + n(3)*time.Hour + n(4)*time.Minute + n(5)*time.Second
}

// horizon is the span read around window: padded for best fit, see horizonPad.
func horizon(window engine.Span) engine.Span {
	return engine.Span{Start: window.Start.Add(-horizonPad), End: window.End.Add(horizonPad)}
}
