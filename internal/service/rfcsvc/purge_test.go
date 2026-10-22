// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/organization"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/orgunit"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// row is one stored record in a fake table: its name, the parent it names
// (organization or organizational_unit), and its AIP-164 times.
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
		return types.ErrNotFound
	case etag != "" && etag != r.etag:
		return types.ErrConflict
	}
	r.expire, r.etag = at, r.etag+"'"
	return nil
}

func (t *table) purge(name string) error {
	if _, ok := t.rows[name]; !ok {
		return types.ErrNotFound
	}
	delete(t.rows, name)
	t.purged = append(t.purged, name)
	return nil
}

type fakeOrganizations struct {
	organization.OrganizationRepository
	*table
}

func (f fakeOrganizations) List(_ context.Context, in repox.ListInput) ([]*organizationv1.Organization, string, error) {
	var out []*organizationv1.Organization
	for _, r := range f.list(in) {
		out = append(out, &organizationv1.Organization{Name: r.name, Etag: r.etag, DeleteTime: r.deleted, ExpireTime: r.expire})
	}
	return out, "", nil
}

func (f fakeOrganizations) UpdateOutputOnly(_ context.Context, m *organizationv1.Organization, _ []string) (*organizationv1.Organization, error) {
	return m, f.expire(m.GetName(), m.GetEtag(), m.GetExpireTime())
}

func (f fakeOrganizations) Purge(_ context.Context, name string) error { return f.purge(name) }

type fakeUnits struct {
	orgunit.OrganizationalUnitRepository
	*table
}

func (f fakeUnits) List(_ context.Context, in repox.ListInput) ([]*organizational_unitv1.OrganizationalUnit, string, error) {
	var out []*organizational_unitv1.OrganizationalUnit
	for _, r := range f.list(in) {
		out = append(out, &organizational_unitv1.OrganizationalUnit{Name: r.name, Etag: r.etag, Organization: r.parent, DeleteTime: r.deleted, ExpireTime: r.expire})
	}
	return out, "", nil
}

func (f fakeUnits) UpdateOutputOnly(_ context.Context, m *organizational_unitv1.OrganizationalUnit, _ []string) (*organizational_unitv1.OrganizationalUnit, error) {
	return m, f.expire(m.GetName(), m.GetEtag(), m.GetExpireTime())
}

func (f fakeUnits) Purge(_ context.Context, name string) error { return f.purge(name) }

type fakeResources struct {
	resource.ResourceRepository
	*table
}

func (f fakeResources) List(_ context.Context, in repox.ListInput) ([]*resourcev1.Resource, string, error) {
	var out []*resourcev1.Resource
	for _, r := range f.list(in) {
		out = append(out, &resourcev1.Resource{Name: r.name, Etag: r.etag, OrganizationalUnit: r.parent, DeleteTime: r.deleted, ExpireTime: r.expire})
	}
	return out, "", nil
}

func (f fakeResources) UpdateOutputOnly(_ context.Context, m *resourcev1.Resource, _ []string) (*resourcev1.Resource, error) {
	return m, f.expire(m.GetName(), m.GetEtag(), m.GetExpireTime())
}

func (f fakeResources) Purge(_ context.Context, name string) error { return f.purge(name) }

// The calendar tree is empty here; its fakes only list nothing.
type emptyCalendars struct{ calendar.CalendarRepository }

func (emptyCalendars) List(context.Context, repox.ListInput) ([]*calendarv1.Calendar, string, error) {
	return nil, "", nil
}

type emptyEvents struct{ event.EventRepository }

func (emptyEvents) List(context.Context, string, repox.ListInput) ([]*eventv1.Event, string, error) {
	return nil, "", nil
}

type emptyAvailabilities struct {
	availability.AvailabilityRepository
}

func (emptyAvailabilities) List(context.Context, string, repox.ListInput) ([]*availabilityv1.Availability, string, error) {
	return nil, "", nil
}

func TestPurgerSweep(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ago := func(days int) *timestamppb.Timestamp { return timestamppb.New(now.AddDate(0, 0, -days)) }

	orgs := newTable("",
		// Due, but its site still has a row: it waits.
		&row{name: "organizations/busy", deleted: ago(40)},
		// Due and nothing names it: purged.
		&row{name: "organizations/gone", deleted: ago(31)},
		// Deleted 10 days ago: kept, with expire_time stamped at day 30.
		&row{name: "organizations/recent", deleted: ago(10)},
		// Undeleted, but expire_time survived the restore: cleared, not purged.
		&row{name: "organizations/restored", expire: ago(1)},
	)
	units := newTable("organization",
		&row{name: "organizationalUnits/kept", parent: "organizations/busy", deleted: ago(5)},
		// Due, and its resource is due too: the resource goes first, then it.
		&row{name: "organizationalUnits/old", parent: "organizations/other", deleted: ago(35)},
	)
	resources := newTable("organizational_unit",
		&row{name: "resources/court", parent: "organizationalUnits/old", deleted: ago(35)},
	)
	p := NewPurger(Repositories{
		Organizations:       fakeOrganizations{table: orgs},
		OrganizationalUnits: fakeUnits{table: units},
		Resources:           fakeResources{table: resources},
		Calendars:           emptyCalendars{},
		Events:              emptyEvents{},
		Availabilities:      emptyAvailabilities{},
	}, 30*24*time.Hour)
	p.now = func() time.Time { return now }

	n, err := p.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("purged %d records, want 3", n)
	}
	for tbl, want := range map[*table][]string{
		orgs:      {"organizations/gone"},
		units:     {"organizationalUnits/old"},
		resources: {"resources/court"},
	} {
		if !slices.Equal(tbl.purged, want) {
			t.Errorf("purged %v, want %v", tbl.purged, want)
		}
	}
	if r := orgs.rows["organizations/busy"]; r == nil {
		t.Error("an organization a site still names was purged")
	}
	if got := orgs.rows["organizations/recent"].expire; got == nil || !got.AsTime().Equal(now.AddDate(0, 0, 20)) {
		t.Errorf("recent expire_time = %v, want delete_time + 30 days", got)
	}
	if got := orgs.rows["organizations/restored"]; got == nil || got.expire != nil {
		t.Errorf("restored organization = %+v, want kept with expire_time cleared", got)
	}

	// A second sweep finds nothing new to write.
	etag := orgs.rows["organizations/recent"].etag
	if _, err := p.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if orgs.rows["organizations/recent"].etag != etag {
		t.Error("a second sweep rewrote an expire_time already correct")
	}
}
