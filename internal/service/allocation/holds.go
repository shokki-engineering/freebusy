package allocation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/oh-tarnished/runtime-go/ulid"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
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
		case errors.Is(err, types.ErrNotFound):
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
		id = ulid.GenerateString()
	}
	quantity := max(int(in.GetQuantity()), 1)

	for attempt := 0; ; attempt++ {
		if prev, err := prior(); prev != nil || err != nil {
			return prev, err
		}
		now := s.now()
		st, err := s.gather(ctx, ResourceQuery{
			OrganizationalUnits: []string{parent},
			Names:               in.GetCandidateResources(),
		}, horizon(window), now)
		if err != nil {
			return nil, err
		}
		res := engine.Check(st.resources, window, quantity, now)
		if !res.Bookable {
			if prev, err := prior(); prev != nil || err != nil {
				return prev, err
			}
			return nil, errorf(types.ErrCapacityExhausted, "%d of %d requested resources free", len(res.Candidates), quantity)
		}
		hold := &allocationpbv1.Hold{
			Name:               parent + "/holds/" + id,
			Resources:          res.Candidates[:quantity],
			Window:             intervalOf(window),
			CandidateResources: in.GetCandidateResources(),
			Quantity:           int32(quantity),
			ExpireTime:         timestamppb.New(now.Add(s.holdTTL)),
			State:              allocationpbv1.HoldState_HOLD_STATE_ACTIVE,
			IdempotencyKey:     in.GetIdempotencyKey(),
		}
		created, err := s.store.InsertHold(ctx, parent, hold)
		if err == nil {
			return created, nil
		}
		if errors.Is(err, types.ErrAlreadyExists) {
			if prev, perr := prior(); prev != nil || perr != nil {
				return prev, perr
			}
		}
		if !errors.Is(err, types.ErrConflict) || attempt+1 >= createAttempts {
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
			return rpc.ToStatusErr(errorf(types.ErrInvalidArgument, "duration must be positive and at most %s", MaxHoldExtension))
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
			return rpc.ToStatusErr(errorf(types.ErrInvalidState, "hold %s was converted to a booking; cancel the booking instead", h.GetName()))
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
			return rpc.ToStatusErr(errorf(types.ErrInvalidState, "hold %s is active; release it first", h.GetName()))
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
			return nil, errorf(types.ErrInvalidState, "hold %s is %s", name, h.GetState())
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
		if !errors.Is(err, types.ErrConflict) || attempt >= settleAttempts {
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
		return nil, errorf(types.ErrInvalidState, "hold %s is %s", name, h.GetState())
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
			if _, err := s.store.ExpireHolds(ctx, s.now()); err != nil && logErr != nil && ctx.Err() == nil {
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
