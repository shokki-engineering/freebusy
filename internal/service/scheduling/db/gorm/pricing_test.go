package gorm

import (
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"testing"

	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/common"
	pricing_model "github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/pricing"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/promocode"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/shared/v1/sharedpbv1"
)

func inr(units int64) *common.Money {
	return &common.Money{CurrencyCode: repox.Ptr("INR"), Units: repox.Ptr(units), Nanos: repox.Ptr(int32(0))}
}

func componentByCode(cs []*sharedpbv1.PriceComponent, code string) *sharedpbv1.PriceComponent {
	for _, c := range cs {
		if c.GetCode() == code {
			return c
		}
	}
	return nil
}

// perNight is the pricing unit that makes bookingModeOf report ModeNightly —
// the fixtures' stand-in for the old Unit.BookingMode.
var perNight = pricing_model.PricingUnitPerNight

// A 3-night stay at ₹5000/night with a 10% length-of-stay discount, a flat
// ₹500 cleaning fee (taxable), and 12% GST. Base 15000, −1500 LOS = 13500,
// +500 fee = 14000 taxable, +1680 tax = 15680 total.
func TestComputePricingFullStack(t *testing.T) {
	cleaning := "Cleaning fee"
	perBooking := pricing_model.PricingUnitPerBooking
	unit := &pricing_model.RatePlan{
		PricingUnit: &perNight,
		Price:       inr(5000),
		LosDiscounts: []pricing_model.LosDiscount{
			{MinNights: 3, PercentOff: repox.Ptr(int32(10))},
		},
		Fees: []pricing_model.Fee{
			{Code: "cleaning_fee", DisplayName: &cleaning, Amount: inr(500), Taxable: repox.Ptr(true), PricingUnit: &perBooking},
		},
		Taxes: []pricing_model.Tax{
			{Code: "gst", Percent: 12},
		},
	}

	p := computePricing(unit, 3 /*nights*/, 1 /*units*/, nil)

	if p.base.GetUnits() != 15000 {
		t.Fatalf("base = %d, want 15000", p.base.GetUnits())
	}
	if p.discount.GetUnits() != 1500 {
		t.Fatalf("discount = %d, want 1500", p.discount.GetUnits())
	}
	if p.total.GetUnits() != 15680 {
		t.Fatalf("total = %d, want 15680", p.total.GetUnits())
	}
	if los := componentByCode(p.components, "los_discount"); los == nil || los.GetAmount().GetUnits() != -1500 {
		t.Fatalf("los component wrong: %+v", los)
	}
	if fee := componentByCode(p.components, "cleaning_fee"); fee == nil || fee.GetAmount().GetUnits() != 500 {
		t.Fatalf("fee component wrong: %+v", fee)
	}
	if tax := componentByCode(p.components, "gst"); tax == nil || tax.GetAmount().GetUnits() != 1680 {
		t.Fatalf("tax component wrong: %+v", tax)
	}
}

// A promo code stacks after the LOS discount: base 15000, −1500 LOS = 13500,
// then 20% promo off 13500 = −2700, total 10800 (no fees/taxes here).
func TestComputePricingWithPromo(t *testing.T) {
	unit := &pricing_model.RatePlan{
		PricingUnit: &perNight,
		Price:       inr(5000),
		LosDiscounts: []pricing_model.LosDiscount{
			{MinNights: 3, PercentOff: repox.Ptr(int32(10))},
		},
	}
	promo := &promocode.PromoCode{
		Code:     repox.Ptr("SAVE20"),
		Discount: &promocode.Discount{PercentOff: repox.Ptr(int32(20))},
	}

	p := computePricing(unit, 3, 1, promo)

	if p.discount.GetUnits() != 4200 { // 1500 + 2700
		t.Fatalf("discount = %d, want 4200", p.discount.GetUnits())
	}
	if p.total.GetUnits() != 10800 {
		t.Fatalf("total = %d, want 10800", p.total.GetUnits())
	}
	if promoC := componentByCode(p.components, "SAVE20"); promoC == nil || promoC.GetAmount().GetUnits() != -2700 {
		t.Fatalf("promo component wrong: %+v", promoC)
	}
}

// A promo scoped to specific units does not apply to a unit outside the list.
func TestComputePricingPromoScopeExcludesUnit(t *testing.T) {
	unit := &pricing_model.RatePlan{
		ID:          "u1",
		PricingUnit: &perNight,
		Price:       inr(5000),
	}
	promo := &promocode.PromoCode{
		Code:     repox.Ptr("SUITEONLY"),
		Discount: &promocode.Discount{PercentOff: repox.Ptr(int32(50))},
		Scope: &promocode.Scope{
			ApplicableUnits: []string{"u2"},
		},
	}

	p := computePricing(unit, 2, 1, promo)

	if !isZeroMoney(p.discount) {
		t.Fatalf("discount = %d, want 0 (promo out of scope)", p.discount.GetUnits())
	}
	if p.total.GetUnits() != 10000 {
		t.Fatalf("total = %d, want 10000", p.total.GetUnits())
	}
}
