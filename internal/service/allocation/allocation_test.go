// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/rfc"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/genproto/googleapis/type/dayofweek"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/genproto/googleapis/type/month"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const club = "organizationalUnits/club"

// day is the Monday the fixtures are set on, in UTC.
var day = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func hr(h float64) time.Time { return day.Add(time.Duration(h * float64(time.Hour))) }

func win(a, b float64) *interval.Interval {
	return &interval.Interval{StartTime: timestamppb.New(hr(a)), EndTime: timestamppb.New(hr(b))}
}

// fixture is a padel club: three courts on one calendar open 08:00-22:00 every
// day, court-1 booked 18:00-19:00 by an ordinary calendar event. The clock is
// the Sunday before, so nothing is in the past.
type fixture struct {
	srv   *Server
	store *memStore
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: newMemStore(), clock: hr(-24)}
	now := func() time.Time { return f.clock }
	f.store.clock = now
	for _, n := range []string{"court-1", "court-2", "court-3"} {
		f.addResource(n, "")
	}
	f.store.avail["calendars/club"] = []*availabilityv1.Availability{opens(hr(8), hr(22), "FREQ=DAILY")}
	f.store.events["calendars/club-court-1"] = []*eventv1.Event{busy(hr(18), hr(19))}
	f.srv = NewServer(f.store, WithClock(now))
	return f
}

// addResource adds a resource on its own calendar, carrying profile as its
// booking STRUCTURED-DATA when non-empty.
func (f *fixture) addResource(id, profile string) {
	res := &resourcev1.Resource{
		Name:               "resources/" + id,
		Calendar:           "calendars/club-" + id,
		OrganizationalUnit: club,
		ResourceTypes:      []string{"padel-court"},
	}
	if profile != "" {
		res.StructuredData = []*resourcev1.StructuredData{{
			Schema: rfc.ProfileSchemaURI,
			Value:  &resourcev1.StructuredData_Text{Text: profile},
		}}
	}
	f.store.resources = append(f.store.resources, ResourceRecord{Resource: res})
}

// share copies the club's opening hours onto every court's calendar, the way a
// site publishes one pattern for many courts.
func (f *fixture) share() {
	for _, r := range f.store.resources {
		f.store.avail[r.Resource.GetCalendar()] = f.store.avail["calendars/club"]
	}
}

func ctx() context.Context { return context.Background() }

func reasonOf(t *testing.T, err error) (codes.Code, string) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status: %v", err)
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return st.Code(), info.GetReason()
		}
	}
	return st.Code(), ""
}

