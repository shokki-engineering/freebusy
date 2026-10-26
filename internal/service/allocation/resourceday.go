// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

// The availability read model's writer: allocation.v1.ResourceDay rows, one
// per resource per UTC day of a rolling horizon, each holding the resource's
// standing free time that day (engine.FreeIntervals). The generated
// projection runtime calls it with the source records each write changed and
// stores what it returns in the same transaction (see
// repox.ProjectionWriter); Rebuild and the daily horizon job call it too.
//
// This file imports the generated allocation repository package, which shares
// this package's name; nothing else here does.

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/structpb"
)

// The resource types the read model is projected from, as the generated
// repositories report their writes.
const (
	typeResource     = "the-protobuf-project.org/Resource"
	typeCalendar     = "the-protobuf-project.org/Calendar"
	typeAvailability = "the-protobuf-project.org/Availability"
	typeEvent        = "the-protobuf-project.org/Event"
	typeAllocation   = "freebusy.allocation.v1/Allocation"
)

// DefaultHorizonDays is how far ahead the read model reaches by default.
const DefaultHorizonDays = 365

// oneDay is 24 hours: the read model's days are UTC days, which have no DST.
const oneDay = 24 * time.Hour

// rebuildBatch is how many resources one Rebuild transaction projects.
const rebuildBatch = 25

// DayProjection maintains the read model: the writer the projection runtime
// runs, the rebuild, and the horizon.
type DayProjection struct {
	// open returns a Store reading through a connection: the write's
	// transaction, when the writer runs inside one.
	open    func(repox.Conn) Store
	horizon int
	now     func() time.Time
}

// NewDayProjection returns the read model's maintainer. open builds a Store
// reading through a given connection (db.On); horizonDays <= 0 is
// DefaultHorizonDays.
func NewDayProjection(open func(repox.Conn) Store, horizonDays int) *DayProjection {
	if horizonDays <= 0 {
		horizonDays = DefaultHorizonDays
	}
	return &DayProjection{open: open, horizon: horizonDays, now: time.Now}
}

// Projector is the read model's projector, for repox.NewProjections.
func (p *DayProjection) Projector() repox.Projector {
	return allocation.ResourceDays.Projector(p.Writer())
}

// Writer is the ResourceDayWriter the projection runtime runs for every write
// to a source.
func (p *DayProjection) Writer() allocation.ResourceDayWriter {
	return allocation.ResourceDayWriterFunc(func(ctx context.Context, read repox.Conn, changes []repox.Change) (allocation.ResourceDayRows, error) {
		t := p.targetsOf(changes)
		if t.empty() {
			return allocation.ResourceDayRows{}, nil
		}
		return p.rows(ctx, read, t, changes)
	})
}

// Horizon is the span the read model covers at now: yesterday (UTC) -- a
// window starting just after local midnight east of Greenwich starts there --
// through HorizonDays ahead.
func (p *DayProjection) Horizon(now time.Time) engine.Span {
	today := now.UTC().Truncate(oneDay)
	return engine.Span{Start: today.Add(-oneDay), End: today.Add(time.Duration(p.horizon) * oneDay)}
}

// Covers reports whether window lies inside the horizon at now, so the read
// model can answer for it.
func (p *DayProjection) Covers(window engine.Span, now time.Time) bool {
	return p.Horizon(now).Contains(window)
}

// targets are the resources a write changes and the days it changes them on:
// named resources, and every resource on a calendar.
type targets struct {
	resources map[string]engine.Spans
	calendars map[string]engine.Spans
}

func newTargets() targets {
	return targets{resources: map[string]engine.Spans{}, calendars: map[string]engine.Spans{}}
}

func (t targets) empty() bool {
	return len(t.resources) == 0 && len(t.calendars) == 0
}

func add(m map[string]engine.Spans, key string, s engine.Span) {
	if key == "" || s.Empty() {
		return
	}
	m[key] = m[key].Union(engine.Spans{s})
}

