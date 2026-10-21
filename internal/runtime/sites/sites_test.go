// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package sites

import (
	"slices"
	"testing"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/protobuf/proto"
)

func TestNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  proto.Message
		want []string
	}{
		{"a parent",
			&reservationpbv1.ListReservationsRequest{Parent: "organizationalUnits/a"},
			[]string{"organizationalUnits/a"}},
		{"a batch's parents, each once",
			&allocationpbv1.BatchComputeAvailabilityRequest{Requests: []*allocationpbv1.ComputeAvailabilityRequest{
				{Parent: "organizationalUnits/a"}, {Parent: "organizationalUnits/b"}, {Parent: "organizationalUnits/a"},
			}},
			[]string{"organizationalUnits/a", "organizationalUnits/b"}},
		{"nothing inside the resource being created, which the service checks by field",
			&resourcev1.CreateResourceRequest{Resource: &resourcev1.Resource{
				OrganizationalUnit: "organizationalUnits/a",
				Description:        "moved from organizations/elsewhere",
			}},
			nil},
		{"a search's organization and its sites",
			&allocationpbv1.SearchAvailabilityRequest{Parent: "organizations/o", OrganizationalUnits: []string{"organizationalUnits/a"}},
			[]string{"organizations/o", "organizationalUnits/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Names(tc.req)
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("Names = %v, want %v", got, want)
			}
		})
	}
}

func TestServiceOf(t *testing.T) {
	if got := serviceOf("/protobuf.rfc4519.organization.v1.Organizations/CreateOrganization"); !exempt[got] {
		t.Fatalf("serviceOf = %q, which is not exempt", got)
	}
	if got := serviceOf("/freebusy.reservation.v1.ReservationService/ListReservations"); exempt[got] {
		t.Fatalf("%q is exempt", got)
	}
}
