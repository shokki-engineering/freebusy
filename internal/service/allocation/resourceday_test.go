// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDayTargets(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	p := NewDayProjection(nil, 30)
	p.now = func() time.Time { return now }
	claim := func(expire *timestamppb.Timestamp) *allocationpbv1.Allocation {
		return &allocationpbv1.Allocation{
			Resource:   "resources/r",
			StartTime:  timestamppb.New(now.Add(48 * time.Hour)),
			EndTime:    timestamppb.New(now.Add(50 * time.Hour)),
			ExpireTime: expire,
		}
	}
	live := timestamppb.New(now.Add(15 * time.Minute))

	// A live hold's claim changes nothing the read model holds.
	if tg := p.targetsOf([]repox.Change{{Type: typeAllocation, Op: repox.ChangeCreate, Record: claim(live)}}); !tg.empty() {
		t.Fatalf("a live claim touched %v", tg.resources)
	}
	// Converting it (expiry cleared) touches its day only.
	tg := p.targetsOf([]repox.Change{{Type: typeAllocation, Op: repox.ChangeUpdate,
		Record: &allocationpbv1.Allocation{Name: "a"}, Prior: claim(live)}})
	want := engine.Spans{{Start: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}}
	if got := tg.resources["resources/r"]; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("conversion touched %v, want %v", got, want)
	}
	// Deleting a kept claim touches its day too.
	if tg := p.targetsOf([]repox.Change{{Type: typeAllocation, Op: repox.ChangeDelete, Prior: claim(nil)}}); len(tg.resources["resources/r"]) != 1 {
		t.Fatalf("deleting a kept claim touched %v", tg.resources)
	}
	// A resource write re-projects the whole horizon; an event its calendar.
	tg = p.targetsOf([]repox.Change{
		{Type: typeResource, Name: "resources/x", Op: repox.ChangeUpdate},
		{Type: typeAvailability, Name: "calendars/c/availabilities/a", Op: repox.ChangeCreate},
	})
	if got := tg.resources["resources/x"]; len(got) != 1 || got[0] != p.Horizon(now) {
		t.Fatalf("resource write touched %v, want the horizon %v", got, p.Horizon(now))
	}
	if len(tg.calendars["calendars/c"]) != 1 {
		t.Fatalf("availability write touched calendars %v", tg.calendars)
	}
}

func TestAttributesOf(t *testing.T) {
	r := &resourcev1.Resource{StructuredData: []*resourcev1.StructuredData{
		{Value: &resourcev1.StructuredData_Text{Text: `{"max_occupancy": 2, "view": "city"}`}},
		{Value: &resourcev1.StructuredData_Text{Text: `{"view": "river"}`}},
		{Value: &resourcev1.StructuredData_Text{Text: `not json`}},
	}}
	f := attributesOf(r).GetFields()
	if f["view"].GetStringValue() != "river" || f["max_occupancy"].GetNumberValue() != 2 {
		t.Fatalf("attributes %v", f)
	}
	if attributesOf(&resourcev1.Resource{}) != nil {
		t.Fatal("a resource without structured data has attributes")
	}
}
