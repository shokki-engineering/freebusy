// Input/output shapes for the pricing engine.
package quote

import (
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/shared/v1/sharedpbv1"
	"google.golang.org/genproto/googleapis/type/money"
)

// Booking modes (matching the proto/DB enum values).
const (
	ModeNightly  = "NIGHTLY"
	ModeTimeSlot = "TIME_SLOT"
)

// Pricing-unit values a fee can be charged per (matching the proto/DB enum).
const (
	PerBooking = "PER_BOOKING"
	PerNight   = "PER_NIGHT"
	PerPerson  = "PER_PERSON"
)

// Inputs is the provider-neutral pricing request.
type Inputs struct {
	Price        *money.Money // unit base price (nil means no charge configured)
	BookingMode  string       // ModeNightly or ModeTimeSlot
	Nights       int64        // calendar nights, counted in the unit timezone
	Units        int64        // number of units/guests requested (>= 1)
	LosDiscounts []LosDiscount
	Fees         []Fee
	Taxes        []Tax
	Promo        *Promo // optional
}

// LosDiscount is a length-of-stay discount: PercentOff or AmountOff off the base.
type LosDiscount struct {
	MinNights  int32
	PercentOff *int32
	AmountOff  *money.Money
}

// Fee is a charge on top of the base: a flat Amount scaled by PricingUnit, or a
// Percent of the base. Taxable fees join the taxable base.
type Fee struct {
	Code        string
	DisplayName string
	PricingUnit string
	Percent     *int32
	Amount      *money.Money
	Taxable     bool
}

// Tax is a percentage rate on the taxable base.
type Tax struct {
	Code        string
	DisplayName string
	Percent     float64
}

// Promo is a promo-code discount with an optional scope.
type Promo struct {
	Code              string
	DisplayName       string
	PercentOff        *int32
	AmountOff         *money.Money
	ApplicableUnitIDs []string // empty = applies to any unit
	MinSubtotal       *money.Money
}

// Result is the computed cost: the base subtotal, the aggregate discount
// (positive), the final total, and the signed line items behind them.
type Result struct {
	Base       *money.Money
	Discount   *money.Money
	Total      *money.Money
	Components []*sharedpbv1.PriceComponent
}
