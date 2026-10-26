// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"google.golang.org/protobuf/proto"
)

// VersionedStore is a Store whose static inputs -- resources, calendars and
// their availability -- carry a version that moves on every write to them.
// The Server caches those inputs, converted, per resource and version, and
// reads them again only when the version moves. A Store without it is read in
// full on every request.
type VersionedStore interface {
	Store
	// InputVersion is the current version. An error means it cannot be
	// told, and nothing cached is used.
	InputVersion(ctx context.Context) (string, error)
}

// staticResource is one resource's static inputs, converted: everything the
// engine needs about it that is not a booking.
type staticResource struct {
	rec      ResourceRecord
	loc      *time.Location
	avs      []engine.Availability
	policy   engine.Policy
	inactive bool
	// raw are the stored availability components avs came from, kept to
	// compare against the database when a hold takes this resource.
	raw []proto.Message
}

// staticsMaxAge bounds how long cached inputs are used even when the version
// has not moved: the backstop for a version that failed to move (a cache
// outage on another instance). Holds never depend on it; see verifyPicked.
const staticsMaxAge = 2 * time.Minute

// staticsMaxResources bounds the resources cached at once. Past it the cache
// starts over.
const staticsMaxResources = 65536

// resourceEntry is one resource's cached inputs.
type resourceEntry struct {
	version string
	loaded  time.Time
	res     staticResource
}

// poolEntry is the names of every resource, cached.
type poolEntry struct {
	version string
	loaded  time.Time
	names   []string
}

// statics caches converted static inputs per resource, and the names of
// every resource for a request that names none.
type statics struct {
	mu        sync.Mutex
	resources map[string]*resourceEntry
	pool      *poolEntry
}

func newStatics() *statics {
	return &statics{resources: map[string]*resourceEntry{}}
}

// drop forgets names' entries, so the next read loads them again.
func (c *statics) drop(names []string) {
	c.mu.Lock()
	for _, n := range names {
		delete(c.resources, n)
	}
	c.mu.Unlock()
}

// lookup returns the entries of names cached under version, and the names
// that are not.
func (c *statics) lookup(names []string, version string, now time.Time) (hit []staticResource, miss []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		e := c.resources[n]
		if e == nil || e.version != version || now.Sub(e.loaded) >= staticsMaxAge {
			miss = append(miss, n)
			continue
		}
		hit = append(hit, e.res)
	}
	return hit, miss
}

// store caches res under version; with all, res is every resource there is.
func (c *statics) store(res []staticResource, version string, now time.Time, all bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.resources)+len(res) > staticsMaxResources {
		clear(c.resources)
	}
	for _, r := range res {
		c.resources[r.rec.Resource.GetName()] = &resourceEntry{version: version, loaded: now, res: r}
	}
	if all {
		names := make([]string, len(res))
		for i, r := range res {
			names[i] = r.rec.Resource.GetName()
		}
		c.pool = &poolEntry{version: version, loaded: now, names: names}
	}
}

// poolNames returns the names of every resource cached under version.
func (c *statics) poolNames(version string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pool == nil || c.pool.version != version || now.Sub(c.pool.loaded) >= staticsMaxAge {
		return nil, false
	}
	return c.pool.names, true
}

// staticInputs returns the static inputs of the resources q selects, from the
// cache when the store is versioned and fresh is false, in name order. Only
// the resources the cache lacks are read, in one batch.
func (s *Server) staticInputs(ctx context.Context, q ResourceQuery, fresh bool) ([]staticResource, error) {
	vs, ok := s.store.(VersionedStore)
	if !ok || fresh || len(q.Calendars) > 0 {
		return s.readStatics(ctx, q)
	}
	version, err := vs.InputVersion(ctx)
	if err != nil {
		return s.readStatics(ctx, q)
	}
	now := time.Now()
	names := q.Names
	if len(names) == 0 {
		if names, ok = s.statics.poolNames(version, now); !ok {
			all, err := s.readStatics(ctx, ResourceQuery{})
			if err != nil {
				return nil, err
			}
			s.statics.store(all, version, now, true)
			return slices.DeleteFunc(all, func(r staticResource) bool { return !selected(r.rec, q) }), nil
		}
	}
	out, miss := s.statics.lookup(names, version, now)
	if len(miss) > 0 {
		read, err := s.readStatics(ctx, ResourceQuery{Names: miss})
		if err != nil {
			return nil, err
		}
		s.statics.store(read, version, now, false)
		out = append(out, read...)
	}
	// Every entry is one of names already; only the types are left to apply.
	types := ResourceQuery{Types: q.Types}
	out = slices.DeleteFunc(out, func(r staticResource) bool { return !selected(r.rec, types) })
	slices.SortFunc(out, func(a, b staticResource) int {
		return strings.Compare(a.rec.Resource.GetName(), b.rec.Resource.GetName())
	})
	// A name asked for twice is one resource.
	return slices.CompactFunc(out, func(a, b staticResource) bool {
		return a.rec.Resource.GetName() == b.rec.Resource.GetName()
	}), nil
}

// selected applies q's Names and Types to one cached resource.
func selected(rec ResourceRecord, q ResourceQuery) bool {
	r := rec.Resource
	if len(q.Names) > 0 && !slices.Contains(q.Names, r.GetName()) {
		return false
	}
	if len(q.Types) > 0 && !slices.ContainsFunc(r.GetResourceTypes(), func(t string) bool { return slices.Contains(q.Types, t) }) {
		return false
	}
	return true
}

// readStatics reads the static inputs of the resources q selects from the
// store and converts them.
func (s *Server) readStatics(ctx context.Context, q ResourceQuery) ([]staticResource, error) {
	recs, err := s.store.Resources(ctx, q)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	var calendars []string
	seen := map[string]bool{}
	for _, r := range recs {
		if c := r.Resource.GetCalendar(); c != "" && !seen[c] {
			seen[c] = true
			calendars = append(calendars, c)
		}
	}
	avs, err := s.store.Availability(ctx, calendars)
	if err != nil {
		return nil, err
	}
	// One conversion per calendar, shared by the resources on it.
	converted := map[string][]engine.Availability{}
	out := make([]staticResource, 0, len(recs))
	for _, rec := range recs {
		loc := zone(rec.TimeZone)
		cal := rec.Resource.GetCalendar()
		key := cal + "\x00" + loc.String()
		conv, ok := converted[key]
		if !ok {
			conv = toAvailabilities(avs[cal], loc)
			converted[key] = conv
		}
		raw := make([]proto.Message, len(avs[cal]))
		for i, a := range avs[cal] {
			raw[i] = a
		}
		out = append(out, staticResource{
			rec:      rec,
			loc:      loc,
			avs:      conv,
			policy:   policyOf(rec),
			inactive: rec.Resource.GetDeleteTime() != nil,
			raw:      raw,
		})
	}
	return out, nil
}

// sameStatics reports whether two reads of one resource's static inputs agree.
func sameStatics(a, b staticResource) bool {
	if a.rec.TimeZone != b.rec.TimeZone || !proto.Equal(a.rec.Resource, b.rec.Resource) || len(a.raw) != len(b.raw) {
		return false
	}
	for i := range a.raw {
		if !proto.Equal(a.raw[i], b.raw[i]) {
			return false
		}
	}
	return true
}
