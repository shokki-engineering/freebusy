// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"google.golang.org/grpc/codes"
)

// versionedStore is a memStore with a version the test moves by hand, as a
// write through the generated repositories would, and counters of the
// static reads the cache should save.
type versionedStore struct {
	*memStore
	version       atomic.Int64
	resourceReads atomic.Int64
	// claimed counts the resources whose claims were read.
	claimed atomic.Int64
	// slowInsert delays every InsertHold, so concurrent callers all plan
	// before any of them writes, as they do against a real database.
	slowInsert time.Duration
}

func (v *versionedStore) InputVersion(context.Context) (string, error) {
	return strconv.FormatInt(v.version.Load(), 10), nil
}

func (v *versionedStore) Claims(ctx context.Context, resources []string, window engine.Span, now time.Time) (Claims, error) {
	v.claimed.Add(int64(len(resources)))
	return v.memStore.Claims(ctx, resources, window, now)
}

func (v *versionedStore) Resources(ctx context.Context, q ResourceQuery) ([]ResourceRecord, error) {
	v.resourceReads.Add(1)
	return v.memStore.Resources(ctx, q)
}

func (v *versionedStore) InsertHold(ctx context.Context, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	time.Sleep(v.slowInsert)
	return v.memStore.InsertHold(ctx, h)
}

// garage is n interchangeable bays, open 06:00-23:00.
func garage(t *testing.T, n int) (*versionedStore, func() time.Time) {
	t.Helper()
	m := newMemStore()
	clock := hr(-24)
	now := func() time.Time { return clock }
	m.clock = now
	m.avail["calendars/bays"] = []*availabilityv1.Availability{opens(hr(6), hr(23), "FREQ=DAILY")}
	for i := range n {
		m.resources = append(m.resources, ResourceRecord{Resource: &resourcev1.Resource{
			Name: fmt.Sprintf("resources/bay-%03d", i), Calendar: "calendars/bays", ResourceTypes: []string{"parking-bay"},
		}})
	}
	return &versionedStore{memStore: m}, now
}

// Sixty drivers ask for the same two hours at once at a garage of fifty
// interchangeable bays. Every bay sells, and the ten left over are told the
// garage is full -- none is answered ABORTED while bays are free.
func TestConcurrentHoldsSpreadAcrossEqualResources(t *testing.T) {
	store, now := garage(t, 50)
	store.slowInsert = 2 * time.Millisecond
	srv := NewServer(store, WithClock(now), WithSeed(7))
	var ok, full, other atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
				Hold: &allocationpbv1.Hold{Window: win(8, 10), Quantity: 1},
			})
			if err == nil {
				ok.Add(1)
				return
			}
			if c, r := reasonOf(t, err); c == codes.FailedPrecondition && r == allocationpbv1svc.ReasonCapacityExhausted {
				full.Add(1)
				return
			}
			t.Logf("other error: %v", err)
			other.Add(1)
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 50 || full.Load() != 10 || other.Load() != 0 {
		t.Fatalf("got %d held, %d full, %d other; want 50, 10, 0", ok.Load(), full.Load(), other.Load())
	}
}

// A hold outside opening hours says so, rather than that the garage is full.
func TestCreateHoldOutsideAvailability(t *testing.T) {
	store, now := garage(t, 3)
	srv := NewServer(store, WithClock(now))
	_, err := srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: win(22, 24), Quantity: 1},
	})
	if c, r := reasonOf(t, err); c != codes.FailedPrecondition || r != "OUTSIDE_AVAILABILITY" {
		t.Fatalf("outside hours: %v %q (%v)", c, r, err)
	}
	// Inside hours but taken: still CAPACITY_EXHAUSTED.
	for range 3 {
		if _, err := srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
			Hold: &allocationpbv1.Hold{Window: win(9, 10)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: win(9, 10)},
	})
	if c, r := reasonOf(t, err); c != codes.FailedPrecondition || r != allocationpbv1svc.ReasonCapacityExhausted {
		t.Fatalf("taken: %v %q", c, r)
	}
}

