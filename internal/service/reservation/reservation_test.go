package reservation

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const site = "organizationalUnits/club"

// memStore is an in-memory Store with the compare-and-set Transition the real
// stores give.
type memStore struct {
	mu      sync.Mutex
	rows    map[string]*reservationpbv1.Reservation
	updated map[string]time.Time
	clock   func() time.Time
	version int
}

func newMemStore(clock func() time.Time) *memStore {
	return &memStore{rows: map[string]*reservationpbv1.Reservation{}, updated: map[string]time.Time{}, clock: clock}
}

func (m *memStore) Get(_ context.Context, name string) (*reservationpbv1.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[name]
	if !ok {
		return nil, types.ErrNotFound
	}
	return proto.Clone(r).(*reservationpbv1.Reservation), nil
}

func (m *memStore) FindByHold(_ context.Context, hold string) (*reservationpbv1.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.GetHold() == hold {
			return proto.Clone(r).(*reservationpbv1.Reservation), nil
		}
	}
	return nil, types.ErrNotFound
}

func (m *memStore) List(context.Context, string, repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
	return nil, "", nil
}

func (m *memStore) Insert(_ context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[r.GetName()]; ok {
		return nil, types.ErrAlreadyExists
	}
	for _, other := range m.rows {
		if other.GetHold() == r.GetHold() {
			return nil, types.ErrAlreadyExists // the unique index on hold
		}
	}
	c := proto.Clone(r).(*reservationpbv1.Reservation)
	m.version++
	c.Etag = strconv.Itoa(m.version)
	m.rows[r.GetName()] = c
	m.updated[r.GetName()] = m.clock()
	return proto.Clone(c).(*reservationpbv1.Reservation), nil
}

func (m *memStore) Transition(_ context.Context, name, etag string, from []reservationpbv1.ReservationState, c Change) (*reservationpbv1.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[name]
	if !ok {
		return nil, types.ErrNotFound
	}
	if !slices.Contains(from, r.GetState()) || (etag != "" && etag != r.GetEtag()) {
		return nil, types.ErrConflict
	}
	r.State = c.State
	if c.ConfirmTime != nil {
		r.ConfirmTime = timestamppb.New(*c.ConfirmTime)
	}
	if c.CancelTime != nil {
		r.CancelTime = timestamppb.New(*c.CancelTime)
	}
	if c.CancelReason != "" {
		r.CancelReason = c.CancelReason
	}
	if c.Replacement != nil {
		r.Replacement = *c.Replacement
	}
	m.version++
	r.Etag = strconv.Itoa(m.version)
	m.updated[name] = m.clock()
	return proto.Clone(r).(*reservationpbv1.Reservation), nil
}

func (m *memStore) Stuck(_ context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*reservationpbv1.Reservation
	for n, r := range m.rows {
		switch r.GetState() {
		case reservationpbv1.ReservationState_RESERVATION_STATE_PENDING, reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING,
			reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING:
			if m.updated[n].Before(cutoff) && len(out) < limit {
				out = append(out, proto.Clone(r).(*reservationpbv1.Reservation))
			}
		}
	}
	return out, nil
}

// fakeHolds stands in for the engine, with switches to make a call fail once
// the way a crash or a timeout would.
type fakeHolds struct {
	mu          sync.Mutex
	holds       map[string]*allocationpbv1.Hold
	failConvert error
	failEnd     error
	ended       map[string]int
}

func (f *fakeHolds) LookupHold(_ context.Context, name string) (*allocationpbv1.Hold, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.holds[name]
	if !ok {
		return nil, types.ErrNotFound
	}
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (f *fakeHolds) ConvertHold(_ context.Context, name string) (*allocationpbv1.Hold, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failConvert; err != nil {
		f.failConvert = nil
		return nil, err
	}
	h := f.holds[name]
	switch h.GetState() {
	case allocationpbv1.HoldState_HOLD_STATE_CONVERTED:
	case allocationpbv1.HoldState_HOLD_STATE_ACTIVE:
		h.State = allocationpbv1.HoldState_HOLD_STATE_CONVERTED
	default:
		return nil, types.ErrInvalidState
	}
	return proto.Clone(h).(*allocationpbv1.Hold), nil
}

func (f *fakeHolds) EndConversion(_ context.Context, name string, _ time.Time) (*allocationpbv1.Hold, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failEnd; err != nil {
		f.failEnd = nil
		return nil, err
	}
	f.ended[name]++
	return f.holds[name], nil
}

type fixture struct {
	srv   *Server
	store *memStore
	holds *fakeHolds
	clock time.Time
}

func newFixture() *fixture {
	f := &fixture{clock: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	now := func() time.Time { return f.clock }
	f.store = newMemStore(now)
	f.holds = &fakeHolds{holds: map[string]*allocationpbv1.Hold{}, ended: map[string]int{}}
	f.srv = NewServer(f.store, f.holds, WithClock(now))
	return f
}

func (f *fixture) hold(id string, state allocationpbv1.HoldState) string {
	name := site + "/holds/" + id
	f.holds.holds[name] = &allocationpbv1.Hold{
		Name:      name,
		State:     state,
		Resources: []string{"resources/court-1"},
		Window: &interval.Interval{
			StartTime: timestamppb.New(f.clock.Add(24 * time.Hour)),
			EndTime:   timestamppb.New(f.clock.Add(25 * time.Hour)),
		},
	}
	return name
}

func (f *fixture) create(hold string) (*reservationpbv1.Reservation, error) {
	return f.srv.CreateReservation(context.Background(), &reservationpbv1.CreateReservationRequest{
		Parent: site, Reservation: &reservationpbv1.Reservation{Hold: hold, Customer: "users/ana"},
	})
}

func TestCreateConfirmsAndIsIdempotent(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	r, err := f.create(h)
	if err != nil {
		t.Fatal(err)
	}
	if r.GetName() != site+"/reservations/h1" || r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("got %s %v", r.GetName(), r.GetState())
	}
	if f.holds.holds[h].GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
		t.Fatal("hold not converted")
	}
	if len(r.GetResources()) != 1 || r.GetStartTime() == nil || r.GetCustomer() != "users/ana" {
		t.Fatalf("copied fields: %v", r)
	}
	again, err := f.create(h)
	if err != nil || again.GetEtag() != r.GetEtag() || len(f.store.rows) != 1 {
		t.Fatalf("retry changed something: %v %v", again, err)
	}
}

