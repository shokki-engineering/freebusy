package engine

import (
	"errors"
	"sort"
	"time"
)

// Reason is why one resource cannot take a window. It mirrors
// freebusy.allocation.v1.UnbookableReasonCode so the engine stays free of
// generated types; the service layer maps one onto the other.
type Reason int

const (
	ReasonNone Reason = iota
	ReasonNoCapacity
	ReasonOutsideAvailability
	ReasonHeld
	ReasonBooked
	ReasonWindowTooShort
	ReasonWindowTooLong
	ReasonLeadTime
	ReasonResourceInactive
)

// Policy is the booking rules a resource was published with, read from its
// RFC 9073 STRUCTURED-DATA. Zero fields impose nothing, so a resource that
// declares no policy accepts any window it is free for.
type Policy struct {
	// MinDuration is the shortest window accepted: a minimum stay, a minimum
	// court booking.
	MinDuration time.Duration
	// MaxDuration is the longest window accepted.
	MaxDuration time.Duration
	// MinNotice is how far ahead of its start a window must be booked.
	MinNotice time.Duration
	// MaxAdvance is how far ahead bookings open.
	MaxAdvance time.Duration
}

// Check returns the first rule window breaks at instant now, or ReasonNone.
func (p Policy) Check(window Span, now time.Time) Reason {
	d := window.Duration()
	switch {
	case p.MinDuration > 0 && d < p.MinDuration:
		return ReasonWindowTooShort
	case p.MaxDuration > 0 && d > p.MaxDuration:
		return ReasonWindowTooLong
	case p.MinNotice > 0 && window.Start.Before(now.Add(p.MinNotice)):
		return ReasonLeadTime
	case p.MaxAdvance > 0 && window.Start.After(now.Add(p.MaxAdvance)):
		return ReasonLeadTime
	}
	return ReasonNone
}

// Resource is everything the engine knows about one bookable thing over the
// window being asked about. Callers fill it from storage; the engine never
// reads anything else.
type Resource struct {
	// Name is the RFC 9073 resource name, "resources/{resource}".
	Name string
	// Inactive marks a soft-deleted resource, or one otherwise refusing
	// bookings. It is still counted in totals -- it exists -- but is never free.
	Inactive bool
	// Offered is the time its availability puts on offer, from Offered.
	Offered Spans
	// Held is time claimed by live holds.
	Held Spans
	// Booked is time taken by confirmed bookings: converted holds and opaque
	// events on the resource's calendar.
	Booked Spans
	// Policy is the resource's booking rules.
	Policy Policy
}

// Free returns the resource's free time: offered, less held, less booked.
func (r Resource) Free() Spans {
	if r.Inactive {
		return nil
	}
	return r.Offered.Subtract(r.Held).Subtract(r.Booked)
}

// Why returns the reason the resource cannot take window, or ReasonNone when it
// can. Ordered from the most permanent cause to the most transient, so the
// reason reported is the one worth acting on: a court that is both closed and
// held should say "closed", since waiting for the hold to lapse will not help.
func (r Resource) Why(window Span, now time.Time) Reason {
	switch {
	case r.Inactive:
		return ReasonResourceInactive
	case !r.Offered.Covers(window):
		return ReasonOutsideAvailability
	}
	if p := r.Policy.Check(window, now); p != ReasonNone {
		return p
	}
	switch {
	case r.Booked.AnyOverlap(window):
		return ReasonBooked
	case r.Held.AnyOverlap(window):
		return ReasonHeld
	}
	return ReasonNone
}

// Granularity is how an availability answer is sliced.
type Granularity int

const (
	GranularityAuto Granularity = iota
	GranularitySlot
	GranularityDay
)

// DefaultSlot is the slot length used when SLOT granularity is asked for
// without one.
const DefaultSlot = time.Hour

// MaxBuckets caps one answer. A year of 15-minute slots is 35,040 buckets, each
// multiplied by every candidate resource; past this the caller wants a coarser
// granularity, not a slower answer.
const MaxBuckets = 5000

// ErrTooManyBuckets reports a window and granularity that would exceed
// MaxBuckets.
var ErrTooManyBuckets = errors.New("engine: window and granularity produce too many buckets")

