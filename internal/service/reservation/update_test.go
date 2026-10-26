// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

import (
	"context"
	"testing"

	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
)

func (f *fixture) update(r *reservationpbv1.Reservation, paths ...string) (*reservationpbv1.Reservation, error) {
	var mask *fieldmaskpb.FieldMask
	if paths != nil {
		mask = &fieldmaskpb.FieldMask{Paths: paths}
	}
	return f.srv.UpdateReservation(context.Background(), &reservationpbv1.UpdateReservationRequest{Reservation: r, UpdateMask: mask})
}

// reasonOf is the ErrorInfo reason err carries.
func reasonOf(err error) string {
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

func wantReason(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	if status.Code(err) != code || reasonOf(err) != reason {
		t.Fatalf("got %v (reason %q), want %v %s", err, reasonOf(err), code, reason)
	}
}

func attrs(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpdateCustomer(t *testing.T) {
	f := newFixture()
	r, err := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if err != nil {
		t.Fatal(err)
	}
	party := attrs(t, map[string]any{"party": 4})
	// attributes is sent but not in the mask: AIP-134 says it is ignored.
	got, err := f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben", Attributes: party}, "customer")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetCustomer() != "users/ben" || got.GetAttributes() != nil || got.GetEtag() == r.GetEtag() ||
		got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || got.GetHold() != r.GetHold() {
		t.Fatalf("after customer update: %v", got)
	}
	// Empty clears it.
	if got, err = f.update(&reservationpbv1.Reservation{Name: r.GetName()}, "customer"); err != nil || got.GetCustomer() != "" {
		t.Fatalf("clearing customer: %v %v", got, err)
	}
}

func TestUpdateAttributes(t *testing.T) {
	f := newFixture()
	r, err := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if err != nil {
		t.Fatal(err)
	}
	party := attrs(t, map[string]any{"party": 4, "note": "window seat"})
	got, err := f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ignored", Attributes: party}, "attributes")
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got.GetAttributes(), party) || got.GetCustomer() != "users/ana" {
		t.Fatalf("after attributes update: %v", got)
	}
	// Replaced whole, not merged.
	plate := attrs(t, map[string]any{"plate": "KA-01"})
	if got, err = f.update(&reservationpbv1.Reservation{Name: r.GetName(), Attributes: plate}, "attributes"); err != nil || !proto.Equal(got.GetAttributes(), plate) {
		t.Fatalf("replacing attributes: %v %v", got, err)
	}
	// "*" is both updatable fields.
	got, err = f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/cy", Attributes: party}, "*")
	if err != nil || got.GetCustomer() != "users/cy" || !proto.Equal(got.GetAttributes(), party) {
		t.Fatalf("update *: %v %v", got, err)
	}
}

func TestUpdateMaskValidation(t *testing.T) {
	f := newFixture()
	r, err := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if err != nil {
		t.Fatal(err)
	}
	in := &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben"}
	_, err = f.update(in)
	wantReason(t, err, codes.InvalidArgument, "REQUIRED_FIELD")
	_, err = f.update(in, []string{}...)
	wantReason(t, err, codes.InvalidArgument, "REQUIRED_FIELD")
	for _, path := range []string{"hold", "state", "start_time", "endTime", "resources", "etag", "name", "cancel_reason", "attributes.party", "nope"} {
		_, err := f.update(in, "customer", path)
		wantReason(t, err, codes.InvalidArgument, "UNKNOWN_MASK_PATH")
	}
	if f.store.writes != 0 {
		t.Fatalf("%d writes for rejected updates", f.store.writes)
	}
}

func TestUpdateEtag(t *testing.T) {
	f := newFixture()
	r, err := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben", Etag: "stale"}, "customer")
	wantReason(t, err, codes.Aborted, "CONCURRENT_MODIFICATION")
	if got := f.get(t, r.GetName()); got.GetCustomer() != "users/ana" {
		t.Fatalf("a stale update wrote: %v", got)
	}
	got, err := f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben", Etag: r.GetEtag()}, "customer")
	if err != nil || got.GetCustomer() != "users/ben" {
		t.Fatalf("current etag: %v %v", got, err)
	}
	// The etag it read is spent now.
	_, err = f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/cy", Etag: r.GetEtag()}, "customer")
	wantReason(t, err, codes.Aborted, "CONCURRENT_MODIFICATION")
}

func TestUpdateOnlyConfirmed(t *testing.T) {
	f := newFixture()
	r, err := f.create(f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.CancelReservation(context.Background(), &reservationpbv1.CancelReservationRequest{Name: r.GetName()}); err != nil {
		t.Fatal(err)
	}
	_, err = f.update(&reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben"}, "customer")
	wantReason(t, err, codes.FailedPrecondition, "RESERVATION_NOT_CONFIRMED")

	// A rescheduled reservation points at its replacement.
	old, _ := f.create(f.hold("h2", allocationpbv1.HoldState_HOLD_STATE_ACTIVE))
	if _, err := f.move(old.GetName(), f.hold("h3", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)); err != nil {
		t.Fatal(err)
	}
	_, err = f.update(&reservationpbv1.Reservation{Name: old.GetName(), Customer: "users/ben"}, "customer")
	wantReason(t, err, codes.FailedPrecondition, "RESERVATION_MOVED")

	if f.store.writes != 0 {
		t.Fatalf("%d writes to reservations that cannot be updated", f.store.writes)
	}
	if _, err := f.update(&reservationpbv1.Reservation{Name: "reservations/missing"}, "customer"); status.Code(err) != codes.NotFound {
		t.Fatalf("missing reservation: %v", err)
	}
}

// A reservation still PENDING is confirmed first, as a cancel would, and then
// updated.
func TestUpdateFinishesPending(t *testing.T) {
	f := newFixture()
	h := f.hold("h1", allocationpbv1.HoldState_HOLD_STATE_ACTIVE)
	f.holds.failConvert = context.DeadlineExceeded
	if _, err := f.create(h); err == nil {
		t.Fatal("expected the interrupted create to fail")
	}
	name := "reservations/h1"
	got, err := f.update(&reservationpbv1.Reservation{Name: name, Customer: "users/ben"}, "customer")
	if err != nil || got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || got.GetCustomer() != "users/ben" {
		t.Fatalf("update of a pending reservation: %v %v", got, err)
	}
}
