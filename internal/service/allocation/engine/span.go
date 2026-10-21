// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package engine is the availability engine's arithmetic: what was offered, what
// has been taken, and what is left, for any bookable thing over any window.
//
// It knows nothing about rooms, courts or car parks, and nothing about storage.
// Callers hand it plain values -- a resource's RFC 7953 availability, the spans
// already claimed on it, the policy it was published with -- and get spans back.
// Every vertical fact the engine needs arrives as one of those values, which is
// what keeps the same code answering "is room 12 free on the 3rd" and "which of
// the six padel courts is free at 18:00".
//
// All spans are half-open, [Start, End), and compared as instants. Time zones
// matter only where a rule is expressed in local time (an RRULE, a calendar
// day), and those functions take the *time.Location explicitly.
package engine

import (
	"sort"
	"time"
)

// Span is one half-open interval of time, [Start, End). A zero Start or End is
// not "unbounded": callers that mean unbounded clip against the window first.
type Span struct {
	Start time.Time
	End   time.Time
}

// Empty reports whether the span covers no time.
func (s Span) Empty() bool { return !s.End.After(s.Start) }

// Duration is the span's length, zero when empty.
func (s Span) Duration() time.Duration {
	if s.Empty() {
		return 0
	}
	return s.End.Sub(s.Start)
}

// Overlaps reports whether the two spans share any instant. Touching endpoints
// do not overlap, so a booking ending at 10:00 and one starting at 10:00 can
// both stand.
func (s Span) Overlaps(o Span) bool {
	return s.Start.Before(o.End) && o.Start.Before(s.End)
}

// Contains reports whether o lies entirely inside s.
func (s Span) Contains(o Span) bool {
	return !o.Start.Before(s.Start) && !o.End.After(s.End)
}

// Intersect returns the overlap of two spans, empty when they do not overlap.
func (s Span) Intersect(o Span) Span {
	out := Span{Start: maxTime(s.Start, o.Start), End: minTime(s.End, o.End)}
	if out.Empty() {
		return Span{}
	}
	return out
}

// Spans is a set of time held in canonical form: sorted by start, non-empty,
// with no two members overlapping or touching. Every operation below returns
// canonical output; Normalize is how arbitrary input gets there.
type Spans []Span

// Normalize sorts, drops empty spans, and merges overlapping or touching ones.
// It copies, so the input is left untouched.
func Normalize(in []Span) Spans {
	out := make(Spans, 0, len(in))
	for _, s := range in {
		if !s.Empty() {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	merged := out[:0]
	for _, s := range out {
		if n := len(merged); n > 0 && !s.Start.After(merged[n-1].End) {
			if s.End.After(merged[n-1].End) {
				merged[n-1].End = s.End
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// Union returns the time in either set.
func (s Spans) Union(o Spans) Spans {
	all := make([]Span, 0, len(s)+len(o))
	all = append(all, s...)
	all = append(all, o...)
	return Normalize(all)
}

// Intersect returns the time in both sets.
func (s Spans) Intersect(o Spans) Spans {
	var out Spans
	i, j := 0, 0
	for i < len(s) && j < len(o) {
		if x := s[i].Intersect(o[j]); !x.Empty() {
			out = append(out, x)
		}
		// Advance whichever ends first; the other may still overlap the next.
		if s[i].End.Before(o[j].End) {
			i++
		} else {
			j++
		}
	}
	return out
}

// Subtract returns the time in s that is not in o.
func (s Spans) Subtract(o Spans) Spans {
	var out Spans
	j := 0
	for _, cur := range s {
		// Skip removals that end before this span starts; they cannot touch it
		// or anything after it, since both sets are sorted.
		for j < len(o) && !o[j].End.After(cur.Start) {
			j++
		}
		k := j
		for k < len(o) && o[k].Start.Before(cur.End) {
			if o[k].Start.After(cur.Start) {
				out = append(out, Span{Start: cur.Start, End: o[k].Start})
			}
			if o[k].End.After(cur.Start) {
				cur.Start = o[k].End
			}
			if !cur.End.After(cur.Start) {
				break
			}
			k++
		}
		if cur.End.After(cur.Start) {
			out = append(out, cur)
		}
	}
	return out
}

// Clip returns the part of s inside w.
func (s Spans) Clip(w Span) Spans {
	return s.Intersect(Spans{w})
}

// Covers reports whether a single member of s contains the whole of w. Because
// the set is canonical (touching spans are merged), one member is enough:
// coverage never needs stitching across two.
func (s Spans) Covers(w Span) bool {
	_, ok := s.Containing(w)
	return ok
}

// Containing returns the member of s that contains w, if any.
func (s Spans) Containing(w Span) (Span, bool) {
	if w.Empty() {
		return Span{}, false
	}
	// First member ending after w starts; only it can contain w.
	i := sort.Search(len(s), func(i int) bool { return s[i].End.After(w.Start) })
	if i < len(s) && s[i].Contains(w) {
		return s[i], true
	}
	return Span{}, false
}

// AnyOverlap reports whether any member of s overlaps w.
func (s Spans) AnyOverlap(w Span) bool {
	i := sort.Search(len(s), func(i int) bool { return s[i].End.After(w.Start) })
	return i < len(s) && s[i].Start.Before(w.End)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