func TestCreateRejectsLapsedOrForeignHold(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_EXPIRED)
	if _, err := f.create(h); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("lapsed hold: %v", err)
	}
	if len(f.store.rows) != 0 {
		t.Fatal("a lapsed hold should leave no reservation behind")
	}
	if _, err := f.create("organizationalUnits/other/holds/h1"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign hold: %v", err)
	}
}

func TestHoldLapsingMidCreateFails(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	f.holds.failConvert = types.ErrInvalidState // lapsed between lookup and convert
	if _, err := f.create(h); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v", err)
	}
	r, _ := f.store.Get(context.Background(), site+"/reservations/h1")
	if r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_FAILED {
		t.Fatalf("state %v", r.GetState())
	}
}

func TestReconcilerFinishesInterruptedCreate(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	f.holds.failConvert = errors.New("engine unreachable")
	if _, err := f.create(h); err == nil {
		t.Fatal("expected the interrupted create to fail")
	}
	r, _ := f.store.Get(context.Background(), site+"/reservations/h1")
	if r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_PENDING {
		t.Fatalf("state %v", r.GetState())
	}
	if n, _ := f.srv.Reconcile(context.Background()); n != 0 {
		t.Fatal("reconciler must not touch a reservation that may still be in flight")
	}
	f.clock = f.clock.Add(stuckAfter + time.Second)
	if n, err := f.srv.Reconcile(context.Background()); n != 1 || err != nil {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	r, _ = f.store.Get(context.Background(), site+"/reservations/h1")
	if r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("after reconcile: %v", r.GetState())
	}
}

func TestCancel(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	r, err := f.create(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Etag: "stale"}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale etag: %v", err)
	}
	for range 2 {
		c, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Reason: "rain"})
		if err != nil || c.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED || c.GetCancelReason() != "rain" {
			t.Fatalf("cancel: %v %v", c, err)
		}
	}
	if f.holds.ended[h] != 1 {
		t.Fatalf("EndConversion ran %d times, want 1", f.holds.ended[h])
	}
}

func TestReconcilerFinishesInterruptedCancel(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	r, err := f.create(h)
	if err != nil {
		t.Fatal(err)
	}
	f.holds.failEnd = errors.New("engine unreachable")
	if _, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: r.GetName()}); err == nil {
		t.Fatal("expected the interrupted cancel to fail")
	}
	got, _ := f.store.Get(context.Background(), r.GetName())
	if got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLING {
		t.Fatalf("state %v: the cancellation must be recorded before resources are freed", got.GetState())
	}
	f.clock = f.clock.Add(stuckAfter + time.Second)
	if n, err := f.srv.Reconcile(context.Background()); n != 1 || err != nil {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	got, _ = f.store.Get(context.Background(), r.GetName())
	if got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED || f.holds.ended[h] != 1 {
		t.Fatalf("after reconcile: %v, ended %d", got.GetState(), f.holds.ended[h])
	}
}

// A conversion that reports "not convertible" because a concurrent caller
// converted the hold first must confirm, never fail: a FAILED reservation over
// a converted hold would keep a resource nobody owns.
func TestLostConvertRaceConfirmsNotFails(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_CONVERTED)
	f.holds.failConvert = types.ErrInvalidState
	r, err := f.create(h)
	if err != nil || r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("got %v %v", r.GetState(), err)
	}
}

func (f *fixture) move(name, hold string) (*reservationpbv1.Reservation, error) {
	return f.srv.RescheduleReservation(context.Background(), &reservationpbv1.RescheduleReservationRequest{Name: name, Hold: hold})
}

