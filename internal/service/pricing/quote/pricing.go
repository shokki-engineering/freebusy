// Package quote is the provider-neutral pricing engine: from a rate plan's
// price, length-of-stay discounts, fees and taxes -- plus an optional promo
// code -- it computes the itemized breakdown (price_components), the aggregate
// discount and the final total. It speaks in google.type.Money and the shared
// PriceComponent proto. Money is summed in nanos to keep the units/nanos split
// exact.
//
// It moved here from the retired hotel booking service, which was its only
// caller, so that QuoteService can be built on it. Nothing serves it yet.
package quote

import (
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/shared/v1/sharedpbv1"
	"google.golang.org/genproto/googleapis/type/money"
)

// Compute builds the price breakdown, applying (in order) the length-of-stay
// discount, the promo discount, fees, then taxes. UnitID scopes the promo's
// applicable-units allow-list.
func Compute(in Inputs, unitID string) Result {
	currency := in.Price.GetCurrencyCode()
	nightMul := int64(1)
	if in.BookingMode == ModeNightly {
		nightMul = in.Nights
	}

	base := mul(in.Price, nightMul)
	components := []*sharedpbv1.PriceComponent{
		component(sharedpbv1.PriceComponent_TYPE_BASE, "base", "Base charge", base),
	}

	// --- discounts (subtracted from the base subtotal) ---
	discountTotal := fromNanos(currency, 0)
	if los := bestLos(in, in.Nights); los != nil {
		amt := losAmount(los, base)
		if !isZero(amt) {
			discountTotal = add(discountTotal, amt)
			components = append(components, component(
				sharedpbv1.PriceComponent_TYPE_DISCOUNT, "los_discount", "Length-of-stay discount", neg(amt)))
		}
	}

	afterLos := sub(base, discountTotal)
	if in.Promo != nil && promoApplies(in.Promo, unitID, afterLos) {
		amt := promoAmount(in.Promo, afterLos)
		if !isZero(amt) {
			discountTotal = add(discountTotal, amt)
			components = append(components, component(
				sharedpbv1.PriceComponent_TYPE_DISCOUNT, in.Promo.Code, promoLabel(in.Promo), neg(amt)))
		}
	}

	netSubtotal := sub(base, discountTotal)

	// --- fees (added on top; some are taxable) ---
	feesTotal := fromNanos(currency, 0)
	taxableFees := fromNanos(currency, 0)
	for i := range in.Fees {
		f := &in.Fees[i]
		amt := feeAmount(f, base, in.Nights, in.Units)
		if isZero(amt) {
			continue
		}
		feesTotal = add(feesTotal, amt)
		if f.Taxable {
			taxableFees = add(taxableFees, amt)
		}
		components = append(components, component(
			sharedpbv1.PriceComponent_TYPE_FEE, f.Code, f.DisplayName, amt))
	}

	// --- taxes (on the net subtotal plus taxable fees) ---
	taxableBase := add(netSubtotal, taxableFees)
	taxesTotal := fromNanos(currency, 0)
	for i := range in.Taxes {
		t := &in.Taxes[i]
		amt := pctFloat(taxableBase, t.Percent)
		if isZero(amt) {
			continue
		}
		taxesTotal = add(taxesTotal, amt)
		components = append(components, component(
			sharedpbv1.PriceComponent_TYPE_TAX, t.Code, t.DisplayName, amt))
	}

	total := add(add(netSubtotal, feesTotal), taxesTotal)
	return Result{Base: base, Discount: discountTotal, Total: total, Components: components}
}

// bestLos returns the length-of-stay discount with the largest min_nights
// satisfied by nights (nightly stays only), or nil.
func bestLos(in Inputs, nights int64) *LosDiscount {
	if in.BookingMode != ModeNightly {
		return nil
	}
	var best *LosDiscount
	for i := range in.LosDiscounts {
		d := &in.LosDiscounts[i]
		if int64(d.MinNights) > nights {
			continue
		}
		if best == nil || d.MinNights > best.MinNights {
			best = d
		}
	}
	return best
}

func losAmount(d *LosDiscount, base *money.Money) *money.Money {
	if d.PercentOff != nil {
		return pct(base, *d.PercentOff)
	}
	if d.AmountOff != nil {
		return d.AmountOff
	}
	return fromNanos(base.GetCurrencyCode(), 0)
}

func promoApplies(p *Promo, unitID string, subtotal *money.Money) bool {
	if len(p.ApplicableUnitIDs) > 0 {
		ok := false
		for _, id := range p.ApplicableUnitIDs {
			if id == unitID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if p.MinSubtotal != nil && nanos(subtotal) < nanos(p.MinSubtotal) {
		return false
	}
	return true
}

func promoAmount(p *Promo, subtotal *money.Money) *money.Money {
	if p.PercentOff != nil {
		return pct(subtotal, *p.PercentOff)
	}
	if p.AmountOff != nil {
		// A flat discount never exceeds the subtotal it applies to.
		if nanos(p.AmountOff) > nanos(subtotal) {
			return subtotal
		}
		return p.AmountOff
	}
	return fromNanos(subtotal.GetCurrencyCode(), 0)
}

func promoLabel(p *Promo) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return "Promo code " + p.Code
}

func feeAmount(f *Fee, base *money.Money, nights, units int64) *money.Money {
	if f.Amount != nil {
		return mul(f.Amount, feeMultiplier(f.PricingUnit, nights, units))
	}
	if f.Percent != nil {
		return pct(base, *f.Percent)
	}
	return fromNanos(base.GetCurrencyCode(), 0)
}

func feeMultiplier(pu string, nights, units int64) int64 {
	switch pu {
	case PerNight:
		return nights
	case PerPerson:
		if units < 1 {
			return 1
		}
		return units
	default:
		return 1
	}
}

func component(t sharedpbv1.PriceComponent_Type, code, label string, amount *money.Money) *sharedpbv1.PriceComponent {
	return &sharedpbv1.PriceComponent{Type: t, Code: code, DisplayName: label, Amount: amount}
}
