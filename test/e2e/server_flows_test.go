// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// The reservation flow of the server e2e suite.
package e2e

import (
	"context"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// reservationFlow drives one resource from free, through a hold and a
// reservation, to cancelled and free again -- and checks it is never sold
// twice on the way.
func reservationFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()

	site := c.site(t, "e2e-"+c.suffix)
	resource := "resources/e2e-" + c.suffix
	if _, err := c.resources.CreateResource(ctx, &resourcev1.CreateResourceRequest{
		ResourceId: "e2e-" + c.suffix,
		Resource:   &resourcev1.Resource{OrganizationalUnit: site, ResourceTypes: []string{"e2e"}},
	}); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Resources.Purge, resource) })

	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(time.Hour)
	window := &interval.Interval{StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(2 * time.Hour))}
	free := func() int32 {
		t.Helper()
		res, err := c.availability.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Parent: site, Window: window})
		if err != nil {
			t.Fatalf("CheckAvailability: %v", err)
		}
		return res.GetFreeCount()
	}
	if n := free(); n != 1 {
		t.Fatalf("before booking: %d free, want 1", n)
	}

	hold, err := c.holds.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: site, Hold: &allocationpbv1.Hold{Window: window}})
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	if hold.GetState() != allocationpbv1.HoldState_HOLD_STATE_ACTIVE || len(hold.GetResources()) != 1 || hold.GetResources()[0] != resource {
		t.Fatalf("hold %v", hold)
	}
	_, err = c.holds.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: site, Hold: &allocationpbv1.Hold{Window: window}})
	wantCode(t, err, codes.FailedPrecondition, "a second hold on a taken resource")

	r, err := c.reservations.CreateReservation(ctx, &reservationpbv1.CreateReservationRequest{
		Parent:      site,
		Reservation: &reservationpbv1.Reservation{Hold: hold.GetName(), Customer: "users/e2e-" + c.suffix},
	})
	if err != nil || r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
		t.Fatalf("CreateReservation: %v (state %v)", err, r.GetState())
	}
	// The user service asks who names one of its users before deleting it:
	// store.v1.ReferenceService answers with this reservation's customer field.
	refs, err := c.references.ListReferrers(ctx, &storepbv1.ListReferrersRequest{
		Parent: "users/e2e-" + c.suffix, Filter: `resource_type = "users.shokki.engineering/User"`,
	})
	if err != nil || len(refs.GetReferrers()) != 1 || refs.GetReferrers()[0].GetReferrer() != r.GetName() ||
		refs.GetReferrers()[0].GetField() != "customer" || refs.GetReferrers()[0].GetOnDelete() != storepbv1.ReferenceOptions_ON_DELETE_ALLOW {
		t.Fatalf("ListReferrers of the customer: %v, %v", refs, err)
	}
	if n := free(); n != 0 {
		t.Fatalf("while reserved: %d free, want 0", n)
	}

	// Update: the customer and attributes change, guarded by the etag; the
	// booking itself does not.
	party, err := structpb.NewStruct(map[string]any{"party": 4})
	if err != nil {
		t.Fatal(err)
	}
	mask := &fieldmaskpb.FieldMask{Paths: []string{"customer", "attributes"}}
	updated, err := c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/e2e-new-" + c.suffix, Attributes: party, Etag: r.GetEtag()},
		UpdateMask:  mask,
	})
	if err != nil {
		t.Fatalf("UpdateReservation: %v", err)
	}
	if updated.GetCustomer() != "users/e2e-new-"+c.suffix || !proto.Equal(updated.GetAttributes(), party) || updated.GetEtag() == r.GetEtag() ||
		updated.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || updated.GetHold() != r.GetHold() ||
		!updated.GetStartTime().AsTime().Equal(r.GetStartTime().AsTime()) {
		t.Fatalf("updated reservation %v", updated)
	}
	if got, err := c.reservations.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: r.GetName()}); err != nil ||
		got.GetCustomer() != updated.GetCustomer() || !proto.Equal(got.GetAttributes(), party) {
		t.Fatalf("GetReservation after update: %v %v", got, err)
	}
	_, err = c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/late", Etag: r.GetEtag()},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"customer"}},
	})
	wantCode(t, err, codes.Aborted, "UpdateReservation with a stale etag")
	_, err = c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Hold: hold.GetName()},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"hold"}},
	})
	wantCode(t, err, codes.InvalidArgument, "UpdateReservation of the hold")
	_, err = c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/no-mask"},
	})
	wantCode(t, err, codes.InvalidArgument, "UpdateReservation without update_mask")
	_, err = c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "not-a-user"},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"customer"}},
	})
	wantCode(t, err, codes.InvalidArgument, "UpdateReservation to a malformed customer")
	if n := free(); n != 0 {
		t.Fatalf("after update: %d free, want 0", n)
	}

	cancelled, err := c.reservations.CancelReservation(ctx, &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Reason: "e2e"})
	if err != nil || cancelled.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED {
		t.Fatalf("CancelReservation: %v (state %v)", err, cancelled.GetState())
	}
	if n := free(); n != 1 {
		t.Fatalf("after cancel: %d free, want 1", n)
	}
	_, err = c.reservations.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
		Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/too-late"},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"customer"}},
	})
	wantCode(t, err, codes.FailedPrecondition, "UpdateReservation of a cancelled reservation")
}

// purge removes what a flow wrote, for good; a failure is logged, not fatal,
// since cleanup must not mask the failure that may have caused it.
func (c *e2eClients) purge(t *testing.T, purge func(context.Context, string) error, name string) {
	t.Helper()
	if err := purge(context.Background(), name); err != nil {
		t.Logf("purge %s: %v", name, err)
	}
}
