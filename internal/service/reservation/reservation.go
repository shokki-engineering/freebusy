// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"google.golang.org/protobuf/proto"
)

// Server implements ReservationService.
type Server struct {
	reservationpbv1.UnimplementedReservationServiceServer

	store Store
	holds Holds
	now   func() time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the server's clock. For tests.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// NewServer returns a Server persisting to store and driving holds.
func NewServer(store Store, holds Holds, opts ...Option) *Server {
	s := &Server{store: store, holds: holds, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateReservation confirms a live hold as a reservation.
func (s *Server) CreateReservation(ctx context.Context, req *reservationpbv1.CreateReservationRequest) (*reservationpbv1.Reservation, error) {
	var out *reservationpbv1.Reservation
	err := rpc.Traced(ctx, "ReservationService", "CreateReservation", func(ctx context.Context) error {
		r, err := s.create(ctx, req)
		out = r
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) create(ctx context.Context, req *reservationpbv1.CreateReservationRequest) (*reservationpbv1.Reservation, error) {
	in := req.GetReservation()
	return s.open(ctx, in.GetHold(), req.GetReservationId(), &reservationpbv1.Reservation{
		Customer:   in.GetCustomer(),
		Attributes: in.GetAttributes(),
	})
}

// open makes, or finishes making, the reservation for a hold, named
// id when given and after the hold otherwise. CreateReservation and
// RescheduleReservation's replacement both come through here; tmpl carries
// the fields the caller chose (customer, attributes, and for a replacement,
// replaces).
func (s *Server) open(ctx context.Context, holdName, id string, tmpl *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	name := reservationFor(holdName)
	if id != "" {
		name = "reservations/" + id
	}

	// A retry, or a second caller with the same hold: finish and return what
	// is already there, whatever it was named.
	if existing, err := s.store.FindByHold(ctx, holdName); err == nil {
		return s.advance(ctx, existing)
	} else if !errors.Is(err, repox.ErrNotFound) {
		return nil, err
	}

	h, err := s.holds.LookupHold(ctx, holdName)
	if errors.Is(err, repox.ErrNotFound) {
		return nil, servicex.BadField("HOLD_NOT_FOUND", "reservation.hold",
			fmt.Sprintf("hold %s does not exist", holdName),
			"A reservation is made from a live hold: take one first with "+
				hints.Call("CreateHold", nil)+
				" and send the name it returns as reservation.hold")
	}
	if err != nil {
		return nil, err
	}
	switch h.GetState() {
	case allocationpbv1.HoldState_HOLD_STATE_ACTIVE, allocationpbv1.HoldState_HOLD_STATE_CONVERTED:
	default:
		word := strings.ToLower(strings.TrimPrefix(h.GetState().String(), "HOLD_STATE_"))
		return nil, servicex.Blocked("HOLD_NOT_ACTIVE", hints.TypeOf(holdName), holdName,
			fmt.Sprintf("hold %s is %s, so it no longer holds the resources", holdName, word),
			"Take a new hold with "+hints.Call("CreateHold", nil)+
				" and create the reservation from it; if settling takes long, ExtendHold before expire_time")
	}

	// A hold taken to replace a booking claims only the time that booking
	// leaves free until RescheduleReservation hands the rest over, so it may
	// back nothing but the move of that booking.
	if replaced := h.GetReplaces(); replaced != "" {
		ok := false
		if tmpl.GetReplaces() != "" {
			moved, err := s.store.Get(ctx, tmpl.GetReplaces())
			if err != nil && !errors.Is(err, repox.ErrNotFound) {
				return nil, err
			}
			ok = err == nil && moved.GetHold() == replaced
		}
		if !ok {
			return nil, servicex.Blocked("HOLD_FOR_RESCHEDULE", hints.TypeOf(holdName), holdName,
				fmt.Sprintf("hold %s was taken to replace the booking of hold %s, so it can only reschedule the reservation made from that hold", holdName, replaced),
				"Reschedule that reservation onto it with RescheduleReservation, or take a hold without replaces with "+
					hints.Call("CreateHold", nil))
		}
	}

	// Record the intent first. If the conversion below never happens -- a
	// crash, a timeout -- this PENDING row is what tells the reconciler to
	// finish it; converting first would leave a hold nobody knew to clean up.
	r := &reservationpbv1.Reservation{
		Name:       name,
		Hold:       holdName,
		Resources:  h.GetResources(),
		StartTime:  h.GetWindow().GetStartTime(),
		EndTime:    h.GetWindow().GetEndTime(),
		Customer:   tmpl.GetCustomer(),
		State:      reservationpbv1.ReservationState_RESERVATION_STATE_PENDING,
		Attributes: tmpl.GetAttributes(),
		Replaces:   tmpl.GetReplaces(),
	}
	created, err := s.store.Insert(ctx, r)
	if errors.Is(err, repox.ErrAlreadyExists) {
		// Either a concurrent call made this hold's reservation first -- then
		// that one is the answer -- or the name belongs to another hold.
		existing, ferr := s.store.FindByHold(ctx, holdName)
		if ferr != nil {
			return nil, &problem.Problem{
				Kind: repox.ErrAlreadyExists, Reason: "RESERVATION_ID_TAKEN", Field: "reservation_id",
				ResourceType: hints.TypeOf(name), ResourceName: name,
				What: fmt.Sprintf("reservation %s already exists and was made from another hold", name),
				Fix:  "Choose another reservation_id, or leave it empty to name the reservation after its hold",
			}
		}
		created, err = existing, nil
	}
	if err != nil {
		return nil, err
	}
	return s.advance(ctx, created)
}

// reservationFor names the reservation a hold becomes by default: the
// hold's id.
func reservationFor(holdName string) string {
	return "reservations/" + lastSegment(holdName)
}

// advance carries a reservation in a transient state through to the state it
// was heading for. It is the whole of the recovery story: CreateReservation, a
// retry of it, CancelReservation and the reconciler all finish work by calling
// this, and every step in it is idempotent, so running it twice -- or two
// callers running it at once -- lands in the same place.
func (s *Server) advance(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	switch r.GetState() {
	case reservationpbv1.ReservationState_RESERVATION_STATE_PENDING:
		return s.confirm(ctx, r)
	case reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING:
		return s.finishCancel(ctx, r)
	case reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING:
		return s.finishReschedule(ctx, r)
	}
	return r, nil
}

func (s *Server) confirm(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	_, err := s.holds.ConvertHold(ctx, r.GetHold())
	switch {
	case errors.Is(err, servicex.ErrFailedPrecondition) || errors.Is(err, repox.ErrNotFound):
		// The hold lapsed, was released, or is gone: nothing was taken. Check
		// once more before saying so -- marking a reservation FAILED while its
		// hold is in fact converted would keep a resource nobody owns.
		if h, lerr := s.holds.LookupHold(ctx, r.GetHold()); lerr == nil && h.GetState() == allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
			return s.markConfirmed(ctx, r)
		}
		if _, terr := s.store.Transition(ctx, r.GetName(), "", pending, Change{State: reservationpbv1.ReservationState_RESERVATION_STATE_FAILED}); terr != nil {
			return s.settled(ctx, r.GetName(), terr)
		}
		return nil, servicex.Blocked("HOLD_LAPSED", hints.TypeOf(r.GetHold()), r.GetHold(),
			fmt.Sprintf("hold %s lapsed before the reservation could be confirmed, so reservation %s failed", r.GetHold(), r.GetName()),
			"Take a new hold with "+hints.Call("CreateHold", nil)+
				" and create the reservation again; hold for longer, or ExtendHold, if settling takes time")
	case err != nil:
		return nil, err
	}
	return s.markConfirmed(ctx, r)
}

func (s *Server) markConfirmed(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	now := s.now()
	done, err := s.store.Transition(ctx, r.GetName(), "", pending, Change{
		State:       reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED,
		ConfirmTime: &now,
	})
	if err != nil {
		return s.settled(ctx, r.GetName(), err)
	}
	return done, nil
}

func (s *Server) finishCancel(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	at := s.now()
	if t := r.GetCancelTime(); t != nil {
		at = t.AsTime()
	}
	if _, err := s.holds.EndConversion(ctx, r.GetHold(), at); err != nil && !errors.Is(err, repox.ErrNotFound) {
		return nil, err
	}
	done, err := s.store.Transition(ctx, r.GetName(), "", cancelling, Change{State: reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED})
	if err != nil {
		return s.settled(ctx, r.GetName(), err)
	}
	return done, nil
}

// settled handles a lost transition race: if another caller already moved the
// reservation on, its result is the answer, not an error.
func (s *Server) settled(ctx context.Context, name string, err error) (*reservationpbv1.Reservation, error) {
	if !errors.Is(err, repox.ErrConflict) {
		return nil, err
	}
	return s.store.Get(ctx, name)
}

var (
	pending      = []reservationpbv1.ReservationState{reservationpbv1.ReservationState_RESERVATION_STATE_PENDING}
	cancelling   = []reservationpbv1.ReservationState{reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING}
	confirmed    = []reservationpbv1.ReservationState{reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED}
	rescheduling = []reservationpbv1.ReservationState{reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING}
)

// GetReservation returns one reservation.
func (s *Server) GetReservation(ctx context.Context, req *reservationpbv1.GetReservationRequest) (*reservationpbv1.Reservation, error) {
	var out *reservationpbv1.Reservation
	err := rpc.Traced(ctx, "ReservationService", "GetReservation", func(ctx context.Context) error {
		r, err := s.store.Get(ctx, req.GetName())
		out = r
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// ListReservations returns a page of reservations.
func (s *Server) ListReservations(ctx context.Context, req *reservationpbv1.ListReservationsRequest) (*reservationpbv1.ListReservationsResponse, error) {
	var out *reservationpbv1.ListReservationsResponse
	err := rpc.Traced(ctx, "ReservationService", "ListReservations", func(ctx context.Context) error {
		items, next, err := s.store.List(ctx, repox.ListInput{
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
			Filter:    req.GetFilter(),
			OrderBy:   req.GetOrderBy(),
		})
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = &reservationpbv1.ListReservationsResponse{Reservations: items, NextPageToken: next}
		return nil
	})
	return out, err
}

// CancelReservation cancels a reservation and frees its resources.
func (s *Server) CancelReservation(ctx context.Context, req *reservationpbv1.CancelReservationRequest) (*reservationpbv1.Reservation, error) {
	var out *reservationpbv1.Reservation
	err := rpc.Traced(ctx, "ReservationService", "CancelReservation", func(ctx context.Context) error {
		r, err := s.cancel(ctx, req)
		out = r
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) cancel(ctx context.Context, req *reservationpbv1.CancelReservationRequest) (*reservationpbv1.Reservation, error) {
	r, err := s.store.Get(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	// A reservation still confirming, or mid-move, is finished first:
	// cancelling something half-made would race the conversion.
	if r.GetState() == reservationpbv1.ReservationState_RESERVATION_STATE_PENDING ||
		r.GetState() == reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING {
		if _, err := s.advance(ctx, r); err != nil && !errors.Is(err, servicex.ErrFailedPrecondition) {
			return nil, err
		}
		if r, err = s.store.Get(ctx, req.GetName()); err != nil {
			return nil, err
		}
	}
	switch r.GetState() {
	case reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED:
		return r, nil
	case reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING:
		return s.advance(ctx, r)
	case reservationpbv1.ReservationState_RESERVATION_STATE_FAILED:
		return nil, servicex.Blocked("RESERVATION_FAILED", hints.TypeOf(r.GetName()), r.GetName(),
			fmt.Sprintf("reservation %s failed, so it holds nothing to cancel", r.GetName()),
			"There is nothing to do: its resources were never taken")
	case reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED:
		return nil, servicex.Blocked("RESERVATION_MOVED", hints.TypeOf(r.GetName()), r.GetName(),
			fmt.Sprintf("reservation %s was rescheduled to %s, which now holds the booking", r.GetName(), r.GetReplacement()),
			"Cancel the replacement instead: "+hints.Call("CancelReservation", map[string]string{"name": r.GetReplacement()}))
	}

	// Record the cancellation before freeing anything. Freeing first would
	// leave, after a crash, a CONFIRMED reservation whose court is back on
	// sale; this order can only leave a court held a little too long.
	now := s.now()
	marked, err := s.store.Transition(ctx, r.GetName(), req.GetEtag(), confirmed, Change{
		State:        reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING,
		CancelTime:   &now,
		CancelReason: req.GetReason(),
	})
	if err != nil {
		if req.GetEtag() != "" {
			return nil, err
		}
		if marked, err = s.settled(ctx, r.GetName(), err); err != nil {
			return nil, err
		}
	}
	return s.advance(ctx, marked)
}

// stuckAfter is how long a reservation may sit PENDING or CANCELLING before
// the reconciler assumes its caller is gone and finishes it. Long enough that
// it never races a call still in flight on the happy path.
const stuckAfter = 30 * time.Second

// reconcileBatch bounds one pass; a backlog drains over successive ticks.
const reconcileBatch = 200

// Reconcile finishes every reservation left mid-transition, returning how many
// it moved on. Safe to run concurrently with live calls and with itself:
// advance is idempotent and every write is a compare-and-set.
func (s *Server) Reconcile(ctx context.Context) (int, error) {
	stuck, err := s.store.Stuck(ctx, s.now().Add(-stuckAfter), reconcileBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, r := range stuck {
		got, err := s.advance(ctx, r)
		if err != nil && !errors.Is(err, servicex.ErrFailedPrecondition) {
			errs = append(errs, err)
			continue
		}
		if got != nil && !proto.Equal(got, r) {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// reconcileInterval is how often the reconciler runs.
const reconcileInterval = time.Minute

// StartReconciler runs Reconcile every interval (reconcileInterval when <= 0)
// until ctx ends. Non-blocking.
func (s *Server) StartReconciler(ctx context.Context, interval time.Duration, logErr func(error)) {
	if interval <= 0 {
		interval = reconcileInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if _, err := s.Reconcile(ctx); err != nil && logErr != nil && ctx.Err() == nil {
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