func (f *fixture) get(t *testing.T, name string) *reservationpbv1.Reservation {
	t.Helper()
	r, err := f.store.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRescheduleMovesAndLinks(t *testing.T) {
	f := newFixture()
	oldHold := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	old, err := f.create(oldHold)
	if err != nil {
		t.Fatal(err)
	}
	newHold := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	rep, err := f.move(old.GetName(), newHold)
	if err != nil {
		t.Fatal(err)
	}
	if rep.GetName() != site+"/reservations/h2" || rep.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED ||
		rep.GetReplaces() != old.GetName() || rep.GetCustomer() != "users/ana" {
		t.Fatalf("replacement %v", rep)
	}
	o := f.get(t, old.GetName())
	if o.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED || o.GetReplacement() != rep.GetName() {
		t.Fatalf("original %v", o)
	}
	if f.holds.ended[oldHold] != 1 || f.holds.holds[newHold].GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
		t.Fatalf("old ended %d, new hold %v", f.holds.ended[oldHold], f.holds.holds[newHold].GetState())
	}

	again, err := f.move(old.GetName(), newHold)
	if err != nil || again.GetName() != rep.GetName() {
		t.Fatalf("retry: %v %v", again, err)
	}
	other := f.hold("h3", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	if _, err := f.move(old.GetName(), other); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("moving a moved reservation elsewhere: %v", err)
	}
	if _, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: old.GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cancelling the moved-from reservation: %v", err)
	}
}

func TestRescheduleOntoLapsedHoldKeepsOriginal(t *testing.T) {
	f := newFixture()
	oldHold := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	old, _ := f.create(oldHold)
	newHold := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	f.holds.failConvert = types.ErrInvalidState // lapses between lookup and convert
	if _, err := f.move(old.GetName(), newHold); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v", err)
	}
	o := f.get(t, old.GetName())
	if o.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || o.GetReplacement() != "" {
		t.Fatalf("original should be CONFIRMED again with no replacement: %v", o)
	}
	if f.holds.ended[oldHold] != 0 {
		t.Fatal("the original's slot was let go for a move that never happened")
	}
}

func TestReconcilerFinishesInterruptedReschedule(t *testing.T) {
	f := newFixture()
	oldHold := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	old, _ := f.create(oldHold)
	newHold := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	f.holds.failConvert = errors.New("engine unreachable")
	if _, err := f.move(old.GetName(), newHold); err == nil {
		t.Fatal("expected the interrupted move to fail")
	}
	if s := f.get(t, old.GetName()).GetState(); s != reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULING {
		t.Fatalf("original %v", s)
	}
	if f.holds.ended[oldHold] != 0 {
		t.Fatal("old slot let go before the replacement was confirmed")
	}
	f.clock = f.clock.Add(stuckAfter + time.Second)
	if _, err := f.srv.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := f.get(t, old.GetName()).GetState(); s != reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED {
		t.Fatalf("after reconcile original %v", s)
	}
	if s := f.get(t, site+"/reservations/h2").GetState(); s != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("after reconcile replacement %v", s)
	}
}

func TestRescheduleOntoCancelledReplacementReverts(t *testing.T) {
	f := newFixture()
	old, _ := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	newHold := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	rep, _ := f.create(newHold)
	if _, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: rep.GetName()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(old.GetName(), newHold); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v", err)
	}
	if s := f.get(t, old.GetName()).GetState(); s != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("original %v", s)
	}
}

func (f *fixture) createAs(hold, id string) (*reservationpbv1.Reservation, error) {
	return f.srv.CreateReservation(context.Background(), &reservationpbv1.CreateReservationRequest{
		Parent: site, ReservationId: id, Reservation: &reservationpbv1.Reservation{Hold: hold},
	})
}

func TestReservationID(t *testing.T) {
	f := newFixture()
	h1 := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	r, err := f.createAs(h1, "court-booking-1")
	if err != nil || r.GetName() != site+"/reservations/court-booking-1" || r.GetHold() != h1 {
		t.Fatalf("custom id: %v %v", r, err)
	}
	// The same hold under another id is the same reservation, not a second.
	again, err := f.createAs(h1, "something-else")
	if err != nil || again.GetName() != r.GetName() || len(f.store.rows) != 1 {
		t.Fatalf("second id for one hold: %v %v", again, err)
	}
	// An id already used by another hold's reservation is taken.
	h2 := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	if _, err := f.createAs(h2, "court-booking-1"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("taken id: %v", err)
	}
	// Moving a custom-named reservation still works, and finds the
	// replacement by its hold.
	rep, err := f.move(r.GetName(), h2)
	if err != nil || rep.GetName() != site+"/reservations/h2" || rep.GetReplaces() != r.GetName() {
		t.Fatalf("move: %v %v", rep, err)
	}
}

func TestRescheduleOntoAlreadyReservedHoldUsesIt(t *testing.T) {
	f := newFixture()
	old, _ := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	h2 := f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	mine, err := f.createAs(h2, "custom")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := f.move(old.GetName(), h2)
	if err != nil || rep.GetName() != mine.GetName() {
		t.Fatalf("move onto a hold that already has a reservation: %v %v", rep, err)
	}
	if o := f.get(t, old.GetName()); o.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED || o.GetReplacement() != mine.GetName() {
		t.Fatalf("original %v", o)
	}
}