// targetsOf works out what changes touch, clipped to the horizon and widened
// to whole days. A write that changes nothing the read model holds -- a claim
// of a live hold, which lapses by itself -- touches nothing.
func (p *DayProjection) targetsOf(changes []repox.Change) targets {
	h := p.Horizon(p.now())
	t := newTargets()
	clip := func(s engine.Span) engine.Span { return days(s.Intersect(h)) }
	for _, c := range changes {
		switch c.Type {
		case typeResource:
			add(t.resources, c.Name, h)
			if r, ok := c.Prior.(*resourcev1.Resource); ok && r.GetName() != "" {
				add(t.resources, r.GetName(), h)
			}
		case typeCalendar:
			add(t.calendars, c.Name, h)
		case typeAvailability:
			add(t.calendars, parentOf(c.Name), h)
		case typeEvent:
			for _, m := range []any{c.Record, c.Prior} {
				if e, ok := m.(*eventv1.Event); ok && e != nil {
					add(t.calendars, parentOf(c.Name), clip(eventReach(e, h)))
				}
			}
			if c.Record == nil && c.Prior == nil {
				add(t.calendars, parentOf(c.Name), h)
			}
		case typeAllocation:
			before, _ := c.Prior.(*allocationpbv1.Allocation)
			after, _ := c.Record.(*allocationpbv1.Allocation)
			for _, a := range []*allocationpbv1.Allocation{before, after} {
				if a == nil || !kept(c, a == after, a) {
					continue
				}
				res := cmpOr(a.GetResource(), before.GetResource())
				span := claimSpan(a)
				if span.Empty() && before != nil {
					span = claimSpan(before)
				}
				add(t.resources, res, clip(span))
			}
		}
	}
	return t
}

// kept reports whether a, the record before or after an allocation write,
// is a claim the read model subtracts: one with no expiry, kept by a
// converted hold. An update queued in a Batch carries only the fields its
// mask names, which for an allocation is its expiry.
func kept(c repox.Change, isAfter bool, a *allocationpbv1.Allocation) bool {
	if isAfter {
		switch c.Op {
		case repox.ChangeDelete, repox.ChangePurge:
			return false
		}
	}
	return a.GetExpireTime() == nil
}

// cmpOr is the first non-empty string.
func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// claimSpan is the time an allocation claims.
func claimSpan(a *allocationpbv1.Allocation) engine.Span {
	if a.GetStartTime() == nil || a.GetEndTime() == nil {
		return engine.Span{}
	}
	return engine.Span{Start: a.GetStartTime().AsTime(), End: a.GetEndTime().AsTime()}
}

// eventReach is the time an event can block, read in UTC with a day of
// slack each side (a floating or all-day time read in its calendar's zone
// moves by up to a day): its one occurrence, or everything from its first
// for a recurring series, plus the instance an override replaces.
func eventReach(e *eventv1.Event, h engine.Span) engine.Span {
	sp, err := EventSpan(e, time.UTC)
	if err != nil {
		return h
	}
	out := engine.Span{Start: sp.Start.Add(-oneDay), End: sp.End.Add(oneDay)}
	if e.GetRecurrence() != nil {
		out.End = h.End
	}
	if e.GetOriginalStart() != nil {
		if at, _, ok := Instant(e.GetOriginalStart(), time.UTC); ok {
			if at.Add(-oneDay).Before(out.Start) {
				out.Start = at.Add(-oneDay)
			}
			if end := at.Add(2 * oneDay); end.After(out.End) {
				out.End = end
			}
		}
	}
	return out
}

// days widens s to whole UTC days.
func days(s engine.Span) engine.Span {
	if s.Empty() {
		return engine.Span{}
	}
	start := s.Start.UTC().Truncate(oneDay)
	end := s.End.UTC().Truncate(oneDay)
	if end.Before(s.End) {
		end = end.Add(oneDay)
	}
	return engine.Span{Start: start, End: end}
}

