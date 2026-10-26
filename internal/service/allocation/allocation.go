// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/the-protobuf-project/runtime-go/telemetry"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DefaultHoldTTL is how long a new hold reserves its resources before lapsing.
// Long enough for a checkout page, short enough that an abandoned cart frees a
// court before the next person looks for one.
const DefaultHoldTTL = 15 * time.Minute

// MaxHoldExtension caps one ExtendHold. Extensions are measured from now, so
// repeated calls cannot accumulate past this; the cap stops a single call from
// turning a hold into an unpaid booking.
const MaxHoldExtension = 24 * time.Hour

// Server implements AllocationService and HoldService over a Store.
type Server struct {
	allocationpbv1.UnimplementedAllocationServiceServer
	allocationpbv1.UnimplementedHoldServiceServer

	store Store
	// now is the clock, swapped in tests.
	now func() time.Time
	// holdTTL is the lifetime of a new hold.
	holdTTL time.Duration
	// sweeps reports the hold sweeper's runs (metrics.go).
	sweeps sweepMetrics
	// statics caches converted static inputs per site (statics.go).
	statics *statics
	// admit bounds concurrent evaluations (admission.go).
	admit *admission
	// shuffle spreads concurrent holds across equally good resources.
	shuffle func(n int, swap func(i, j int))
	// meter is kept for options applied after WithMeter.
	meter telemetry.Meter
	// admitLimit and admitWait configure admit.
	admitLimit int
	admitWait  time.Duration
	// days maintains the availability read model; nil without one, when
	// every evaluation reads every resource in scope (readmodel.go).
	days *DayProjection
	// pg searches the read model's Postgres table; searcher, when set,
	// serves SearchAvailability's candidates instead (OpenSearch).
	pg       DaySearcher
	searcher DaySearcher
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the server's clock. For tests.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// WithHoldTTL sets how long new holds last.
func WithHoldTTL(d time.Duration) Option { return func(s *Server) { s.holdTTL = d } }

// WithMeter reports the hold sweeper's metrics through m. Without it they go
// nowhere.
func WithMeter(m telemetry.Meter) Option {
	return func(s *Server) { s.sweeps, s.meter = newSweepMetrics(m), m }
}

// WithAdmission bounds the availability evaluations -- search, compute,
// check, and a hold's choice of resources -- running at once to limit (the
// number of CPUs when <= 0). One that finds no slot within wait
// (DefaultAdmissionWait when <= 0) is turned away with ENGINE_BUSY.
func WithAdmission(limit int, wait time.Duration) Option {
	return func(s *Server) { s.admitLimit, s.admitWait = limit, wait }
}

// WithSeed makes the choice among equally good resources for a hold
// deterministic for a seed. For tests.
func WithSeed(seed uint64) Option {
	return func(s *Server) {
		var mu sync.Mutex
		r := rand.New(rand.NewPCG(seed, seed))
		s.shuffle = func(n int, swap func(i, j int)) {
			mu.Lock()
			defer mu.Unlock()
			r.Shuffle(n, swap)
		}
	}
}

// NewServer returns a Server over store.
func NewServer(store Store, opts ...Option) *Server {
	s := &Server{store: store, now: time.Now, holdTTL: DefaultHoldTTL, sweeps: newSweepMetrics(nil),
		statics: newStatics(), shuffle: rand.Shuffle}
	for _, o := range opts {
		o(s)
	}
	s.admit = newAdmission(s.admitLimit, s.admitWait, s.meter)
	return s
}

// ComputeAvailability buckets free resources over a window.
func (s *Server) ComputeAvailability(ctx context.Context, req *allocationpbv1.ComputeAvailabilityRequest) (*allocationpbv1.ComputeAvailabilityResponse, error) {
	var out *allocationpbv1.ComputeAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "ComputeAvailability", func(ctx context.Context) error {
		release, err := s.admit.acquire(ctx, "ComputeAvailability")
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		defer release()
		res, err := s.compute(ctx, req)
		out = res
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) compute(ctx context.Context, req *allocationpbv1.ComputeAvailabilityRequest) (*allocationpbv1.ComputeAvailabilityResponse, error) {
	window, err := spanOf(req.GetWindow())
	if err != nil {
		return nil, err
	}
	scope, restricted, err := s.scopeOf(ctx, req.GetResources(), req.GetFilter(), "filter")
	if err != nil {
		return nil, err
	}
	st := &inputs{zone: time.UTC}
	if !restricted || len(scope) > 0 {
		if st, err = s.gather(ctx, ResourceQuery{Names: scope}, window, s.now()); err != nil {
			return nil, err
		}
	}
	loc := st.zone
	if tz := req.GetTimeZone(); tz != "" {
		l, lerr := time.LoadLocation(tz)
		if lerr != nil {
			return nil, servicex.BadField("INVALID_TIME_ZONE", "time_zone",
				fmt.Sprintf("time_zone %q is not a time zone", tz),
				"Use an IANA zone name such as Europe/London or America/New_York, or leave it empty for the zone of the first resource's calendar")
		}
		loc = l
	}
	buckets, err := engine.Buckets(window, granularity(req.GetGranularity()), req.GetSlotDuration().AsDuration(), loc)
	if errors.Is(err, engine.ErrTooManyBuckets) {
		return nil, servicex.BadField("TOO_MANY_BUCKETS", "granularity",
			fmt.Sprintf("this window would be answered in more than %d buckets", engine.MaxBuckets),
			"Ask for a shorter window, a longer slot_duration, or granularity AVAILABILITY_GRANULARITY_DAY")
	}
	if err != nil {
		return nil, err
	}
	out := &allocationpbv1.ComputeAvailabilityResponse{TotalCount: int32(len(st.resources))}
	for _, b := range engine.Compute(st.resources, buckets) {
		pb := &allocationpbv1.AvailabilityBucket{
			Window:     intervalOf(b.Window),
			FreeCount:  int32(len(b.Free)),
			TotalCount: int32(b.Total),
		}
		if req.GetIncludeFreeResources() {
			pb.FreeResources = b.Free
		}
		out.Buckets = append(out.Buckets, pb)
	}
	return out, nil
}

// CheckAvailability decides whether a quantity can be held for an exact window.
func (s *Server) CheckAvailability(ctx context.Context, req *allocationpbv1.CheckAvailabilityRequest) (*allocationpbv1.CheckAvailabilityResponse, error) {
	var out *allocationpbv1.CheckAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "CheckAvailability", func(ctx context.Context) error {
		window, err := spanOf(req.GetWindow())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		release, err := s.admit.acquire(ctx, "CheckAvailability")
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		defer release()
		now := s.now()
		res, err := s.check(ctx, req, window, now)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = &allocationpbv1.CheckAvailabilityResponse{
			Bookable:           res.Bookable,
			FreeCount:          int32(len(res.Candidates)),
			CandidateResources: res.Candidates,
		}
		for _, r := range res.Reasons {
			out.Reasons = append(out.Reasons, &allocationpbv1.UnbookableReason{
				Code:     reasonCode(r.Reason),
				Message:  reasonMessage(r.Reason),
				Resource: r.Resource,
			})
		}
		return nil
	})
	return out, err
}

// check decides CheckAvailability: first over the candidates the read model
// offers, and over every resource in scope when they are not enough, so a
// refusal always carries the reason of each resource asked about.
func (s *Server) check(ctx context.Context, req *allocationpbv1.CheckAvailabilityRequest, window engine.Span, now time.Time) (engine.CheckResult, error) {
	quantity := max(int(req.GetQuantity()), 1)
	if err := validFilter(req.GetFilter(), "filter"); err != nil {
		return engine.CheckResult{}, err
	}
	cands, ok, err := s.modelCandidates(ctx, req.GetResources(), req.GetFilter(), window, now)
	if err != nil {
		return engine.CheckResult{}, err
	}
	if ok && len(cands) >= quantity {
		st, err := s.gather(ctx, ResourceQuery{Names: cands}, horizon(window), now)
		if err != nil {
			return engine.CheckResult{}, err
		}
		if res := engine.Check(st.resources, window, int(req.GetQuantity()), now); res.Bookable {
			return res, nil
		}
	}
	scope, restricted, err := s.scopeOf(ctx, req.GetResources(), req.GetFilter(), "filter")
	if err != nil {
		return engine.CheckResult{}, err
	}
	var resources []engine.Resource
	if !restricted || len(scope) > 0 {
		st, err := s.gather(ctx, ResourceQuery{Names: scope}, horizon(window), now)
		if err != nil {
			return engine.CheckResult{}, err
		}
		resources = st.resources
	}
	return engine.Check(resources, window, int(req.GetQuantity()), now), nil
}

// BatchComputeAvailability runs several computations in one call. One failing
// entry fails the batch: a partial answer with a hole in it would render as
// "nothing free" for whatever was missing.
func (s *Server) BatchComputeAvailability(ctx context.Context, req *allocationpbv1.BatchComputeAvailabilityRequest) (*allocationpbv1.BatchComputeAvailabilityResponse, error) {
	var out *allocationpbv1.BatchComputeAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "BatchComputeAvailability", func(ctx context.Context) error {
		release, err := s.admit.acquire(ctx, "BatchComputeAvailability")
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		defer release()
		out = &allocationpbv1.BatchComputeAvailabilityResponse{}
		for _, r := range req.GetRequests() {
			res, err := s.compute(ctx, r)
			if err != nil {
				return rpc.ToStatusErr(err)
			}
			out.Responses = append(out.Responses, res)
		}
		return nil
	})
	return out, err
}

