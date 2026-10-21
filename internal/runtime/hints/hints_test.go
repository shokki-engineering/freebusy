// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package hints

import (
	"context"
	"strings"
	"testing"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestRewrite(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		method string
		req    proto.Message
		err    error
		want   []string // substrings of the rewritten message
		same   bool     // left as it was
	}{
		{"a bare not found names the resource and how to list",
			"/protobuf.rfc4519.organization.v1.Organizations/GetOrganization",
			&organizationv1.GetOrganizationRequest{Name: "organizations/ghost"},
			status.Error(codes.NotFound, "Not found."),
			[]string{`Organization "organizations/ghost" does not exist`, "ListOrganizations (GET /v1/organizations)"}, false},
		{"a nested resource lists under its parent",
			"/freebusy.reservation.v1.ReservationService/GetReservation",
			&reservationpbv1.GetReservationRequest{Name: "organizationalUnits/s/reservations/r"},
			status.Error(codes.NotFound, "not found"),
			[]string{"ListReservations (GET /v1/organizationalUnits/s/reservations)"}, false},
		{"a stale etag says to read again",
			"/protobuf.rfc4519.organization.v1.Organizations/UpdateOrganization",
			&organizationv1.UpdateOrganizationRequest{Organization: &organizationv1.Organization{Name: "organizations/acme"}},
			status.Error(codes.Aborted, "conflict: Message: row changed"),
			[]string{"changed after you read it", "GetOrganization (GET /v1/organizations/acme)"}, false},
		{"a taken id says to choose another",
			"/protobuf.rfc4519.organization.v1.Organizations/CreateOrganization",
			&organizationv1.CreateOrganizationRequest{OrganizationId: "acme", Organization: &organizationv1.Organization{}},
			status.Error(codes.AlreadyExists, "already exists: duplicate key"),
			[]string{`"organizations/acme" already exists`, "Choose another id"}, false},
		{"undelete of a live record says there is nothing to restore",
			"/protobuf.rfc4519.organization.v1.Organizations/UndeleteOrganization",
			&organizationv1.UndeleteOrganizationRequest{Name: "organizations/acme"},
			status.Error(codes.AlreadyExists, "organizations/acme is not deleted"),
			[]string{"is not deleted, so there is nothing to restore"}, false},
		{"a GET of a custom method says to POST",
			"/protobuf.rfc4519.organization.v1.Organizations/GetOrganization",
			&organizationv1.GetOrganizationRequest{Name: "organizations/acme:undelete"},
			status.Error(codes.NotFound, "not found"),
			[]string{"custom methods are called with POST"}, false},
		{"a not found that already names what is missing is left alone",
			"/freebusy.reservation.v1.ReservationService/GetReservation",
			&reservationpbv1.GetReservationRequest{Name: "organizationalUnits/s/reservations/r"},
			status.Error(codes.NotFound, "Organizational unit \"organizationalUnits/s\" does not exist."),
			nil, true},
		{"an internal error carries no internals",
			"/protobuf.rfc4519.organization.v1.Organizations/CreateOrganization",
			&organizationv1.CreateOrganizationRequest{},
			status.Error(codes.Internal, "ERROR: null value in column \"display_names\""),
			[]string{"failed unexpectedly while handling CreateOrganization", "fault on our side"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(ctx, tc.method, tc.req, tc.err)
			msg := status.Convert(got).Message()
			if tc.same {
				if got != tc.err {
					t.Fatalf("rewrote %q into %q", status.Convert(tc.err).Message(), msg)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q lacks %q", msg, w)
				}
			}
			if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "null value") || strings.Contains(msg, "Message: row") {
				t.Errorf("message leaks the backend: %q", msg)
			}
		})
	}
}

// A service that described its error with its own reason keeps its message.
func TestRewriteKeepsDescribedErrors(t *testing.T) {
	st, _ := status.New(codes.AlreadyExists, "Reservation r already exists and was made from another hold.").
		WithDetails(&errdetails.ErrorInfo{Reason: "RESERVATION_ID_TAKEN"})
	err := st.Err()
	got := Rewrite(context.Background(), "/freebusy.reservation.v1.ReservationService/CreateReservation",
		&reservationpbv1.CreateReservationRequest{Parent: "organizationalUnits/s", ReservationId: "r"}, err)
	if got != err {
		t.Fatalf("rewrote a described error: %v", got)
	}
}

func TestCall(t *testing.T) {
	if got, want := Call("UndeleteOrganization", map[string]string{"name": "organizations/acme"}),
		"UndeleteOrganization (POST /v1/organizations/acme:undelete)"; got != want {
		t.Fatalf("Call = %q, want %q", got, want)
	}
	if got := Call("NoSuchMethod", nil); got != "NoSuchMethod" {
		t.Fatalf("an unknown method renders as %q", got)
	}
}