// Static inputs are read once per version, read again when it moves, and a
// hold never acts on a stale copy even when the version failed to move.
func TestStaticInputsCachedByVersion(t *testing.T) {
	store, now := garage(t, 2)
	srv := NewServer(store, WithClock(now))
	check := func() *allocationpbv1.CheckAvailabilityResponse {
		t.Helper()
		res, err := srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Window: win(9, 10), Quantity: 1})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	check()
	check()
	if n := store.resourceReads.Load(); n != 1 {
		t.Fatalf("resources read %d times for two checks under one version, want 1", n)
	}

	// Close the garage. With the version moved the next check sees it.
	store.mu.Lock()
	store.avail["calendars/bays"] = []*availabilityv1.Availability{opens(hr(12), hr(13), "FREQ=DAILY")}
	store.mu.Unlock()
	store.version.Add(1)
	if check().GetBookable() {
		t.Fatal("a check after the version moved still saw the old opening hours")
	}

	// Open again, but the version does not move (a write another instance
	// failed to announce). Reads may be stale; a hold may not.
	store.mu.Lock()
	store.avail["calendars/bays"] = []*availabilityv1.Availability{opens(hr(6), hr(23), "FREQ=DAILY")}
	store.mu.Unlock()
	if _, err := srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: win(9, 10)},
	}); err != nil {
		t.Fatalf("a hold planned from stale inputs was refused instead of re-read: %v", err)
	}
	// And closed again, unannounced: the hold is refused, not taken.
	store.mu.Lock()
	store.avail["calendars/bays"] = []*availabilityv1.Availability{opens(hr(12), hr(13), "FREQ=DAILY")}
	store.mu.Unlock()
	_, err := srv.CreateHold(ctx(), &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: win(15, 16)},
	})
	if c, r := reasonOf(t, err); c != codes.FailedPrecondition || r != "OUTSIDE_AVAILABILITY" {
		t.Fatalf("a hold on a closed garage, planned from a stale cache: %v %q", c, r)
	}
}

// Admission control turns an evaluation away with ENGINE_BUSY when every slot
// stays taken past the wait.
func TestAdmissionTurnsAwayWhenBusy(t *testing.T) {
	store, now := garage(t, 1)
	srv := NewServer(store, WithClock(now), WithAdmission(1, 10*time.Millisecond))
	release, err := srv.admit.acquire(ctx(), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Window: win(9, 10)})
	if c, r := reasonOf(t, err); c != codes.ResourceExhausted || r != allocationpbv1svc.ReasonEngineBusy {
		t.Fatalf("busy: %v %q", c, r)
	}
	release()
	if _, err := srv.CheckAvailability(ctx(), &allocationpbv1.CheckAvailabilityRequest{Window: win(9, 10)}); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

// Search beyond the read model stops once it has the page and one match
// more, and its pages are the ones a search of every resource would draw.
func TestSearchStopsAtPage(t *testing.T) {
	store, now := garage(t, 4*searchChunk)
	srv := NewServer(store, WithClock(now))
	var got []string
	token := ""
	for page := 0; ; page++ {
		before := store.claimed.Load()
		res, err := srv.SearchAvailability(ctx(), &allocationpbv1.SearchAvailabilityRequest{
			Window: win(9, 10), PageSize: 100, PageToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 {
			// 100 + 1 matches are in the first two chunks.
			if n := store.claimed.Load() - before; n > 2*searchChunk {
				t.Fatalf("first page read the bookings of %d resources, want at most %d", n, 2*searchChunk)
			}
		}
		for _, m := range res.GetMatches() {
			got = append(got, m.GetResource())
		}
		if token = res.GetNextPageToken(); token == "" {
			break
		}
	}
	if len(got) != 4*searchChunk || !slices.IsSorted(got) {
		t.Fatalf("paged %d matches, want all %d in order", len(got), 4*searchChunk)
	}
}
