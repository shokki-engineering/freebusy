// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"testing"

	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The interceptor enforces the buf.validate rules annotated on the protos: a
// hold's replaces must match ^holds/[^/]+$ and its quantity is
// >= 0. Valid requests pass through to the handler
// untouched. The interceptor itself lives in runtime-go (grpc.WithValidation /
// grpc.NewValidationInterceptor); these tests pin its behavior against
// freebusy's protos.
func TestValidationInterceptor(t *testing.T) {
	intercept, err := grpc.NewValidationInterceptor()
	if err != nil {
		t.Fatalf("build interceptor: %v", err)
	}

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "ok", nil
	}

	window := &interval.Interval{StartTime: timestamppb.Now(), EndTime: timestamppb.Now()}

	// Bad hold name → InvalidArgument before the handler runs.
	bad := &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: window, Replaces: "rooms/nope"},
	}
	if _, err := intercept(context.Background(), bad, nil, handler); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad name: got err %v, want InvalidArgument", err)
	}
	if handlerCalled {
		t.Fatal("handler must not run on a validation failure")
	}

	// Negative quantity → InvalidArgument.
	neg := &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: window, Quantity: -1},
	}
	if _, err := intercept(context.Background(), neg, nil, handler); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative quantity: got err %v, want InvalidArgument", err)
	}

	// Valid request → handler runs.
	good := &allocationpbv1.CreateHoldRequest{
		Hold: &allocationpbv1.Hold{Window: window, Quantity: 2},
	}
	out, err := intercept(context.Background(), good, nil, handler)
	if err != nil || out != "ok" || !handlerCalled {
		t.Fatalf("valid request: out=%v err=%v handlerCalled=%t", out, err, handlerCalled)
	}
}

// One case per service and rule family: the buf.validate annotations must
// reject exactly what the deleted hand-written handler guards rejected —
// missing required fields, malformed resource names, unset oneofs, and
// out-of-range values — and pass well-formed requests through.
func TestValidationInterceptor_Services(t *testing.T) {
	intercept, err := grpc.NewValidationInterceptor()
	if err != nil {
		t.Fatalf("build interceptor: %v", err)
	}
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	window := &interval.Interval{StartTime: timestamppb.Now(), EndTime: timestamppb.Now()}

	cases := []struct {
		name    string
		req     proto.Message
		wantErr bool
	}{
		// allocation
		{"check availability missing window", &allocationpbv1.CheckAvailabilityRequest{}, true},
		{"check availability negative quantity", &allocationpbv1.CheckAvailabilityRequest{Window: window, Quantity: -1}, true},
		{"check availability ok", &allocationpbv1.CheckAvailabilityRequest{Window: window}, false},
		{"batch compute empty", &allocationpbv1.BatchComputeAvailabilityRequest{}, true},
		{"hold get bad name", &allocationpbv1.GetHoldRequest{Name: "rooms/h1"}, true},

		// reservation
		{"reservation create missing hold", &reservationpbv1.CreateReservationRequest{Reservation: &reservationpbv1.Reservation{}}, true},
		{"reservation create bad id", &reservationpbv1.CreateReservationRequest{ReservationId: "Not_Valid", Reservation: &reservationpbv1.Reservation{Hold: "holds/h1"}}, true},
		{"reservation create ok", &reservationpbv1.CreateReservationRequest{Reservation: &reservationpbv1.Reservation{Hold: "holds/h1"}}, false},
		{"reservation reschedule bad hold", &reservationpbv1.RescheduleReservationRequest{Name: "reservations/r1", Hold: "rooms/h2"}, true},
		{"reservation cancel ok", &reservationpbv1.CancelReservationRequest{Name: "reservations/r1"}, false},
		{"reservation update missing reservation", &reservationpbv1.UpdateReservationRequest{}, true},
		{"reservation update bad name", &reservationpbv1.UpdateReservationRequest{Reservation: &reservationpbv1.Reservation{Name: "rooms/r1"}}, true},
		{"reservation update bad customer", &reservationpbv1.UpdateReservationRequest{Reservation: &reservationpbv1.Reservation{Name: "reservations/r1", Customer: "ana"}}, true},
		// No hold: an update need not send what it does not change.
		{"reservation update ok", &reservationpbv1.UpdateReservationRequest{Reservation: &reservationpbv1.Reservation{Name: "reservations/r1", Customer: "users/ana"}}, false},
		{"reservation update clears customer", &reservationpbv1.UpdateReservationRequest{Reservation: &reservationpbv1.Reservation{Name: "reservations/r1"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := intercept(context.Background(), tc.req, nil, handler)
			if tc.wantErr {
				if status.Code(err) != codes.InvalidArgument {
					t.Fatalf("got err %v, want InvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
		})
	}
}
