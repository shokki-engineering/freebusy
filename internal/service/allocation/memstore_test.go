// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/genproto/googleapis/type/datetime"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// memStore is an in-memory Store with the same guarantees the database
// adapters must give, so the service's behaviour can be tested without one.
type memStore struct {
	mu        sync.Mutex
	resources []ResourceRecord
	avail     map[string][]*availabilityv1.Availability
	events    map[string][]*eventv1.Event
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
		avail:  map[string][]*availabilityv1.Availability{},
		events: map[string][]*eventv1.Event{},
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

func (m *memStore) Availability(_ context.Context, calendars []string) (map[string][]*availabilityv1.Availability, error) {
	out := map[string][]*availabilityv1.Availability{}
	for _, c := range calendars {
		if a, ok := m.avail[c]; ok {
			out[c] = a
		}
	}
	return out, nil
}

func (m *memStore) Events(_ context.Context, calendars []string, _ engine.Span) (map[string][]*eventv1.Event, error) {
	out := map[string][]*eventv1.Event{}
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
	return m.claimsLocked(resources, window, now, ""), nil
}

// claimsLocked is Claims, leaving out the claims of hold except.
func (m *memStore) claimsLocked(resources []string, window engine.Span, now time.Time, except string) Claims {
	c := Claims{Held: map[string]engine.Spans{}, Converted: map[string]engine.Spans{}}
	for n, h := range m.holds {
		if except != "" && n == except {
			continue
		}
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

func (m *memStore) InsertHold(_ context.Context, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	if m.beforeInsert != nil {
		f := m.beforeInsert
		m.beforeInsert = nil
		f()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.holds[h.GetName()]; ok {
		return nil, repox.ErrAlreadyExists
	}
	w := engine.Span{Start: h.GetWindow().GetStartTime().AsTime(), End: h.GetWindow().GetEndTime().AsTime()}
	c := m.claimsLocked(h.GetResources(), w, m.clock(), h.GetReplaces())
	for _, r := range h.GetResources() {
		if c.Held[r].AnyOverlap(w) || c.Converted[r].AnyOverlap(w) {
			return nil, repox.ErrConflict
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
		return nil, repox.ErrNotFound
	}
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (m *memStore) FindHold(_ context.Context, key string) (*allocationpbv1.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.order) - 1; i >= 0; i-- {
		h := m.holds[m.order[i]]
		if h != nil && h.GetIdempotencyKey() == key {
			return proto.Clone(h).(*allocationpbv1.Hold), nil
		}
	}
	return nil, repox.ErrNotFound
}

func (m *memStore) ListHolds(_ context.Context, _ repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*allocationpbv1.Hold
	for _, n := range m.order {
		if h := m.holds[n]; h != nil {
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
		return nil, repox.ErrNotFound
	}
	if etag != "" && etag != h.GetEtag() {
		return nil, repox.ErrConflict
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

// HandOver ends from as of at. Holds here claim their whole window, so to
// already has it all.
func (m *memStore) HandOver(_ context.Context, from, etag, _ string, at time.Time) (*allocationpbv1.Hold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[from]
	if !ok {
		return nil, repox.ErrNotFound
	}
	if etag != "" && etag != h.GetEtag() {
		return nil, repox.ErrConflict
	}
	h.ExpireTime = timestamppb.New(at)
	m.version++
	h.Etag = strconv.Itoa(m.version)
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (m *memStore) DeleteHold(_ context.Context, name, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[name]
	if !ok {
		return repox.ErrNotFound
	}
	if etag != "" && etag != h.GetEtag() {
		return repox.ErrConflict
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

// dateTime is t stored as a DATE-TIME in t's own zone.
func dateTime(t time.Time) *datetime.DateTime {
	return &datetime.DateTime{
		Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day()),
		Hours: int32(t.Hour()), Minutes: int32(t.Minute()), Seconds: int32(t.Second()),
		TimeOffset: &datetime.DateTime_TimeZone{TimeZone: &datetime.TimeZone{Id: t.Location().String()}},
	}
}

func eventTime(t time.Time) *eventv1.CalendarTime {
	return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dateTime(t)}}
}

// busy is a stored event from start to end.
func busy(start, end time.Time) *eventv1.Event {
	return &eventv1.Event{Start: eventTime(start), Ends: &eventv1.Event_End{End: eventTime(end)}}
}

// opens is a stored VAVAILABILITY with one AVAILABLE period from start to
// end, repeating by rule when it is set.
func opens(start, end time.Time, rule string) *availabilityv1.Availability {
	at := func(t time.Time) *availabilityv1.CalendarTime {
		return &availabilityv1.CalendarTime{Value: &availabilityv1.CalendarTime_DateTime{DateTime: dateTime(t)}}
	}
	return &availabilityv1.Availability{AvailablePeriods: []*availabilityv1.AvailablePeriod{{
		Start: at(start), EndForm: &availabilityv1.AvailablePeriod_End{End: at(end)}, RecurrenceRule: rule,
	}}}
}
