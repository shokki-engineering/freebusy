// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"github.com/the-protobuf-project/runtime-go/ulid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// createAttempts bounds how often CreateHold re-plans after losing a race.
// Each loss means another caller took a resource between the read and the
// write; re-reading picks the next best one, or finds there is none left and
// reports exhaustion. Running out of attempts means the site is contended hard
// enough that the caller should hear about it.
const createAttempts = 6

// backoffBase is the first re-plan's maximum wait, doubling each attempt.
//
// The wait matters most on a store that settles races by claim-then-verify
// (Hasura): there, simultaneous claimants all see each other and all withdraw,
// and retrying in lockstep repeats the tie. A random wait spreads them out so
// one claim lands alone. Measured on DDN, 10 callers racing for one court with
// no wait left the court unsold in 16 runs of 20.
const backoffBase = 10 * time.Millisecond

// CreateHold takes a hold, choosing which resources it gets.
func (s *Server) CreateHold(ctx context.Context, req *allocationpbv1.CreateHoldRequest) (*allocationpbv1.Hold, error) {
	var out *allocationpbv1.Hold
	err := rpc.Traced(ctx, "HoldService", "CreateHold", func(ctx context.Context) error {
		h, err := s.createHold(ctx, req)
		out = h
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) createHold(ctx context.Context, req *allocationpbv1.CreateHoldRequest) (*allocationpbv1.Hold, error) {
	parent := req.GetParent()
	in := req.GetHold()
	window, err := spanOf(in.GetWindow())
	if err != nil {
		return nil, err
	}
	// prior returns the hold already taken under this request's idempotency
	// key, if any. Checked before every attempt, not just the first: a
	// concurrent caller with the same key may land between attempts, and its
	// claim on the resources then looks like a conflict -- or exhaustion --
	// rather than the duplicate it is.
	prior := func() (*allocationpbv1.Hold, error) {
		key := in.GetIdempotencyKey()
		if key == "" {
			return nil, nil
		}
		prev, err := s.store.FindHold(ctx, parent, key)
		switch {
		case err == nil:
			return s.authoritative(prev), nil
		case errors.Is(err, repox.ErrNotFound):
			return nil, nil
		}
		return nil, err
	}
	id := req.GetHoldId()
	switch {
	case id != "":
	case in.GetIdempotencyKey() != "":
		// Derived, not random: two concurrent calls with one key then race
		// for one name, the store's uniqueness picks a winner, and the loser
		// returns the winner's hold below. A random id would let both win.
		id = keyedID(parent, in.GetIdempotencyKey())
	default:
		id = ulid.Generate().String()
	}
	quantity := max(int(in.GetQuantity()), 1)
	// A hold replacing a booking leaves that booking's own claim out of
	// what it competes with, so a window overlapping it can keep its
	// resources.
	kept, err := s.replaced(ctx, parent, in.GetReplaces())
	if err != nil {
		return nil, err
	}
	// The resources the hold may take: its candidates, narrowed by its
	// filter. restricted with an empty scope is a filter nothing matches.
	scope, restricted, err := s.scopeOf(ctx, parent, in.GetCandidateResources(), in.GetCandidateFilter(), "hold.candidate_filter")
	if err != nil {
		return nil, err
	}

	fresh := false
	for attempt := 0; ; attempt++ {
		if prev, err := prior(); prev != nil || err != nil {
			return prev, err
		}
		now := s.now()
		picked, res, retry, err := s.plan(ctx, parent, scope, restricted, in.GetCandidateFilter(), window, quantity, now, fresh, kept)
		if err != nil {
			return nil, err
		}
		if retry {
			// The cached inputs disagreed with the database about what this
			// hold would take: plan again from the database.
			fresh = true
			continue
		}
		if picked == nil {
			if prev, err := prior(); prev != nil || err != nil {
				return prev, err
			}
			return nil, unbookable(parent, res, quantity)
		}
		hold := &allocationpbv1.Hold{
			Name:               parent + "/holds/" + id,
			Resources:          picked,
			Window:             intervalOf(window),
			CandidateResources: in.GetCandidateResources(),
			CandidateFilter:    in.GetCandidateFilter(),
			Quantity:           int32(quantity),
			ExpireTime:         timestamppb.New(now.Add(s.holdTTL)),
			State:              allocationpbv1.HoldState_HOLD_STATE_ACTIVE,
			IdempotencyKey:     in.GetIdempotencyKey(),
			Replaces:           in.GetReplaces(),
		}
		created, err := s.store.InsertHold(ctx, parent, hold)
		if err == nil {
			return created, nil
		}
		if errors.Is(err, repox.ErrAlreadyExists) {
			if prev, perr := prior(); prev != nil || perr != nil {
				return prev, perr
			}
		}
		if !errors.Is(err, repox.ErrConflict) || attempt+1 >= createAttempts {
			return nil, err
		}
		wait := time.Duration(rand.Int64N(int64(backoffBase << attempt)))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// plan chooses a hold's resources: from the candidates the read model says
// are free when there are enough of them -- a handful of resources read
// instead of the whole site -- and from every resource in scope when they
// fall short or the read model cannot answer, so a hold never depends on it
// being current, and a refusal always comes from the whole scope.
func (s *Server) plan(ctx context.Context, parent string, scope []string, restricted bool, filter string, window engine.Span, quantity int, now time.Time, fresh bool, kept map[string]engine.Spans) (picked []string, res engine.CheckResult, retry bool, err error) {
	if restricted && len(scope) == 0 {
		return nil, engine.Check(nil, window, quantity, now), false, nil
	}
	// A replacing hold competes without its booking's own claims, which the
	// read model counts as booked: plan it from the whole scope.
	if kept == nil {
		cands, ok, err := s.modelCandidates(ctx, parent, scope, filter, window, now)
		if err != nil {
			return nil, res, false, err
		}
		if ok && len(cands) >= quantity {
			picked, res, retry, err = s.pick(ctx, parent, cands, window, quantity, now, fresh, kept)
			if err != nil || retry || picked != nil {
				return picked, res, retry, err
			}
		}
	}
	return s.pick(ctx, parent, scope, window, quantity, now, fresh, kept)
}

// pick plans one CreateHold attempt under admission control: it gathers the
// site, checks the window, and chooses quantity resources, spreading
// concurrent callers across equally good ones (engine.Pick). nil picked with
// no error is a window that cannot be held, and res says why.
//
// From cached static inputs, a plan is checked against the database before it
// is used: the chosen resources' static inputs are read again and must match,
// and a refusal that a static input could explain is confirmed the same way.
// retry asks the caller to plan again with fresh set, so no write to a
// resource, its calendar or availability is ever hidden from a hold.
//
// kept is time a booking the hold replaces keeps on its resources, which the
// plan does not count as booked.
func (s *Server) pick(ctx context.Context, parent string, candidates []string, window engine.Span, quantity int, now time.Time, fresh bool, kept map[string]engine.Spans) (picked []string, res engine.CheckResult, retry bool, err error) {
	release, err := s.admit.acquire(ctx, "CreateHold")
	if err != nil {
		return nil, res, false, err
	}
	defer release()
	q := ResourceQuery{OrganizationalUnits: []string{parent}, Names: candidates}
	st, err := s.gatherFrom(ctx, q, horizon(window), now, fresh, kept)
	if err != nil {
		return nil, res, false, err
	}
	res = engine.Check(st.resources, window, quantity, now)
	_, versioned := s.store.(VersionedStore)
	cached := versioned && !fresh
	if !res.Bookable {
		if cached && staticRefusal(res) {
			return nil, res, true, nil
		}
		return nil, res, false, nil
	}
	picked = engine.Pick(res.Fits, quantity, s.shuffle)
	if cached {
		ok, err := s.verifyPicked(ctx, parent, st, picked)
		if err != nil {
			return nil, res, false, err
		}
		if !ok {
			s.statics.drop(parent)
			return nil, res, true, nil
		}
	}
	return picked, res, false, nil
}

// staticRefusal reports whether any resource was refused for a reason that
// comes from its static inputs -- availability, policy, deletion -- rather
// than from the bookings read fresh on every call.
func staticRefusal(res engine.CheckResult) bool {
	for _, r := range res.Reasons {
		switch r.Reason {
		case engine.ReasonNone, engine.ReasonNoCapacity, engine.ReasonHeld, engine.ReasonBooked:
		default:
			return true
		}
	}
	return false
}

// verifyPicked reads the chosen resources' static inputs from the database and
// reports whether they match the cached ones the plan was made from.
func (s *Server) verifyPicked(ctx context.Context, parent string, st *site, picked []string) (bool, error) {
	got, err := s.readStatics(ctx, ResourceQuery{OrganizationalUnits: []string{parent}, Names: picked})
	if err != nil {
		return false, err
	}
	if len(got) != len(picked) {
		return false, nil
	}
	cached := map[string]staticResource{}
	for _, r := range st.statics {
		cached[r.rec.Resource.GetName()] = r
	}
	for _, g := range got {
		c, ok := cached[g.rec.Resource.GetName()]
		if !ok || !sameStatics(c, g) {
			return false, nil
		}
	}
	return true, nil
}

// unbookable is the problem with a window that cannot be held: outside the
// published availability of every resource asked about, or offered but not
// enough of it free.
func unbookable(parent string, res engine.CheckResult, quantity int) error {
	if outsideAvailability(res) {
		return &problem.Problem{
			Kind: allocationpbv1svc.ErrCapacityExhausted, Reason: "OUTSIDE_AVAILABILITY",
			ResourceType: hints.TypeOf(parent), ResourceName: parent,
			What: "the window is outside the published availability of every resource asked about: nothing is offered then",
			Fix: "Ask for a window inside opening hours -- " + hints.Call("ComputeAvailability", map[string]string{"parent": parent}) +
				" shows what is offered and free when",
		}
	}
	return &problem.Problem{
		Kind: allocationpbv1svc.ErrCapacityExhausted, Reason: allocationpbv1svc.ReasonCapacityExhausted,
		ResourceType: hints.TypeOf(parent), ResourceName: parent,
		What: fmt.Sprintf("only %d of the %d resources asked for are free for that whole window", len(res.Candidates), quantity),
		Fix: "Ask for fewer, or another window -- " + hints.Call("ComputeAvailability", map[string]string{"parent": parent}) +
			" shows what is free when, and CheckAvailability says why a window is not",
	}
}

// outsideAvailability reports whether no resource was offered for the window
// at all: every one refused as outside its availability, or deleted, and at
// least one of them for availability. A site with no resources is not
// outside availability; it has nothing to offer.
func outsideAvailability(res engine.CheckResult) bool {
	outside := false
	for _, r := range res.Reasons {
		switch r.Reason {
		case engine.ReasonNoCapacity, engine.ReasonResourceInactive:
		case engine.ReasonOutsideAvailability:
			outside = true
		default:
			return false
		}
	}
	return outside
}

// replaced is the time the booking a new hold replaces keeps, by resource:
// the window of that hold, the reservation's, on each of its resources. It
// must be a converted hold at the same site still keeping them; nil when
// name is empty.
func (s *Server) replaced(ctx context.Context, parent, name string) (map[string]engine.Spans, error) {
	if name == "" {
		return nil, nil
	}
	if siteOf(name) != parent {
		return nil, servicex.BadField("HOLD_AT_ANOTHER_SITE", "hold.replaces",
			fmt.Sprintf("hold %q is not a hold at %s", name, parent),
			"A hold can replace only a booking at its own site: send the hold of a reservation at "+parent)
	}
	stored, err := s.store.GetHold(ctx, name)
	if errors.Is(err, repox.ErrNotFound) {
		return nil, servicex.BadField("HOLD_NOT_FOUND", "hold.replaces",
			fmt.Sprintf("hold %s does not exist", name),
			"Send the hold of the reservation to move: GetReservation returns it as hold")
	}
	if err != nil {
		return nil, err
	}
	if stored.GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED || stored.GetExpireTime() != nil {
		return nil, servicex.Blocked("REPLACED_HOLD_NOT_BOOKED", hints.TypeOf(name), name,
			fmt.Sprintf("hold %s backs no booking that still keeps its resources, so there is nothing for this hold to replace", name),
			"Send the hold of a confirmed reservation, or leave hold.replaces empty to take a hold on free time only")
	}
	w, err := spanOf(stored.GetWindow())
	if err != nil {
		return nil, err
	}
	kept := make(map[string]engine.Spans, len(stored.GetResources()))
	for _, r := range stored.GetResources() {
		kept[r] = engine.Spans{w}
	}
	return kept, nil
}

// HandOver ends the conversion of hold from, as EndConversion does, for a
// reschedule onto hold to. When to was taken to replace from (its replaces),
// the time to's window shares with from's on the same resources -- which to
// could not claim while from kept it -- passes to to in the same write that
// ends from, so nobody else can take it in between. Idempotent: once from has
// ended, the hand-over it was part of has happened.
func (s *Server) HandOver(ctx context.Context, from, to string, at time.Time) (*allocationpbv1.Hold, error) {
	next, err := s.store.GetHold(ctx, to)
	if err != nil && !errors.Is(err, repox.ErrNotFound) {
		return nil, err
	}
	if err != nil || next.GetReplaces() != from {
		return s.EndConversion(ctx, from, at)
	}
	return s.settle(ctx, from, func() (*allocationpbv1.Hold, error) {
		stored, err := s.store.GetHold(ctx, from)
		if err != nil {
			return nil, err
		}
		if h := s.authoritative(stored); h.GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED || h.GetExpireTime() != nil {
			return h, nil
		}
		return s.store.HandOver(ctx, from, stored.GetEtag(), to, at)
	})
}

// keyedID derives a hold id from a site and an idempotency key: 26 lowercase
// hex characters of their SHA-256, the length of a ULID so names stay uniform.
func keyedID(parent, key string) string {
	sum := sha256.Sum256([]byte(parent + "\x00" + key))
	return hex.EncodeToString(sum[:13])
}

// GetHold returns one hold, with expiry applied.
func (s *Server) GetHold(ctx context.Context, req *allocationpbv1.GetHoldRequest) (*allocationpbv1.Hold, error) {
	var out *allocationpbv1.Hold
	err := rpc.Traced(ctx, "HoldService", "GetHold", func(ctx context.Context) error {
		h, err := s.store.GetHold(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = s.authoritative(h)
		return nil
	})
	return out, err
}

// ListHolds returns a page of a site's holds, with expiry applied.
func (s *Server) ListHolds(ctx context.Context, req *allocationpbv1.ListHoldsRequest) (*allocationpbv1.ListHoldsResponse, error) {
	var out *allocationpbv1.ListHoldsResponse
	err := rpc.Traced(ctx, "HoldService", "ListHolds", func(ctx context.Context) error {
		items, next, err := s.store.ListHolds(ctx, req.GetParent(), repox.ListInput{
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
			Filter:    req.GetFilter(),
		})
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		for i := range items {
			items[i] = s.authoritative(items[i])
		}
		out = &allocationpbv1.ListHoldsResponse{Holds: items, NextPageToken: next}
		return nil
	})
	return out, err
}

// ExtendHold pushes a live hold's expiry to now + duration.
func (s *Server) ExtendHold(ctx context.Context, req *allocationpbv1.ExtendHoldRequest) (*allocationpbv1.Hold, error) {
	var out *allocationpbv1.Hold
	err := rpc.Traced(ctx, "HoldService", "ExtendHold", func(ctx context.Context) error {
		d := req.GetDuration().AsDuration()
		if d <= 0 || d > MaxHoldExtension {
			return rpc.ToStatusErr(servicex.BadField("INVALID_HOLD_EXTENSION", "duration",
				fmt.Sprintf("duration must be more than zero and at most %s; got %s", MaxHoldExtension, d),
				"A hold is meant to be short: extend it again before it expires if settling needs longer"))
		}
		h, err := s.live(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		expire := s.now().Add(d)
		out, err = s.store.UpdateHold(ctx, h.GetName(), etagOr(req.GetEtag(), h), allocationpbv1.HoldState_HOLD_STATE_ACTIVE, &expire)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// ReleaseHold gives a hold up early. Releasing a hold that is already released
// or expired succeeds unchanged: the caller wanted the resources free, and they
// are.
func (s *Server) ReleaseHold(ctx context.Context, req *allocationpbv1.ReleaseHoldRequest) (*allocationpbv1.Hold, error) {
	var out *allocationpbv1.Hold
	err := rpc.Traced(ctx, "HoldService", "ReleaseHold", func(ctx context.Context) error {
		stored, err := s.store.GetHold(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		h := s.authoritative(stored)
		switch h.GetState() {
		case allocationpbv1.HoldState_HOLD_STATE_RELEASED, allocationpbv1.HoldState_HOLD_STATE_EXPIRED:
			out = h
			return nil
		case allocationpbv1.HoldState_HOLD_STATE_CONVERTED:
			return rpc.ToStatusErr(servicex.Blocked("HOLD_CONVERTED", hints.TypeOf(h.GetName()), h.GetName(),
				fmt.Sprintf("hold %s became a reservation, and releasing it would free resources that are booked", h.GetName()),
				"Cancel the reservation instead with CancelReservation; "+
					hints.Call("ListReservations", map[string]string{"parent": siteOf(h.GetName())})+
					" with filter hold = \""+h.GetName()+"\" finds it"))
		}
		now := s.now()
		out, err = s.store.UpdateHold(ctx, h.GetName(), etagOr(req.GetEtag(), h), allocationpbv1.HoldState_HOLD_STATE_RELEASED, &now)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// DeleteHold removes a terminal hold's record. A live hold must be released
// first, so inventory is never freed by a path that leaves no trace.
func (s *Server) DeleteHold(ctx context.Context, req *allocationpbv1.DeleteHoldRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, "HoldService", "DeleteHold", func(ctx context.Context) error {
		stored, err := s.store.GetHold(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		h := s.authoritative(stored)
		if h.GetState() == allocationpbv1.HoldState_HOLD_STATE_ACTIVE {
			return rpc.ToStatusErr(servicex.Blocked("HOLD_ACTIVE", hints.TypeOf(h.GetName()), h.GetName(),
				fmt.Sprintf("hold %s is active and still holds its resources", h.GetName()),
				"Release it first with "+hints.Call("ReleaseHold", map[string]string{"name": h.GetName()})+", then delete it"))
		}
		return rpc.ToStatusErr(s.store.DeleteHold(ctx, h.GetName(), etagOr(req.GetEtag(), stored)))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ConvertHold turns a live hold into a permanent claim on its resources. Not an
// RPC: conversion is the booking side's decision, made after payment, and the
// reservation service calls this in-process rather than reaching into
// HoldService over the wire.
//
// Idempotent, including under concurrency: converting a hold that is already
// CONVERTED returns it, and so does losing the race to convert it, since the
// winner's result is the one the caller wanted. A reservation retried after a
// crash, or created twice at once, therefore finishes rather than fails.
func (s *Server) ConvertHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	return s.settle(ctx, name, func() (*allocationpbv1.Hold, error) {
		stored, err := s.store.GetHold(ctx, name)
		if err != nil {
			return nil, err
		}
		// One read decides. Reading again to check liveness would let a
		// concurrent conversion land between the reads and turn this
		// caller's success into "no longer convertible".
		switch h := s.authoritative(stored); h.GetState() {
		case allocationpbv1.HoldState_HOLD_STATE_CONVERTED:
			return stored, nil
		case allocationpbv1.HoldState_HOLD_STATE_ACTIVE:
			return s.store.UpdateHold(ctx, name, stored.GetEtag(), allocationpbv1.HoldState_HOLD_STATE_CONVERTED, nil)
		default:
			return nil, notActive(name, h.GetState())
		}
	})
}

// settleAttempts bounds how often settle re-runs a step that lost an etag race.
const settleAttempts = 4

// settle runs an idempotent step, re-running it when it loses an etag race.
// Each re-run re-reads the hold, so a step whose goal another caller already
// reached returns that result instead of the conflict.
func (s *Server) settle(ctx context.Context, name string, step func() (*allocationpbv1.Hold, error)) (*allocationpbv1.Hold, error) {
	for attempt := 1; ; attempt++ {
		h, err := step()
		if !errors.Is(err, repox.ErrConflict) || attempt >= settleAttempts {
			return h, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

// EndConversion frees the resources a hold keeps, as of at. In-process, for the
// reservation service when a reservation is cancelled.
//
// A converted hold stays CONVERTED -- that it was booked is history worth
// keeping -- but gains an expiry, and like any claim it stops counting the
// instant that passes. A hold still ACTIVE is released instead. Idempotent:
// a hold already ended, released or expired is returned unchanged.
func (s *Server) EndConversion(ctx context.Context, name string, at time.Time) (*allocationpbv1.Hold, error) {
	return s.settle(ctx, name, func() (*allocationpbv1.Hold, error) {
		stored, err := s.store.GetHold(ctx, name)
		if err != nil {
			return nil, err
		}
		switch h := s.authoritative(stored); {
		case h.GetState() == allocationpbv1.HoldState_HOLD_STATE_CONVERTED && h.GetExpireTime() == nil:
			return s.store.UpdateHold(ctx, name, stored.GetEtag(), allocationpbv1.HoldState_HOLD_STATE_CONVERTED, &at)
		case h.GetState() == allocationpbv1.HoldState_HOLD_STATE_ACTIVE:
			return s.store.UpdateHold(ctx, name, stored.GetEtag(), allocationpbv1.HoldState_HOLD_STATE_RELEASED, &at)
		default:
			return h, nil
		}
	})
}

// LookupHold returns one hold with expiry applied, for in-process callers that
// want domain errors rather than gRPC statuses.
func (s *Server) LookupHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	h, err := s.store.GetHold(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.authoritative(h), nil
}

// live returns a hold that is ACTIVE and not lapsed, or ErrInvalidState.
func (s *Server) live(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	stored, err := s.store.GetHold(ctx, name)
	if err != nil {
		return nil, err
	}
	h := s.authoritative(stored)
	if h.GetState() != allocationpbv1.HoldState_HOLD_STATE_ACTIVE {
		return nil, notActive(name, h.GetState())
	}
	return stored, nil
}

// authoritative reports a lapsed ACTIVE hold as EXPIRED, whether or not the
// sweeper has written that yet. The stored row is left alone; the copy is what
// the caller sees.
func (s *Server) authoritative(h *allocationpbv1.Hold) *allocationpbv1.Hold {
	if h.GetState() != allocationpbv1.HoldState_HOLD_STATE_ACTIVE || h.GetExpireTime() == nil {
		return h
	}
	if h.GetExpireTime().AsTime().After(s.now()) {
		return h
	}
	out := proto.Clone(h).(*allocationpbv1.Hold)
	out.State = allocationpbv1.HoldState_HOLD_STATE_EXPIRED
	return out
}

// etagOr returns the caller's etag when given, otherwise the one just read, so
// a call without an etag is still guarded against a change that lands between
// this read and the write.
func etagOr(given string, h *allocationpbv1.Hold) string {
	if given != "" {
		return given
	}
	return h.GetEtag()
}

// sweepInterval is how often lapsed holds are marked EXPIRED. Reads never wait
// for it, so this only affects list views and reporting.
const sweepInterval = time.Minute

// StartHoldSweeper runs ExpireHolds every interval (sweepInterval when <= 0)
// until ctx ends. Non-blocking.
func (s *Server) StartHoldSweeper(ctx context.Context, interval time.Duration, logErr func(error)) {
	if interval <= 0 {
		interval = sweepInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			expired, err := s.store.ExpireHolds(ctx, s.now())
			if ctx.Err() != nil {
				return
			}
			s.sweeps.record(ctx, expired, err)
			if err != nil && logErr != nil {
				logErr(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// notActive is the problem with acting on a hold that no longer holds
// anything: it expired, was released, or became a reservation.
func notActive(name string, state allocationpbv1.HoldState) error {
	word := strings.ToLower(strings.TrimPrefix(state.String(), "HOLD_STATE_"))
	fix := "A hold that has " + word + " holds nothing: take a new one with " +
		hints.Call("CreateHold", map[string]string{"parent": siteOf(name)}) +
		" and act on that; to keep a hold alive while you settle, ExtendHold it before expire_time"
	if state == allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
		word, fix = "converted into a reservation", "Act on the reservation it became instead"
	}
	return servicex.Blocked("HOLD_NOT_ACTIVE", hints.TypeOf(name), name, fmt.Sprintf("hold %s is %s, not active", name, word), fix)
}

// siteOf is the site a hold or reservation name sits under:
// "organizationalUnits/o/holds/h" is "organizationalUnits/o".
func siteOf(name string) string {
	segs := strings.SplitN(name, "/", 3)
	if len(segs) < 2 {
		return name
	}
	return segs[0] + "/" + segs[1]
}
