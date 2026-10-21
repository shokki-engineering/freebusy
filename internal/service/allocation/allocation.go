package allocation

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/oh-tarnished/freebusy/internal/runtime/rpc"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
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
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the server's clock. For tests.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// WithHoldTTL sets how long new holds last.
func WithHoldTTL(d time.Duration) Option { return func(s *Server) { s.holdTTL = d } }

// NewServer returns a Server over store.
func NewServer(store Store, opts ...Option) *Server {
	s := &Server{store: store, now: time.Now, holdTTL: DefaultHoldTTL}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ComputeAvailability buckets a site's free resources over a window.
func (s *Server) ComputeAvailability(ctx context.Context, req *allocationpbv1.ComputeAvailabilityRequest) (*allocationpbv1.ComputeAvailabilityResponse, error) {
	var out *allocationpbv1.ComputeAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "ComputeAvailability", func(ctx context.Context) error {
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
	st, err := s.gather(ctx, ResourceQuery{
		OrganizationalUnits: []string{req.GetParent()},
		Names:               req.GetResources(),
	}, window, s.now())
	if err != nil {
		return nil, err
	}
	loc := st.zone
	if tz := req.GetTimeZone(); tz != "" {
		l, lerr := time.LoadLocation(tz)
		if lerr != nil {
			return nil, errorf(types.ErrInvalidArgument, "time_zone %q", tz)
		}
		loc = l
	}
	buckets, err := engine.Buckets(window, granularity(req.GetGranularity()), req.GetSlotDuration().AsDuration(), loc)
	if errors.Is(err, engine.ErrTooManyBuckets) {
		return nil, errorf(types.ErrInvalidArgument, "window yields more than %d buckets; use a coarser granularity", engine.MaxBuckets)
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
		now := s.now()
		st, err := s.gather(ctx, ResourceQuery{
			OrganizationalUnits: []string{req.GetParent()},
			Names:               req.GetResources(),
		}, horizon(window), now)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		res := engine.Check(st.resources, window, int(req.GetQuantity()), now)
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

// BatchComputeAvailability runs several computations in one call. One failing
// entry fails the batch: a partial answer with a hole in it would render as
// "nothing free" for whatever was missing.
func (s *Server) BatchComputeAvailability(ctx context.Context, req *allocationpbv1.BatchComputeAvailabilityRequest) (*allocationpbv1.BatchComputeAvailabilityResponse, error) {
	var out *allocationpbv1.BatchComputeAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "BatchComputeAvailability", func(ctx context.Context) error {
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

// SearchAvailability sweeps an operator's sites for resources free across a
// window. A site matches only if it has `quantity` free together, so a family
// asking for three rooms is never shown a site with one.
func (s *Server) SearchAvailability(ctx context.Context, req *allocationpbv1.SearchAvailabilityRequest) (*allocationpbv1.SearchAvailabilityResponse, error) {
	var out *allocationpbv1.SearchAvailabilityResponse
	err := rpc.Traced(ctx, "AllocationService", "SearchAvailability", func(ctx context.Context) error {
		window, err := spanOf(req.GetWindow())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		now := s.now()
		q := ResourceQuery{Types: req.GetResourceTypes()}
		if ous := req.GetOrganizationalUnits(); len(ous) > 0 {
			q.OrganizationalUnits = ous
		} else {
			q.Organization = req.GetParent()
		}
		st, err := s.gather(ctx, q, horizon(window), now)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		quantity := max(int(req.GetQuantity()), 1)

		// Group by site, keeping only sites with enough free together.
		bySite := map[string][]*allocationpbv1.AvailabilityMatch{}
		for i, r := range st.resources {
			if r.Why(window, now) != engine.ReasonNone {
				continue
			}
			res := st.records[i].Resource
			ou := res.GetOrganizationalUnit()
			bySite[ou] = append(bySite[ou], &allocationpbv1.AvailabilityMatch{
				Resource:           r.Name,
				OrganizationalUnit: ou,
				Window:             intervalOf(window),
				ResourceTypes:      res.GetResourceTypes(),
			})
		}
		sites := make([]string, 0, len(bySite))
		for ou, ms := range bySite {
			if len(ms) >= quantity {
				sites = append(sites, ou)
			}
		}
		sort.Strings(sites)
		var all []*allocationpbv1.AvailabilityMatch
		for _, ou := range sites {
			ms := bySite[ou]
			sort.Slice(ms, func(i, j int) bool { return ms[i].GetResource() < ms[j].GetResource() })
			all = append(all, ms...)
		}

		limit, offset := types.PageBounds(req.GetPageSize(), req.GetPageToken())
		out = &allocationpbv1.SearchAvailabilityResponse{}
		if offset < len(all) {
			end := min(offset+limit, len(all))
			out.Matches = all[offset:end]
			if end < len(all) {
				out.NextPageToken = types.EncodeOffset(end)
			}
		}
		return nil
	})
	return out, err
}

// spanOf validates and converts a request window. Both ends are required: an
// open-ended availability question has no finite answer.
func spanOf(iv *interval.Interval) (engine.Span, error) {
	if iv.GetStartTime() == nil || iv.GetEndTime() == nil {
		return engine.Span{}, errorf(types.ErrInvalidArgument, "window needs both start_time and end_time")
	}
	w := engine.Span{Start: iv.GetStartTime().AsTime(), End: iv.GetEndTime().AsTime()}
	if w.Empty() {
		return engine.Span{}, errorf(types.ErrInvalidArgument, "window end_time must be after start_time")
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