// Buckets slices window by granularity.
//
// SLOT cuts consecutive slots of length slot from the window's start, dropping a
// trailing partial slot: half a padel booking is not a booking.
//
// DAY cuts one bucket per local day, anchored at the window's own start time of
// day rather than at midnight. A stay asked "15:00 on the 1st to 11:00 on the
// 5th" means four nights running 15:00 to 15:00, the last cut short at 11:00; a
// car park asked "midnight to midnight" gets calendar days. Same code, and the
// engine never has to know what a check-in time is. Steps are taken on the
// local calendar, so a 23-hour DST day is still one bucket.
//
// AUTO picks DAY for windows longer than 48 hours and SLOT below that.
func Buckets(window Span, g Granularity, slot time.Duration, loc *time.Location) ([]Span, error) {
	if window.Empty() {
		return nil, nil
	}
	if g == GranularityAuto {
		g = GranularitySlot
		if window.Duration() > 48*time.Hour {
			g = GranularityDay
		}
	}
	if g == GranularitySlot {
		if slot <= 0 {
			slot = DefaultSlot
		}
		n := int(window.Duration() / slot)
		if n > MaxBuckets {
			return nil, ErrTooManyBuckets
		}
		out := make([]Span, 0, n)
		for i := range n {
			s := window.Start.Add(time.Duration(i) * slot)
			out = append(out, Span{Start: s, End: s.Add(slot)})
		}
		return out, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	start := window.Start.In(loc)
	var out []Span
	for i := 0; ; i++ {
		s := start.AddDate(0, 0, i)
		if !s.Before(window.End) {
			break
		}
		if len(out) >= MaxBuckets {
			return nil, ErrTooManyBuckets
		}
		e := start.AddDate(0, 0, i+1)
		if e.After(window.End) {
			e = window.End
		}
		out = append(out, Span{Start: s, End: e})
	}
	return out, nil
}

// Bucket is one slice of a computed answer.
type Bucket struct {
	Window Span
	// Free lists the resources free across the whole of Window, in input order.
	Free []string
	// Total is how many resources were considered.
	Total int
}

// Compute answers "how many are free" for every bucket.
//
// Each resource's free set is computed once and then probed per bucket, so the
// cost is one subtraction per resource plus one binary search per resource per
// bucket -- the bucket count, not the window length, is what scales it.
//
// Policy is not applied here. A bucket is a question about inventory, "is
// there a court at 18:00", and a minimum-booking rule belongs to the request
// that actually books, which Check sees.
func Compute(resources []Resource, buckets []Span) []Bucket {
	free := make([]Spans, len(resources))
	for i := range resources {
		free[i] = resources[i].Free()
	}
	out := make([]Bucket, len(buckets))
	for b, w := range buckets {
		out[b] = Bucket{Window: w, Total: len(resources)}
		for i := range resources {
			if free[i].Covers(w) {
				out[b].Free = append(out[b].Free, resources[i].Name)
			}
		}
	}
	return out
}

// Unbookable is one reason a check failed.
type Unbookable struct {
	Reason Reason
	// Resource is the resource it concerns, empty for a reason about the
	// request as a whole.
	Resource string
}

// CheckResult is the answer to "can quantity resources be taken for window".
type CheckResult struct {
	Bookable bool
	// Candidates are the free resources in the order the allocator would take
	// them. All of them, not just the first quantity: a caller can show
	// alternatives.
	Candidates []string
	// Reasons are populated only when Bookable is false: one request-level
	// NO_CAPACITY, then why each resource that was not free was refused.
	Reasons []Unbookable
}

// Check decides whether quantity resources can take window at instant now, and
// in what order the allocator would pick them.
func Check(resources []Resource, window Span, quantity int, now time.Time) CheckResult {
	if quantity < 1 {
		quantity = 1
	}
	var res CheckResult
	var refused []Unbookable
	for _, r := range resources {
		if why := r.Why(window, now); why != ReasonNone {
			refused = append(refused, Unbookable{Reason: why, Resource: r.Name})
		}
	}
	res.Candidates = Rank(resources, window, now)
	res.Bookable = len(res.Candidates) >= quantity
	if !res.Bookable {
		res.Reasons = append([]Unbookable{{Reason: ReasonNoCapacity}}, refused...)
	}
	return res
}

// Rank returns the resources that can take window, best first.
//
// "Best" is best fit: the resource whose free gap around the window is
// tightest. Taking a 1-hour booking out of a court that is free for exactly an
// hour leaves the court that is free all afternoon whole for the 3-hour
// booking that comes next; taking it from the long gap would split that gap
// into two that may each be too short to sell. Ties break by name, so the
// choice is deterministic and two identical requests pick identically.
func Rank(resources []Resource, window Span, now time.Time) []string {
	type fit struct {
		name string
		gap  time.Duration
	}
	var fits []fit
	for _, r := range resources {
		if r.Why(window, now) != ReasonNone {
			continue
		}
		gap, ok := r.Free().Containing(window)
		if !ok {
			continue
		}
		fits = append(fits, fit{name: r.Name, gap: gap.Duration()})
	}
	sort.SliceStable(fits, func(i, j int) bool {
		if fits[i].gap != fits[j].gap {
			return fits[i].gap < fits[j].gap
		}
		return fits[i].name < fits[j].name
	})
	out := make([]string, len(fits))
	for i, f := range fits {
		out[i] = f.name
	}
	return out
}