func TestComputeAvailabilitySlots(t *testing.T) {
	f := newFixture(t)
	f.share()
	res, err := f.srv.ComputeAvailability(ctx(), &allocationpbv1.ComputeAvailabilityRequest{
		Parent:       club,
		Window:       win(7, 10),
		Granularity:  allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_SLOT,
		SlotDuration: durationpb.New(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []int32{}
	for _, b := range res.GetBuckets() {
		got = append(got, b.GetFreeCount())
	}
	// Closed at 07:00, all three open after.
	if want := []int32{0, 3, 3}; !equal(got, want) {
		t.Fatalf("free counts %v, want %v", got, want)
	}
	if res.GetTotalCount() != 3 {
		t.Fatalf("total %d", res.GetTotalCount())
	}

	res, err = f.srv.ComputeAvailability(ctx(), &allocationpbv1.ComputeAvailabilityRequest{
		Parent: club, Window: win(17, 20), SlotDuration: durationpb.New(time.Hour),
		Granularity: allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_SLOT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetBuckets()[1].GetFreeCount() != 2 {
		t.Fatalf("18:00 should have 2 free (court-1 booked), got %d", res.GetBuckets()[1].GetFreeCount())
	}
}

func TestCheckAvailabilityExplains(t *testing.T) {
	f := newFixture(t)
	f.share()
	res, err := f.srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{
		Parent: club, Window: win(18, 19), Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetBookable() || res.GetFreeCount() != 2 {
		t.Fatalf("bookable=%v free=%d", res.GetBookable(), res.GetFreeCount())
	}
	got := map[string]allocationpbv1.UnbookableReasonCode{}
	for _, r := range res.GetReasons() {
		got[r.GetResource()] = r.GetCode()
	}
	if got[""] != allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_NO_CAPACITY ||
		got["resources/court-1"] != allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_BOOKED {
		t.Fatalf("reasons %v", got)
	}
}

func TestCreateHoldAllocatesAndExhausts(t *testing.T) {
	f := newFixture(t)
	f.share()
	h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Parent: club, Hold: &allocationpbv1.Hold{Window: win(19, 20), Quantity: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Best fit: court-1 is free only 19:00-22:00, so it goes first.
	if len(h.GetResources()) != 2 || h.GetResources()[0] != "resources/court-1" {
		t.Fatalf("resources %v", h.GetResources())
	}
	if h.GetState() != allocationpbv1.HoldState_HOLD_STATE_ACTIVE {
		t.Fatalf("state %v", h.GetState())
	}

	_, err = f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Parent: club, Hold: &allocationpbv1.Hold{Window: win(19, 20), Quantity: 2},
	})
	if c, r := reasonOf(t, err); c != codes.FailedPrecondition || r != allocationpbv1svc.ReasonCapacityExhausted {
		t.Fatalf("second hold: %v %q", c, r)
	}
}

func TestCreateHoldIdempotent(t *testing.T) {
	f := newFixture(t)
	f.share()
	req := &allocationpbv1.CreateHoldRequest{
		Parent: club, Hold: &allocationpbv1.Hold{Window: win(10, 11), IdempotencyKey: "cart-42"},
	}
	a, err := f.srv.CreateHold(ctx(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.srv.CreateHold(ctx(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.GetName() != b.GetName() || len(f.store.holds) != 1 {
		t.Fatalf("retry took a second hold: %s vs %s", a.GetName(), b.GetName())
	}
}

func TestCreateHoldReplansAfterLosingRace(t *testing.T) {
	f := newFixture(t)
	f.share()
	// Between the read and the write, someone else takes court-1 -- the court
	// best fit would have picked.
	f.store.beforeInsert = func() {
		_, err := f.store.InsertHold(ctx(), club, &allocationpbv1.Hold{
			Name: club + "/holds/rival", Resources: []string{"resources/court-1"}, Window: win(19, 20),
			ExpireTime: timestamppb.New(f.clock.Add(time.Hour)), State: allocationpbv1.HoldState_HOLD_STATE_ACTIVE,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Parent: club, Hold: &allocationpbv1.Hold{Window: win(19, 20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.GetResources()[0] == "resources/court-1" {
		t.Fatal("hold was given a court the rival already took")
	}
}

func TestHoldExpiryFreesWithoutSweeper(t *testing.T) {
	f := newFixture(t)
	f.share()
	h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Parent: club, Hold: &allocationpbv1.Hold{Window: win(10, 11), Quantity: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func() int32 {
		res, err := f.srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Parent: club, Window: win(10, 11)})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetFreeCount()
	}
	if n := check(); n != 0 {
		t.Fatalf("held courts free: %d", n)
	}
	f.clock = f.clock.Add(DefaultHoldTTL + time.Second)
	if n := check(); n != 3 {
		t.Fatalf("after expiry free = %d, want 3", n)
	}
	got, err := f.srv.GetHold(ctx(), &allocationpbv1.GetHoldRequest{Name: h.GetName()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetState() != allocationpbv1.HoldState_HOLD_STATE_EXPIRED {
		t.Fatalf("GetHold state %v, want EXPIRED before any sweep", got.GetState())
	}
	if _, err := f.srv.ExtendHold(ctx(), &allocationpbv1.ExtendHoldRequest{Name: h.GetName(), Duration: durationpb.New(time.Hour)}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("extending an expired hold: %v", err)
	}
}

func TestReleaseDeleteConvert(t *testing.T) {
	f := newFixture(t)
	f.share()
	take := func() *allocationpbv1.Hold {
		h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
			Parent: club, Hold: &allocationpbv1.Hold{Window: win(12, 13)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := take()
	if _, err := f.srv.DeleteHold(ctx(), &allocationpbv1.DeleteHoldRequest{Name: h.GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("deleting an active hold: %v", err)
	}
	for range 2 { // idempotent
		r, err := f.srv.ReleaseHold(ctx(), &allocationpbv1.ReleaseHoldRequest{Name: h.GetName()})
		if err != nil || r.GetState() != allocationpbv1.HoldState_HOLD_STATE_RELEASED {
			t.Fatalf("release: %v %v", r.GetState(), err)
		}
	}
	if _, err := f.srv.DeleteHold(ctx(), &allocationpbv1.DeleteHoldRequest{Name: h.GetName()}); err != nil {
		t.Fatalf("deleting a released hold: %v", err)
	}

	c := take()
	if _, err := f.srv.ConvertHold(ctx(), c.GetName()); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(24 * time.Hour) // long past any TTL
	res, err := f.srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Parent: club, Window: win(12, 13)})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetFreeCount() != 2 {
		t.Fatalf("converted hold should keep its court: free %d", res.GetFreeCount())
	}
	if _, err := f.srv.ReleaseHold(ctx(), &allocationpbv1.ReleaseHoldRequest{Name: c.GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("releasing a converted hold: %v", err)
	}
}

func TestPolicyFromStructuredData(t *testing.T) {
	f := newFixture(t)
	f.store.resources = nil
	f.addResource("court-9", `{"min_duration":"PT1H","min_notice":"PT2H"}`)
	res, err := f.srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Parent: club, Window: win(10, 10.5)})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetBookable() || res.GetReasons()[1].GetCode() != allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_WINDOW_TOO_SHORT {
		t.Fatalf("got %v", res.GetReasons())
	}
}

func TestDayBucketsInSiteZone(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip("no tzdata")
	}
	f := newFixture(t)
	f.store.resources = nil
	f.store.avail = map[string][]*availabilityv1.Availability{}
	f.store.events = map[string][]*eventv1.Event{}
	f.addResource("room-101", "")
	f.store.resources[0].TimeZone = "Asia/Kolkata"
	// A three-night stay, check-in 14:00 local.
	in := time.Date(2026, 11, 2, 14, 0, 0, 0, kolkata)
	out := time.Date(2026, 11, 5, 11, 0, 0, 0, kolkata)
	f.store.events["calendars/club-room-101"] = []*eventv1.Event{busy(in.AddDate(0, 0, 1), in.AddDate(0, 0, 2))}
	res, err := f.srv.ComputeAvailability(ctx(), &allocationpbv1.ComputeAvailabilityRequest{
		Parent: club,
		Window: &interval.Interval{StartTime: timestamppb.New(in), EndTime: timestamppb.New(out)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []int32
	for _, b := range res.GetBuckets() {
		got = append(got, b.GetFreeCount())
	}
	if want := []int32{1, 0, 1}; !equal(got, want) {
		t.Fatalf("nights %v, want %v", got, want)
	}
	if s := res.GetBuckets()[1].GetWindow().GetStartTime().AsTime().In(kolkata); s.Hour() != 14 {
		t.Fatalf("night 2 starts %v, want 14:00 local", s)
	}
}

func TestSearchRequiresQuantityPerSite(t *testing.T) {
	f := newFixture(t)
	f.share()
	other := &resourcev1.Resource{Name: "resources/solo", Calendar: "calendars/solo", OrganizationalUnit: "organizationalUnits/annex", ResourceTypes: []string{"padel-court"}}
	f.store.resources = append(f.store.resources, ResourceRecord{Resource: other})
	res, err := f.srv.SearchAvailability(ctx(), &allocationpbv1.SearchAvailabilityRequest{
		Parent: "organizations/acme", Window: win(10, 11), Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.GetMatches() {
		if m.GetOrganizationalUnit() == "organizationalUnits/annex" {
			t.Fatal("a site with one free court matched a request for two")
		}
	}
	if len(res.GetMatches()) != 3 {
		t.Fatalf("matches %d, want the club's 3", len(res.GetMatches()))
	}
}

func TestICalDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT1H": time.Hour, "P2D": 48 * time.Hour, "P1W": 7 * 24 * time.Hour,
		"P1DT2H30M": 26*time.Hour + 30*time.Minute, "": 0, "P": 0, "1h": 0, "-PT1H": 0,
	}
	for in, want := range cases {
		if got := icalDuration(in); got != want {
			t.Errorf("icalDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUnsupportedEventRuleBlocks(t *testing.T) {
	span := engine.Span{Start: hr(0), End: hr(48)}
	hourly := busy(hr(10), hr(11))
	hourly.Recurrence = &eventv1.Recurrence{Frequency: eventv1.Frequency_FREQUENCY_HOURLY}
	got := expandEvents([]*eventv1.Event{hourly}, time.UTC, span)
	if !got.Covers(engine.Span{Start: hr(10), End: hr(48)}) {
		t.Fatalf("unreadable recurring booking should block from its start: %v", got)
	}
	if _, err := engine.ParseRRule("FREQ=HOURLY", time.UTC); !errors.Is(err, engine.ErrUnsupportedRule) {
		t.Fatal(err)
	}
}

// A stored rule using BYSETPOS and BYMONTH expands like any other: the last
// weekday of each November, here Monday 2026-11-30, and nothing else.
func TestEventRulePartsExpand(t *testing.T) {
	ev := busy(time.Date(2025, 11, 28, 10, 0, 0, 0, time.UTC), time.Date(2025, 11, 28, 11, 0, 0, 0, time.UTC))
	var weekdays []*eventv1.WeekdayNum
	for _, d := range []dayofweek.DayOfWeek{dayofweek.DayOfWeek_MONDAY, dayofweek.DayOfWeek_TUESDAY,
		dayofweek.DayOfWeek_WEDNESDAY, dayofweek.DayOfWeek_THURSDAY, dayofweek.DayOfWeek_FRIDAY} {
		weekdays = append(weekdays, &eventv1.WeekdayNum{Day: d})
	}
	ev.Recurrence = &eventv1.Recurrence{
		Frequency:    eventv1.Frequency_FREQUENCY_YEARLY,
		Months:       []month.Month{month.Month_NOVEMBER},
		Weekdays:     weekdays,
		SetPositions: []int32{-1},
	}
	got := expandEvents([]*eventv1.Event{ev}, time.UTC, engine.Span{Start: hr(0), End: hr(24 * 30)})
	want := engine.Spans{{Start: hr(24*28 + 10), End: hr(24*28 + 11)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func equal(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestConvertIdempotentAndEndConversion(t *testing.T) {
	f := newFixture(t)
	f.share()
	h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{Parent: club, Hold: &allocationpbv1.Hold{Window: win(12, 13), Quantity: 3}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if c, err := f.srv.ConvertHold(ctx(), h.GetName()); err != nil || c.GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
			t.Fatalf("convert: %v %v", c.GetState(), err)
		}
	}
	free := func() int32 {
		res, err := f.srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Parent: club, Window: win(12, 13)})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetFreeCount()
	}
	if n := free(); n != 0 {
		t.Fatalf("converted: %d free", n)
	}
	for range 2 {
		if _, err := f.srv.EndConversion(ctx(), h.GetName(), f.clock); err != nil {
			t.Fatal(err)
		}
	}
	f.clock = f.clock.Add(time.Second)
	if n := free(); n != 3 {
		t.Fatalf("after EndConversion: %d free", n)
	}
	got, _ := f.srv.LookupHold(ctx(), h.GetName())
	if got.GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
		t.Fatalf("ended hold should stay CONVERTED, got %v", got.GetState())
	}
}

func TestConcurrentIdempotentCreatesTakeOneHold(t *testing.T) {
	f := newFixture(t)
	f.share()
	var wg sync.WaitGroup
	names := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() {
			h, err := f.srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
				Parent: club, Hold: &allocationpbv1.Hold{Window: win(10, 11), IdempotencyKey: "cart-7"},
			})
			names[i], errs[i] = h.GetName(), err
		})
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil || names[i] != names[0] {
			t.Fatalf("caller %d: %q %v", i, names[i], errs[i])
		}
	}
	if len(f.store.holds) != 1 {
		t.Fatalf("%d holds taken for one idempotency key", len(f.store.holds))
	}
}