// SearchAvailability finds the resources free across a window.
func (s *Server) SearchAvailability(ctx context.Context, req *allocationpbv1.SearchAvailabilityRequest) (*allocationpbv1.SearchAvailabilityResponse, error) {
	var out *allocationpbv1.SearchAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "SearchAvailability", func(ctx context.Context) error {
		window, err := spanOf(req.GetWindow())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if err := checkSearch(req); err != nil {
			return rpc.ToStatusErr(err)
		}
		release, err := s.admit.acquire(ctx, "SearchAvailability")
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		defer release()
		if s.days != nil && s.days.Covers(window, s.now()) {
			out, err = s.searchModel(ctx, req, window)
			return rpc.ToStatusErr(err)
		}
		if req.GetOrderBy() != "" || len(req.GetFacets()) > 0 {
			return rpc.ToStatusErr(servicex.BadField("WINDOW_BEYOND_HORIZON", "window",
				"order_by and facets are answered from search records, which reach only from yesterday to the search horizon ahead, and this window is outside them",
				"Search a window inside the horizon ([search] horizon_days), or leave order_by and facets empty"))
		}
		out, err = s.search(ctx, req, window)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// searchChunk is how many resources one step of a search beyond the read
// model evaluates together, reading their bookings in one batch.
const searchChunk = 64

// search answers SearchAvailability without the read model: every resource
// the search selects, in name order, evaluated a chunk at a time, stopping as
// soon as it has the page asked for and one match past it, which is all it
// needs to know whether there is a next page.
func (s *Server) search(ctx context.Context, req *allocationpbv1.SearchAvailabilityRequest, window engine.Span) (*allocationpbv1.SearchAvailabilityResponse, error) {
	now := s.now()
	limit, offset := rpc.PageBounds(req.GetPageSize(), req.GetPageToken())
	need := offset + limit + 1

	q := ResourceQuery{Types: req.GetResourceTypes()}
	if f := req.GetFilter(); f != "" {
		names, err := s.filterNames(ctx, nil, f, "filter")
		if err != nil || len(names) == 0 {
			return &allocationpbv1.SearchAvailabilityResponse{}, err
		}
		q.Names = names
	}
	statics, err := s.staticInputs(ctx, q, false)
	if err != nil {
		return nil, err
	}
	var all []*allocationpbv1.AvailabilityMatch
	for chunk := range slices.Chunk(statics, searchChunk) {
		if len(all) >= need {
			break
		}
		st, err := s.assemble(ctx, chunk, horizon(window), now, nil)
		if err != nil {
			return nil, err
		}
		for i, r := range st.resources {
			if r.Why(window, now) != engine.ReasonNone {
				continue
			}
			res := st.records[i].Resource
			all = append(all, &allocationpbv1.AvailabilityMatch{
				Resource: r.Name,
				Window:   intervalOf(window),
				// A copy: the record may be cached and shared.
				ResourceTypes: slices.Clone(res.GetResourceTypes()),
				DisplayName:   res.GetDisplayName(),
			})
		}
	}

	out := &allocationpbv1.SearchAvailabilityResponse{}
	if offset < len(all) {
		end := min(offset+limit, len(all))
		out.Matches = all[offset:end]
		if end < len(all) {
			out.NextPageToken = rpc.EncodeOffset(end)
		}
	}
	return out, nil
}

// spanOf validates and converts a request window. Both ends are required: an
// open-ended availability question has no finite answer.
func spanOf(iv *interval.Interval) (engine.Span, error) {
	if iv.GetStartTime() == nil || iv.GetEndTime() == nil {
		return engine.Span{}, servicex.BadField("WINDOW_INCOMPLETE", "window",
			"window needs both start_time and end_time",
			"Send both as RFC 3339 timestamps, e.g. start_time: \"2026-11-02T18:00:00Z\", end_time: \"2026-11-02T19:00:00Z\"")
	}
	w := engine.Span{Start: iv.GetStartTime().AsTime(), End: iv.GetEndTime().AsTime()}
	if w.Empty() {
		return engine.Span{}, servicex.BadField("WINDOW_BACKWARDS", "window.end_time",
			"window.end_time must be after window.start_time",
			"Swap them, or check the offsets: 18:00+01:00 is 17:00Z")
	}
	return w, nil
}

func intervalOf(s engine.Span) *interval.Interval {
	return &interval.Interval{StartTime: timestamppb.New(s.Start), EndTime: timestamppb.New(s.End)}
}

func granularity(g allocationpbv1.AvailabilityGranularity) engine.Granularity {
	switch g {
	case allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_SLOT:
		return engine.GranularitySlot
	case allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_DAY:
		return engine.GranularityDay
	}
	return engine.GranularityAuto
}

func reasonCode(r engine.Reason) allocationpbv1.UnbookableReasonCode {
	switch r {
	case engine.ReasonNoCapacity:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_NO_CAPACITY
	case engine.ReasonOutsideAvailability:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_OUTSIDE_AVAILABILITY
	case engine.ReasonHeld:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_HELD
	case engine.ReasonBooked:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_BOOKED
	case engine.ReasonWindowTooShort:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_WINDOW_TOO_SHORT
	case engine.ReasonWindowTooLong:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_WINDOW_TOO_LONG
	case engine.ReasonLeadTime:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_LEAD_TIME
	case engine.ReasonResourceInactive:
		return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_RESOURCE_INACTIVE
	}
	return allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_UNSPECIFIED
}

func reasonMessage(r engine.Reason) string {
	switch r {
	case engine.ReasonNoCapacity:
		return "fewer free resources than the quantity requested"
	case engine.ReasonOutsideAvailability:
		return "the window is outside the resource's published availability"
	case engine.ReasonHeld:
		return "a live hold covers the window"
	case engine.ReasonBooked:
		return "a confirmed booking covers the window"
	case engine.ReasonWindowTooShort:
		return "the window is shorter than the resource's minimum"
	case engine.ReasonWindowTooLong:
		return "the window is longer than the resource's maximum"
	case engine.ReasonLeadTime:
		return "the window is inside the notice period or beyond how far ahead bookings open"
	case engine.ReasonResourceInactive:
		return "the resource is not accepting bookings"
	}
	return ""
}
