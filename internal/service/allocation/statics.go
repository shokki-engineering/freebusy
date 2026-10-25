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

// VersionedStore is a Store whose static inputs -- resources, sites,
// calendars and their availability -- carry a version that moves on every
// write to them. The Server caches those inputs, converted, per site and
// version, and reads them again only when the version moves. A Store without
// it is read in full on every request.
type VersionedStore interface {
	Store
	// InputVersion is the current version. An error means it cannot be
	// told, and nothing cached is used.
	InputVersion(ctx context.Context) (string, error)
	// Sites returns an organization's live sites, in name order.
	Sites(ctx context.Context, organization string) ([]string, error)
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

// staticsMaxSites bounds the sites cached at once. Past it the oldest entry
// is dropped.
const staticsMaxSites = 4096

// siteEntry is one site's cached inputs, or the load filling it.
type siteEntry struct {
	version string
	loaded  time.Time
	done    chan struct{}
	res     []staticResource
	err     error
}

// statics caches converted static inputs per site.
type statics struct {
	mu    sync.Mutex
	sites map[string]*siteEntry
	orgs  map[string]*orgEntry
}

type orgEntry struct {
	version string
	loaded  time.Time
	sites   []string
}

func newStatics() *statics {
	return &statics{sites: map[string]*siteEntry{}, orgs: map[string]*orgEntry{}}
}

// drop forgets a site's entry, so the next read loads it again.
func (c *statics) drop(site string) {
	c.mu.Lock()
	delete(c.sites, site)
	c.mu.Unlock()
}

// staticInputs returns the static inputs of the resources q selects, from the
// cache when the store is versioned and fresh is false, in name order.
func (s *Server) staticInputs(ctx context.Context, q ResourceQuery, fresh bool) ([]staticResource, error) {
	vs, ok := s.store.(VersionedStore)
	if !ok || fresh || len(q.Calendars) > 0 {
		return s.readStatics(ctx, q)
	}
	version, err := vs.InputVersion(ctx)
	if err != nil {
		return s.readStatics(ctx, q)
	}
	sites := q.OrganizationalUnits
	if len(sites) == 0 && q.Organization != "" {
		if sites, err = s.orgSites(ctx, vs, q.Organization, version); err != nil {
			return nil, err
		}
	}
	if len(sites) == 0 {
		// No scope the cache is keyed by: read what was asked.
		return s.readStatics(ctx, q)
	}
	var out []staticResource
	for _, site := range sites {
		res, err := s.siteStatics(ctx, site, version)
		if err != nil {
			return nil, err
		}
		for _, r := range res {
			if selected(r.rec, q) {
				out = append(out, r)
			}
		}
	}
	if len(sites) > 1 {
		slices.SortFunc(out, func(a, b staticResource) int {
			return strings.Compare(a.rec.Resource.GetName(), b.rec.Resource.GetName())
		})
	}
	return out, nil
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

// orgSites returns an organization's sites, cached under version.
func (s *Server) orgSites(ctx context.Context, vs VersionedStore, org, version string) ([]string, error) {
	now := time.Now()
	s.statics.mu.Lock()
	e := s.statics.orgs[org]
	s.statics.mu.Unlock()
	if e != nil && e.version == version && now.Sub(e.loaded) < staticsMaxAge {
		return e.sites, nil
	}
	sites, err := vs.Sites(ctx, org)
	if err != nil {
		return nil, err
	}
	s.statics.mu.Lock()
	if len(s.statics.orgs) >= staticsMaxSites {
		clear(s.statics.orgs)
	}
	s.statics.orgs[org] = &orgEntry{version: version, loaded: now, sites: sites}
	s.statics.mu.Unlock()
	return sites, nil
}

// siteStatics returns one site's inputs under version, loading them once when
// they are missing or stale however many callers ask at the same time.
func (s *Server) siteStatics(ctx context.Context, site, version string) ([]staticResource, error) {
	c := s.statics
	for {
		c.mu.Lock()
		e := c.sites[site]
		if e != nil {
			select {
			case <-e.done:
				if e.err == nil && e.version == version && time.Since(e.loaded) < staticsMaxAge {
					c.mu.Unlock()
					return e.res, nil
				}
				// Stale or failed: replace it below.
			default:
				if e.version == version {
					c.mu.Unlock()
					select {
					case <-e.done:
						continue
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
		}
		e = &siteEntry{version: version, done: make(chan struct{})}
		if len(c.sites) >= staticsMaxSites {
			c.evictOldestLocked()
		}
		c.sites[site] = e
		c.mu.Unlock()

		// The load is shared, so it must outlive the caller who started it.
		e.res, e.err = s.readStatics(context.WithoutCancel(ctx), ResourceQuery{OrganizationalUnits: []string{site}})
		e.loaded = time.Now()
		close(e.done)
		if e.err != nil {
			c.mu.Lock()
			if c.sites[site] == e {
				delete(c.sites, site)
			}
			c.mu.Unlock()
		}
		return e.res, e.err
	}
}

func (c *statics) evictOldestLocked() {
	var oldest string
	var at time.Time
	for k, e := range c.sites {
		select {
		case <-e.done:
		default:
			continue
		}
		if oldest == "" || e.loaded.Before(at) {
			oldest, at = k, e.loaded
		}
	}
	if oldest != "" {
		delete(c.sites, oldest)
	}
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
