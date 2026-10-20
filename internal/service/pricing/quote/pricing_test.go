package quote

import (
	"testing"

	"google.golang.org/genproto/googleapis/type/money"
)

func inr(units int64) *money.Money { return &money.Money{CurrencyCode: "INR", Units: units} }

func i32(v int32) *int32 { return &v }

// 3 nights @ ₹5000, 10% LOS discount, ₹500 taxable cleaning fee, 12% GST:
// base 15000, −1500 LOS = 13500, +500 fee = 14000 taxable, +1680 tax = 15680.
func TestComputeFullStack(t *testing.T) {
	in := Inputs{
		Price:        inr(5000),
		BookingMode:  ModeNightly,
		Nights:       3,
		Units:        1,
		LosDiscounts: []LosDiscount{{MinNights: 3, PercentOff: i32(10)}},
		Fees:         []Fee{{Code: "cleaning_fee", PricingUnit: PerBooking, Amount: inr(500), Taxable: true}},
		Taxes:        []Tax{{Code: "gst", Percent: 12}},
	}
	r := Compute(in, "u1")
	if r.Base.GetUnits() != 15000 || r.Discount.GetUnits() != 1500 || r.Total.GetUnits() != 15680 {
		t.Fatalf("base=%d discount=%d total=%d, want 15000/1500/15680", r.Base.GetUnits(), r.Discount.GetUnits(), r.Total.GetUnits())
	}
}

// A promo scoped to other units does not apply; total stays the base.
func TestComputePromoOutOfScope(t *testing.T) {
	in := Inputs{
		Price:       inr(5000),
		BookingMode: ModeNightly,
		Nights:      2,
		Units:       1,
		Promo:       &Promo{Code: "SUITE", PercentOff: i32(50), ApplicableUnitIDs: []string{"u2"}},
	}
	r := Compute(in, "u1")
	if !IsZero(r.Discount) || r.Total.GetUnits() != 10000 {
		t.Fatalf("discount=%d total=%d, want 0/10000", r.Discount.GetUnits(), r.Total.GetUnits())
	}
}

// A time-slot booking ignores nights and LOS discounts (single-slot base).
func TestComputeTimeSlot(t *testing.T) {
	in := Inputs{
		Price:        inr(800),
		BookingMode:  ModeTimeSlot,
		Nights:       5, // ignored for time-slot
		Units:        1,
		LosDiscounts: []LosDiscount{{MinNights: 2, PercentOff: i32(20)}},
	}
	r := Compute(in, "u1")
	if r.Base.GetUnits() != 800 || !IsZero(r.Discount) || r.Total.GetUnits() != 800 {
		t.Fatalf("base=%d discount=%d total=%d, want 800/0/800", r.Base.GetUnits(), r.Discount.GetUnits(), r.Total.GetUnits())
	}
}