// rows computes the ResourceDay rows of t: every day each target resource is
// touched on, its free time recomputed from what read sees, which on GORM
// includes the write itself. On GraphQL read does not yet see a Batch's
// writes, so the changes' own records are laid over what it reads: claims,
// events and availability created, updated or removed by the write.
func (p *DayProjection) rows(ctx context.Context, read repox.Conn, t targets, changes []repox.Change) (allocation.ResourceDayRows, error) {
	var out allocation.ResourceDayRows
	store := p.open(read)
	want := map[string]engine.Spans{}
	for name, s := range t.resources {
		want[name] = want[name].Union(s)
	}
	expand := func(q ResourceQuery, spans map[string]engine.Spans, keyOf func(*resourcev1.Resource) string) error {
		if len(spans) == 0 {
			return nil
		}
		recs, err := store.Resources(ctx, q)
		if err != nil {
			return err
		}
		for _, r := range recs {
			n := r.Resource.GetName()
			want[n] = want[n].Union(spans[keyOf(r.Resource)])
		}
		return nil
	}
	if err := expand(ResourceQuery{Calendars: keys(t.calendars)}, t.calendars, (*resourcev1.Resource).GetCalendar); err != nil {
		return out, err
	}
	if len(want) == 0 {
		return out, nil
	}
	names := keys(want)
	recs, err := store.Resources(ctx, ResourceQuery{Names: names})
	if err != nil {
		return out, err
	}
	found := map[string]ResourceRecord{}
	for _, r := range recs {
		found[r.Resource.GetName()] = r
	}
	var live []ResourceRecord
	for _, n := range names {
		r, ok := found[n]
		if !ok || r.Resource.GetDeleteTime() != nil {
			// Gone, or deleted: never offered, so nothing to search.
			out.Prune = append(out.Prune, []filterx.Condition{{Field: "resource", Op: filterx.OpEq, Value: n}})
			continue
		}
		live = append(live, r)
	}
	if len(live) == 0 {
		return out, nil
	}
	upsert, err := p.compute(ctx, read, store, live, want, changes)
	if err != nil {
		return out, err
	}
	out.Upsert = upsert
	return out, nil
}

