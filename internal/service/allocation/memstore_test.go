package allocation

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// memStore is an in-memory Store with the same guarantees the database
// adapters must give, so the service's behaviour can be tested without one.
type memStore struct {
	mu        sync.Mutex
	resources []ResourceRecord
	avail     map[string][]AvailabilityRecord
	events    map[string][]PeriodRecord
	holds     map[string]*allocationpbv1.Hold
	order     []string
	version   int
	// clock is shared with the Server under test, so "live" means the same
	// instant to both.
	clock func() time.Time
	// beforeInsert, when set, runs inside InsertHold before the guard: a hook
	// for tests to simulate a competing write landing mid-flight.
	beforeInsert func()
}

func newMemStore() *memStore {
	return &memStore{
		avail:  map[string][]AvailabilityRecord{},
		events: map[string][]PeriodRecord{},
		holds:  map[string]*allocationpbv1.Hold{},
		clock:  time.Now,
	}
}

func (m *memStore) Resources(_ context.Context, q ResourceQuery) ([]ResourceRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ResourceRecord
	for _, r := range m.resources {
		res := r.Resource
		if len(q.OrganizationalUnits) > 0 && !slices.Contains(q.OrganizationalUnits, res.GetOrganizationalUnit()) {
			continue
		}
		if len(q.Calendars) > 0 && !slices.Contains(q.Calendars, res.GetCalendar()) {
			continue
		}
		if len(q.Names) > 0 && !slices.Contains(q.Names, res.GetName()) {
			continue
		}
		if len(q.Types) > 0 && !slices.ContainsFunc(res.GetResourceTypes(), func(t string) bool { return slices.Contains(q.Types, t) }) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) Availability(_ context.Context, calendars []string) (map[string][]AvailabilityRecord, error) {
	out := map[string][]AvailabilityRecord{}
	for _, c := range calendars {
		if a, ok := m.avail[c]; ok {
			out[c] = a
		}
	}
	return out, nil
}

func (m *memStore) Events(_ context.Context, calendars []string, _ engine.Span) (map[string][]PeriodRecord, error) {
	out := map[string][]PeriodRecord{}
	for _, c := range calendars {
		if e, ok := m.events[c]; ok {
			out[c] = e
		}
	}
	return out, nil
}

func (m *memStore) Claims(_ context.Context, resources []string, window engine.Span, now time.Time) (Claims, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimsLocked(resources, window, now), nil
}

func (m *memStore) claimsLocked(resources []string, window engine.Span, now time.Time) Claims {
	c := Claims{Held: map[string]engine.Spans{}, Converted: map[string]engine.Spans{}}
	for _, h := range m.holds {
		w := engine.Span{Start: h.GetWindow().GetStartTime().AsTime(), End: h.GetWindow().GetEndTime().AsTime()}
		if !w.Overlaps(window) {
			continue
		}
		for _, r := range h.GetResources() {
			if !slices.Contains(resources, r) {
				continue
			}
			switch {
			case h.GetExpireTime() == nil:
				c.Converted[r] = c.Converted[r].Union(engine.Spans{w})
			case h.GetExpireTime().AsTime().After(now):
				c.Held[r] = c.Held[r].Union(engine.Spans{w})
			}
		}
	}
	return c
}

func (m *memStore) InsertHold(_ context.Context, _ string, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	if m.beforeInsert != nil {
		f := m.beforeInsert
		m.beforeInsert = nil
		f()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.holds[h.GetName()]; ok {
		return nil, types.ErrAlreadyExists
	}
	w := engine.Span{Start: h.GetWindow().GetStartTime().AsTime(), End: h.GetWindow().GetEndTime().AsTime()}
	c := m.claimsLocked(h.GetResources(), w, m.clock())
	for _, r := range h.GetResources() {
		if c.Held[r].AnyOverlap(w) || c.Converted[r].AnyOverlap(w) {
			return nil, types.ErrConflict
		}
	}
	stored := proto.Clone(h).(*allocationpbv1.Hold)
	m.version++
	stored.Etag = strconv.Itoa(m.version)
	stored.CreateTime = timestamppb.Now()
	m.holds[h.GetName()] = stored
	m.order = append(m.order, h.GetName())
	return proto.Clone(stored).(*allocationpbv1.Hold), nil
}

func (m *memStore) GetHold(_ context.Context, name string) (*allocationpbv1.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[name]
	if !ok {
		return nil, types.ErrNotFound
	}
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (m *memStore) FindHold(_ context.Context, parent, key string) (*allocationpbv1.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.order {
		h := m.holds[n]
		if h != nil && h.GetIdempotencyKey() == key && len(n) > len(parent) && n[:len(parent)] == parent {
			return proto.Clone(h).(*allocationpbv1.Hold), nil
		}
	}
	return nil, types.ErrNotFound
}

func (m *memStore) ListHolds(_ context.Context, parent string, _ repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*allocationpbv1.Hold
	for _, n := range m.order {
		if h := m.holds[n]; h != nil && n[:len(parent)] == parent {
			out = append(out, proto.Clone(h).(*allocationpbv1.Hold))
		}
	}
	return out, "", nil
}

func (m *memStore) UpdateHold(_ context.Context, name, etag string, state allocationpbv1.HoldState, expire *time.Time) (*allocationpbv1.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[name]
	if !ok {
		return nil, types.ErrNotFound
	}
	if etag != "" && etag != h.GetEtag() {
		return nil, types.ErrConflict
	}
	h.State = state
	h.ExpireTime = nil
	if expire != nil {
		h.ExpireTime = timestamppb.New(*expire)
	}
	m.version++
	h.Etag = strconv.Itoa(m.version)
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (m *memStore) DeleteHold(_ context.Context, name, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[name]
	if !ok {
		return types.ErrNotFound
	}
	if etag != "" && etag != h.GetEtag() {
		return types.ErrConflict
	}
	delete(m.holds, name)
	return nil
}

func (m *memStore) ExpireHolds(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, h := range m.holds {
		if h.GetState() == allocationpbv1.HoldState_HOLD_STATE_ACTIVE && h.GetExpireTime() != nil && !h.GetExpireTime().AsTime().After(now) {
			h.State = allocationpbv1.HoldState_HOLD_STATE_EXPIRED
			n++
		}
	}
	return n, nil
}
