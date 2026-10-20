// Promo-code, rate-plan and reservation flows of the server e2e suite.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/pricing/v1/pricingpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/protobuf/rfc9073/resource/v1/resourcepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/genproto/googleapis/type/money"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// promoFlow: create with derived ACTIVE state and the redeemability math.
func promoFlow(t *testing.T, c *e2eClients, _ database.Provider) {
	t.Helper()
	ctx := context.Background()

	code := "E2E" + c.suffix
	promo, err := c.promos.CreatePromoCode(ctx, &promocodepbv1.CreatePromoCodeRequest{
		PromoCode: &promocodepbv1.PromoCode{
			Code:     code,
			Discount: &promocodepbv1.Discount{Amount: &promocodepbv1.Discount_PercentOff{PercentOff: 20}},
		},
	})
	if err != nil {
		t.Fatalf("CreatePromoCode: %v", err)
	}
	t.Cleanup(func() {
		if _, err := c.promos.DeletePromoCode(ctx, &promocodepbv1.DeletePromoCodeRequest{Name: promo.GetName(), Force: true}); err != nil {
			t.Logf("DeletePromoCode: %v", err)
		}
	})
	if promo.GetState() != promocodepbv1.PromoCodeState_PROMO_CODE_STATE_ACTIVE {
		t.Fatalf("promo state = %v, want derived ACTIVE", promo.GetState())
	}

	if _, err := c.promos.ValidatePromoCode(ctx, &promocodepbv1.ValidatePromoCodeRequest{
		Code:     code,
		Subtotal: &money.Money{CurrencyCode: "USD", Units: 100},
	}); err != nil {
		t.Fatalf("ValidatePromoCode: %v", err)
	}
}

// ratePlanFlow: a plan written through the real service reads back intact.
func ratePlanFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()

	plan, err := c.ratePlans.CreateRatePlan(ctx, &pricingpbv1.CreateRatePlanRequest{
		RatePlan: &pricingpbv1.RatePlan{
			Resource:    "resources/e2e-" + c.suffix,
			DisplayName: "E2E Flexible",
			Price:       &money.Money{CurrencyCode: "INR", Units: 5000},
			PricingUnit: pricingpbv1.PricingUnit_PRICING_UNIT_PER_NIGHT,
		},
	})
	if err != nil {
		t.Fatalf("CreateRatePlan: %v", err)
	}
	t.Cleanup(func() {
		if _, err := c.ratePlans.DeleteRatePlan(ctx, &pricingpbv1.DeleteRatePlanRequest{Name: plan.GetName()}); err != nil {
			t.Logf("DeleteRatePlan: %v", err)
		}
	})
	got, err := c.ratePlans.GetRatePlan(ctx, &pricingpbv1.GetRatePlanRequest{Name: plan.GetName()})
	if err != nil || got.GetPrice().GetUnits() != 5000 {
		t.Fatalf("GetRatePlan: %v (price %v)", err, got.GetPrice())
	}
}

// reservationFlow drives one resource from free, through a hold and a
// reservation, to cancelled and free again -- and checks it is never sold
// twice on the way.
func reservationFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()

	site := "organizationalUnits/e2e-" + c.suffix
	resource := "resources/e2e-" + c.suffix
	if _, err := c.resources.Resources.Create(ctx, &resourcepbv1.Resource{
		Name:               resource,
		OrganizationalUnit: site,
		ResourceTypes:      []string{"e2e"},
	}); err != nil {
		t.Fatalf("seed resource: %v", err)
	}
	t.Cleanup(func() {
		if err := c.resources.Resources.Delete(ctx, resource); err != nil {
			t.Logf("delete resource: %v", err)
		}
	})

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
	if n := free(); n != 0 {
		t.Fatalf("while reserved: %d free, want 0", n)
	}

	cancelled, err := c.reservations.CancelReservation(ctx, &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Reason: "e2e"})
	if err != nil || cancelled.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED {
		t.Fatalf("CancelReservation: %v (state %v)", err, cancelled.GetState())
	}
	if n := free(); n != 1 {
		t.Fatalf("after cancel: %d free, want 1", n)
	}
}