// compute builds the rows of the live resources recs over the days want
// names for each.
func (p *DayProjection) compute(ctx context.Context, read repox.Conn, store Store, recs []ResourceRecord, want map[string]engine.Spans, changes []repox.Change) ([]*allocationpbv1.ResourceDay, error) {
	var span engine.Span
	var names, calendars []string
	seenCal := map[string]bool{}
	for _, r := range recs {
		n := r.Resource.GetName()
		names = append(names, n)
		for _, s := range want[n] {
			if span.Empty() {
				span = s
			} else {
				span = engine.Span{Start: minTime(span.Start, s.Start), End: maxTime(span.End, s.End)}
			}
		}
		if c := r.Resource.GetCalendar(); c != "" && !seenCal[c] {
			seenCal[c] = true
			calendars = append(calendars, c)
		}
	}
	avs, err := store.Availability(ctx, calendars)
	if err != nil {
		return nil, err
	}
	events, err := store.Events(ctx, calendars, span)
	if err != nil {
		return nil, err
	}
	claims, err := store.Claims(ctx, names, span, p.now())
	if err != nil {
		return nil, err
	}
	converted := overlay(claims.Converted, avs, events, changes, read.GraphQL != nil)

	var out []*allocationpbv1.ResourceDay
	type calKey struct {
		cal, loc string
		run      engine.Span
	}
	expanded := map[calKey]engine.Spans{}
	offered := map[calKey]engine.Spans{}
	for _, r := range recs {
		res := r.Resource
		n := res.GetName()
		loc := zone(r.TimeZone)
		cal := res.GetCalendar()
		attrs := attributesOf(res)
		for _, run := range want[n] {
			k := calKey{cal: cal, loc: loc.String(), run: run}
			evs, ok := expanded[k]
			if !ok {
				evs = expandEvents(events[cal], loc, run)
				expanded[k] = evs
			}
			off, ok := offered[k]
			if !ok {
				off = engine.Offered(toAvailabilities(avs[cal], loc), run)
				offered[k] = off
			}
			er := engine.Resource{Name: n, Offered: off, Booked: evs.Union(converted[n])}
			for d := run.Start; d.Before(run.End); d = d.Add(oneDay) {
				free := engine.FreeIntervals(er, d, d.Add(oneDay))
				var secs time.Duration
				row := &allocationpbv1.ResourceDay{
					Resource:      n,
					Day:           &date.Date{Year: int32(d.Year()), Month: int32(d.Month()), Day: int32(d.Day())},
					ResourceTypes: slices.Clone(res.GetResourceTypes()),
					Attributes:    attrs,
					Calendar:      cal,
					DisplayName:   res.GetDisplayName(),
				}
				for _, f := range free {
					row.Free = append(row.Free, intervalOf(f))
					secs += f.Duration()
				}
				row.FreeFraction = float64(secs) / float64(oneDay)
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// overlay lays a write's own claims over what was read: on GORM the read
// already holds them, and laying them again changes nothing (claims of one
// resource never overlap); on GraphQL a Batch's claims are not yet written
// when its projection is computed. With records set (GraphQL), events and
// availability, by calendar, are replaced the same way in avs and events,
// by the record a create or an update queued; on GORM the read is the
// truth, and a queued update's record holds only the fields its mask names.
func overlay(converted map[string]engine.Spans, avs map[string][]*availabilityv1.Availability, events map[string][]*eventv1.Event, changes []repox.Change, records bool) map[string]engine.Spans {
	out := make(map[string]engine.Spans, len(converted))
	for k, v := range converted {
		out[k] = v
	}
	for _, c := range changes {
		removed := c.Op == repox.ChangeDelete || c.Op == repox.ChangePurge
		switch c.Type {
		case typeAllocation:
			before, _ := c.Prior.(*allocationpbv1.Allocation)
			after, _ := c.Record.(*allocationpbv1.Allocation)
			if before != nil && before.GetExpireTime() == nil {
				out[before.GetResource()] = out[before.GetResource()].Subtract(engine.Spans{claimSpan(before)})
			}
			if after != nil && !removed && after.GetExpireTime() == nil {
				res, span := cmpOr(after.GetResource(), before.GetResource()), claimSpan(after)
				if span.Empty() && before != nil {
					span = claimSpan(before)
				}
				out[res] = out[res].Union(engine.Spans{span})
			}
		case typeEvent:
			if !records {
				continue
			}
			cal := parentOf(c.Name)
			list, ok := events[cal]
			if !ok && removed {
				continue
			}
			list = slices.DeleteFunc(slices.Clone(list), func(e *eventv1.Event) bool { return e.GetName() == c.Name })
			if e, ok := c.Record.(*eventv1.Event); ok && e != nil && !removed && e.GetStart() != nil {
				list = append(list, e)
			}
			events[cal] = list
		case typeAvailability:
			if !records {
				continue
			}
			cal := parentOf(c.Name)
			list, ok := avs[cal]
			if !ok && removed {
				continue
			}
			list = slices.DeleteFunc(slices.Clone(list), func(a *availabilityv1.Availability) bool { return a.GetName() == c.Name })
			if a, ok := c.Record.(*availabilityv1.Availability); ok && a != nil && !removed && len(a.GetAvailablePeriods()) > 0 {
				list = append(list, a)
			}
			avs[cal] = list
		}
	}
	return out
}

// attributesOf merges every JSON object the resource's STRUCTURED-DATA
// carries as inline text, in order, a later key replacing an earlier one.
// Anything else -- a URI, binary data, text that is not a JSON object -- is
// not searchable and is skipped.
func attributesOf(r *resourcev1.Resource) *structpb.Struct {
	merged := map[string]any{}
	for _, sd := range r.GetStructuredData() {
		text := sd.GetText()
		if text == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(text), &obj); err != nil {
			continue
		}
		for k, v := range obj {
			merged[k] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	s, err := structpb.NewStruct(merged)
	if err != nil {
		return nil
	}
	return s
}

// keys returns m's keys, sorted.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// parentOf is the calendar an event or availability name sits under:
// "calendars/c/events/e" is "calendars/c".
func parentOf(name string) string {
	n := 0
	for i := range len(name) {
		if name[i] == '/' {
			if n++; n == 2 {
				return name[:i]
			}
		}
	}
	return name
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Rebuild recomputes the read model for every resource, rebuildBatch
// resources a transaction, across the whole horizon, removing the rows of
// resources that are gone. It is the backfill after the read model first
// ships and the repair after anything wrote to its sources around the
// repositories. Searches keep answering throughout: each batch replaces
// rows, nothing is truncated first.
func (p *DayProjection) Rebuild(ctx context.Context, conn repox.Conn) (repox.RebuildResult, error) {
	var rerr error
	res, err := allocation.ResourceDays.Rebuild(ctx, conn, p.Writer(), p.resourceBatches(ctx, conn, &rerr), repox.RebuildOptions{})
	if err == nil {
		err = rerr
	}
	if err != nil {
		return res, err
	}
	// Rows of a resource the repositories no longer have, and of days
	// before the horizon.
	stale, err := p.staleRows(ctx, conn)
	if err != nil {
		return res, err
	}
	if len(stale.Prune) > 0 {
		if err := allocation.ResourceDays.Apply(ctx, conn, stale); err != nil {
			return res, err
		}
		res.Deleted += len(stale.Prune)
	}
	return res, nil
}

// resourceBatches yields every resource, live or deleted, as ChangeUpdate
// changes, rebuildBatch at a time; err, when not nil, receives a read error.
func (p *DayProjection) resourceBatches(ctx context.Context, conn repox.Conn, err *error) iter.Seq[[]repox.Change] {
	return func(yield func([]repox.Change) bool) {
		store := p.open(conn)
		recs, rerr := store.Resources(ctx, ResourceQuery{})
		if rerr != nil {
			if err != nil {
				*err = rerr
			}
			return
		}
		for chunk := range slices.Chunk(recs, rebuildBatch) {
			batch := make([]repox.Change, len(chunk))
			for i, r := range chunk {
				batch[i] = repox.Change{Type: typeResource, Name: r.Resource.GetName(), Op: repox.ChangeUpdate}
			}
			if !yield(batch) {
				return
			}
		}
	}
}

// staleRows are prunes for the rows of resources no longer stored and of days
// before the horizon.
func (p *DayProjection) staleRows(ctx context.Context, conn repox.Conn) (allocation.ResourceDayRows, error) {
	var out allocation.ResourceDayRows
	h := p.Horizon(p.now())
	out.Prune = append(out.Prune, []filterx.Condition{{Field: "day", Op: filterx.OpLt, Value: h.Start.Format(time.DateOnly)}})
	recs, err := p.open(conn).Resources(ctx, ResourceQuery{})
	if err != nil {
		return out, err
	}
	have := map[string]bool{}
	for _, r := range recs {
		have[r.Resource.GetName()] = true
	}
	buckets, err := allocation.ResourceDays.Facet(ctx, conn, repox.SearchInput{}, "resource", 1<<30)
	if err != nil {
		return out, err
	}
	for _, b := range buckets {
		if !have[b.Value] {
			out.Prune = append(out.Prune, []filterx.Condition{{Field: "resource", Op: filterx.OpEq, Value: b.Value}})
		}
	}
	return out, nil
}

// Empty reports whether the read model has no rows at all: a fresh
// deployment, or one upgraded from a version without it.
func (p *DayProjection) Empty(ctx context.Context, conn repox.Conn) (bool, error) {
	rows, _, err := allocation.ResourceDays.Search(ctx, conn, repox.SearchInput{PageSize: 1, ReadMask: []string{"day"}})
	return len(rows) == 0, err
}

// ExtendHorizon moves the read model on to today: it drops the days before
// the horizon and projects, for every resource, the days between the last one
// stored and the horizon's end. A table with no rows is left alone -- that
// is a backfill, Rebuild, which the server runs at startup ([search]
// rebuild_on_start) -- and so is one already at the horizon. It returns how
// many rows it stored.
func (p *DayProjection) ExtendHorizon(ctx context.Context, conn repox.Conn) (int, error) {
	h := p.Horizon(p.now())
	if err := allocation.ResourceDays.Apply(ctx, conn, allocation.ResourceDayRows{
		Prune: [][]filterx.Condition{{{Field: "day", Op: filterx.OpLt, Value: h.Start.Format(time.DateOnly)}}},
	}); err != nil {
		return 0, err
	}
	last, _, err := allocation.ResourceDays.Search(ctx, conn, repox.SearchInput{OrderBy: "day desc", PageSize: 1, ReadMask: []string{"day"}})
	if err != nil || len(last) == 0 {
		return 0, err
	}
	d := last[0].GetDay()
	from := time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC).Add(oneDay)
	if from.Before(h.Start) {
		from = h.Start
	}
	if !from.Before(h.End) {
		return 0, nil
	}
	span := engine.Span{Start: from, End: h.End}
	w := allocation.ResourceDayWriterFunc(func(ctx context.Context, read repox.Conn, changes []repox.Change) (allocation.ResourceDayRows, error) {
		t := newTargets()
		for _, c := range changes {
			add(t.resources, c.Name, span)
		}
		return p.rows(ctx, read, t, nil)
	})
	var rerr error
	res, err := allocation.ResourceDays.Rebuild(ctx, conn, w, p.resourceBatches(ctx, conn, &rerr), repox.RebuildOptions{})
	if err == nil {
		err = rerr
	}
	return res.Upserted, err
}

// horizonInterval is how often the horizon job runs.
const horizonInterval = 24 * time.Hour

// StartHorizon runs ExtendHorizon now and then daily until ctx ends, beside
// the hold sweeper. Non-blocking.
func (p *DayProjection) StartHorizon(ctx context.Context, conn repox.Conn, logErr func(error)) {
	go func() {
		t := time.NewTicker(horizonInterval)
		defer t.Stop()
		for {
			if _, err := p.ExtendHorizon(ctx, conn); err != nil && ctx.Err() == nil && logErr != nil {
				logErr(fmt.Errorf("extend the read model's horizon: %w", err))
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// postgresDays searches the read model's own table.
type postgresDays struct{ conn repox.Conn }

// PostgresDays is a DaySearcher over the read model's table, read through
// conn (Connection.SearchConn).
func PostgresDays(conn repox.Conn) DaySearcher { return postgresDays{conn: conn} }

// overGraphQL reports that the table is read through Hasura, whose
// generated metadata has no comparison on array columns: a resource_types
// condition is applied to the rows read instead.
func (p postgresDays) overGraphQL() bool { return p.conn.GraphQL != nil }

func (p postgresDays) SearchDays(ctx context.Context, in repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error) {
	return allocation.ResourceDays.Search(ctx, p.conn, in)
}

func (p postgresDays) FacetDays(ctx context.Context, in repox.SearchInput, field string, limit int) ([]repox.Bucket, error) {
	return allocation.ResourceDays.Facet(ctx, p.conn, in, field, limit)
}

// checkFilter reports whether filter is an AIP-160 filter over the read
// model's fields, without running it.
func checkFilter(filter string) error {
	if filter == "" {
		return nil
	}
	conds, err := filterx.Parse(filter)
	if err == nil {
		_, _, err = filterx.Gorm[struct{}](allocation.ResourceDays.Spec.Filter).Where(conds)
	}
	return err
}

// checkOrderBy reports whether orderBy is an AIP-132 order over the read
// model's sortable fields.
func checkOrderBy(orderBy string) error {
	_, err := filterx.OrderTerms(allocation.ResourceDays.Spec.Filter, orderBy)
	return err
}

// orderByPath reports whether orderBy orders by a JSON attribute path
// (attributes.max_occupancy).
func orderByPath(orderBy string) bool {
	terms, err := filterx.OrderTerms(allocation.ResourceDays.Spec.Filter, orderBy)
	return err == nil && slices.ContainsFunc(terms, func(t filterx.OrderTerm) bool { return len(t.Path) > 0 })
}

// RebuildResources recomputes the read model for the named resources only,
// across the horizon: the repair after a write to them that bypassed the
// repositories.
func (p *DayProjection) RebuildResources(ctx context.Context, conn repox.Conn, names []string) (repox.RebuildResult, error) {
	batches := func(yield func([]repox.Change) bool) {
		for chunk := range slices.Chunk(names, rebuildBatch) {
			batch := make([]repox.Change, len(chunk))
			for i, n := range chunk {
				batch[i] = repox.Change{Type: typeResource, Name: n, Op: repox.ChangeUpdate}
			}
			if !yield(batch) {
				return
			}
		}
	}
	return allocation.ResourceDays.Rebuild(ctx, conn, p.Writer(), batches, repox.RebuildOptions{})
}
