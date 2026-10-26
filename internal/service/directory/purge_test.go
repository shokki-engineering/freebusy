// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/resource"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// row is one stored record in a fake table: its name, the parent it names
// (a resource's calendar), and its AIP-164 times.
type row struct {
	name, parent, etag string
	deleted, expire    *timestamppb.Timestamp
}

// table is a fake repository's storage, answering the List shapes the purger
// sends.
type table struct {
	parentField string
	rows        map[string]*row
	purged      []string
	// referrers are the tables whose rows name this one's as their parent:
	// a row one of them still names is kept, as a foreign key keeps it.
	referrers []*table
}

func newTable(parentField string, rows ...*row) *table {
	t := &table{parentField: parentField, rows: map[string]*row{}}
	for _, r := range rows {
		r.etag = "e1"
		t.rows[r.name] = r
	}
	return t
}

func (t *table) list(in repox.ListInput) []*row {
	var out []*row
	for _, r := range t.rows {
		if r.deleted != nil && !in.ShowDeleted {
			continue
		}
		ok := true
		for _, c := range in.Where {
			switch {
			case c.Field == "delete_time" && c.Op == filterx.OpNull:
				ok = ok && (r.deleted == nil) == (c.Value == "true")
			case c.Field == "expire_time" && c.Op == filterx.OpNull:
				ok = ok && (r.expire == nil) == (c.Value == "true")
			case c.Field == t.parentField && c.Op == filterx.OpEq:
				ok = ok && r.parent == c.Value
			default:
				panic("fake table cannot filter on " + c.Field)
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if in.PageSize > 0 && len(out) > int(in.PageSize) {
		out = out[:in.PageSize]
	}
	return out
}

func (t *table) expire(name, etag string, at *timestamppb.Timestamp) error {
	r, ok := t.rows[name]
	switch {
	case !ok:
		return repox.ErrNotFound
	case etag != "" && etag != r.etag:
		return repox.ErrConflict
	}
	r.expire, r.etag = at, r.etag+"'"
	return nil
}

// purgeExpired is PurgeExpired: every deleted row whose expire_time has
// passed goes, unless a referrer's row still names it.
func (t *table) purgeExpired(now time.Time) (repox.PurgeResult, error) {
	var res repox.PurgeResult
	for _, r := range t.list(repox.ListInput{ShowDeleted: true}) {
		if r.deleted == nil || r.expire == nil || r.expire.AsTime().After(now) {
			continue
		}
		named := false
		for _, ref := range t.referrers {
			for _, c := range ref.rows {
				named = named || c.parent == r.name
			}
		}
		if named {
			res.Kept = append(res.Kept, r.name)
			continue
		}
		delete(t.rows, r.name)
		t.purged = append(t.purged, r.name)
		res.Purged = append(res.Purged, r.name)
	}
	return res, nil
}

type fakeCalendars struct {
	calendar.CalendarRepository
	*table
}

func (f fakeCalendars) List(_ context.Context, in repox.ListInput) ([]*calendarv1.Calendar, string, error) {
	var out []*calendarv1.Calendar
	for _, r := range f.list(in) {
		out = append(out, &calendarv1.Calendar{Name: r.name, Etag: r.etag, DeleteTime: r.deleted, ExpireTime: r.expire})
	}
	return out, "", nil
}

func (f fakeCalendars) UpdateOutputOnly(_ context.Context, m *calendarv1.Calendar, _ []string) (*calendarv1.Calendar, error) {
	return m, f.expire(m.GetName(), m.GetEtag(), m.GetExpireTime())
}

func (f fakeCalendars) PurgeExpired(_ context.Context, now time.Time) (repox.PurgeResult, error) {
	return f.purgeExpired(now)
}

type fakeResources struct {
	resource.ResourceRepository
	*table
}

func (f fakeResources) List(_ context.Context, in repox.ListInput) ([]*resourcev1.Resource, string, error) {
	var out []*resourcev1.Resource
	for _, r := range f.list(in) {
		out = append(out, &resourcev1.Resource{Name: r.name, Etag: r.etag, Calendar: r.parent, DeleteTime: r.deleted, ExpireTime: r.expire})
	}
	return out, "", nil
}

func (f fakeResources) UpdateOutputOnly(_ context.Context, m *resourcev1.Resource, _ []string) (*resourcev1.Resource, error) {
	return m, f.expire(m.GetName(), m.GetEtag(), m.GetExpireTime())
}

func (f fakeResources) PurgeExpired(_ context.Context, now time.Time) (repox.PurgeResult, error) {
	return f.purgeExpired(now)
}

// Calendars hold no events or availability here; their fakes list nothing.
type emptyEvents struct{ event.EventRepository }

func (emptyEvents) List(context.Context, string, repox.ListInput) ([]*eventv1.Event, string, error) {
	return nil, "", nil
}

func (emptyEvents) PurgeExpired(context.Context, time.Time) (repox.PurgeResult, error) {
	return repox.PurgeResult{}, nil
}

type emptyAvailabilities struct {
	availability.AvailabilityRepository
}

func (emptyAvailabilities) List(context.Context, string, repox.ListInput) ([]*availabilityv1.Availability, string, error) {
	return nil, "", nil
}

func (emptyAvailabilities) PurgeExpired(context.Context, time.Time) (repox.PurgeResult, error) {
	return repox.PurgeResult{}, nil
}

func TestPurgerSweep(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ago := func(days int) *timestamppb.Timestamp { return timestamppb.New(now.AddDate(0, 0, -days)) }

	calendars := newTable("",
		// Due, but a resource still names it: it waits.
		&row{name: "calendars/busy", deleted: ago(40)},
		// Due and nothing names it: purged.
		&row{name: "calendars/gone", deleted: ago(31)},
		// Deleted 10 days ago: kept, with expire_time stamped at day 30.
		&row{name: "calendars/recent", deleted: ago(10)},
		// Undeleted, but expire_time survived the restore: cleared, not purged.
		&row{name: "calendars/restored", expire: ago(1)},
	)
	resources := newTable("calendar",
		&row{name: "resources/kept", parent: "calendars/busy", deleted: ago(5)},
		// Due: purged.
		&row{name: "resources/court", parent: "calendars/other", deleted: ago(35)},
	)
	calendars.referrers = []*table{resources}
	p := NewPurger(Repositories{
		Resources:      fakeResources{table: resources},
		Calendars:      fakeCalendars{table: calendars},
		Events:         emptyEvents{},
		Availabilities: emptyAvailabilities{},
	}, 30*24*time.Hour, nil)
	p.now = func() time.Time { return now }

	n, err := p.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged %d records, want 2", n)
	}
	for tbl, want := range map[*table][]string{
		calendars: {"calendars/gone"},
		resources: {"resources/court"},
	} {
		if !slices.Equal(tbl.purged, want) {
			t.Errorf("purged %v, want %v", tbl.purged, want)
		}
	}
	if r := calendars.rows["calendars/busy"]; r == nil {
		t.Error("a calendar a resource still names was purged")
	}
	if got := calendars.rows["calendars/recent"].expire; got == nil || !got.AsTime().Equal(now.AddDate(0, 0, 20)) {
		t.Errorf("recent expire_time = %v, want delete_time + 30 days", got)
	}
	if got := calendars.rows["calendars/restored"]; got == nil || got.expire != nil {
		t.Errorf("restored calendar = %+v, want kept with expire_time cleared", got)
	}

	// A record stamped under another retention is restamped, not purged by
	// its stale expire_time.
	calendars.rows["calendars/recent"].expire = ago(1)
	if _, err := p.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := calendars.rows["calendars/recent"]; got == nil || !got.expire.AsTime().Equal(now.AddDate(0, 0, 20)) {
		t.Errorf("recent after a stale expire_time = %+v, want kept and restamped", got)
	}

	// A second sweep finds nothing new to write.
	etag := calendars.rows["calendars/recent"].etag
	if _, err := p.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calendars.rows["calendars/recent"].etag != etag {
		t.Error("a second sweep rewrote an expire_time already correct")
	}
}
