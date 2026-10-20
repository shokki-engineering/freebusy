package gorm

import (
	"context"
	"errors"

	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/common"
	pricing_model "github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/pricing"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/promocode"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/service/scheduling/pricing"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/shared/v1/sharedpbv1"
	"google.golang.org/genproto/googleapis/type/money"
	"gorm.io/gorm"
)

// This file adapts the GORM unit/promo models into the provider-neutral
// internal/service/booking/pricing engine, so the GORM and Hasura repositories
// share one pricing implementation.

// pricingResult mirrors pricing.Result with the field names the repository uses.
type pricingResult struct {
	base       *money.Money
	discount   *money.Money
	total      *money.Money
	components []*sharedpbv1.PriceComponent
}

// computePricing builds the price breakdown for a booking of `nights` nights and
// `units` units under `plan`, applying LOS + promo discounts, fees, then taxes.
// `plan` must have Price, LosDiscounts, Fees, and Taxes preloaded; `promo` (with
// Discount + Scope preloaded) is optional.
//
// Prices come from a RatePlan rather than the old Unit: pricing moved to
// freebusy.pricing.v1 when Unit became an RFC 9073 resource, which describes a
// bookable thing and deliberately carries no money.
func computePricing(plan *pricing_model.RatePlan, nights, units int64, promo *promocode.PromoCode) pricingResult {
	in := pricing.Inputs{
		Price:       common.MoneyToProto(plan.Price),
		BookingMode: bookingModeOf(plan),
		Nights:      nights,
		Units:       units,
	}
	for i := range plan.LosDiscounts {
		d := &plan.LosDiscounts[i]
		in.LosDiscounts = append(in.LosDiscounts, pricing.LosDiscount{
			MinNights:  d.MinNights,
			PercentOff: d.PercentOff,
			AmountOff:  common.MoneyToProto(d.AmountOff),
		})
	}
	for i := range plan.Fees {
		f := &plan.Fees[i]
		pu := ""
		if f.PricingUnit != nil {
			pu = string(*f.PricingUnit)
		}
		in.Fees = append(in.Fees, pricing.Fee{
			Code:        f.Code,
			DisplayName: repox.Deref(f.DisplayName),
			PricingUnit: pu,
			Percent:     f.Percent,
			Amount:      common.MoneyToProto(f.Amount),
			Taxable:     repox.Deref(f.Taxable),
		})
	}
	for i := range plan.Taxes {
		t := &plan.Taxes[i]
		in.Taxes = append(in.Taxes, pricing.Tax{Code: t.Code, DisplayName: repox.Deref(t.DisplayName), Percent: t.Percent})
	}
	if promo != nil {
		p := &pricing.Promo{Code: repox.Deref(promo.Code), DisplayName: repox.Deref(promo.DisplayName)}
		if promo.Discount != nil {
			p.PercentOff = promo.Discount.PercentOff
			p.AmountOff = common.MoneyToProto(promo.Discount.AmountOff)
		}
		if promo.Scope != nil {
			p.MinSubtotal = common.MoneyToProto(promo.Scope.MinSubtotal)
			for i := range promo.Scope.ApplicableUnits {
				p.ApplicableUnitIDs = append(p.ApplicableUnitIDs, promo.Scope.ApplicableUnits[i])
			}
		}
		in.Promo = p
	}

	r := pricing.Compute(in, plan.ID)
	return pricingResult{base: r.Base, discount: r.Discount, total: r.Total, components: r.Components}
}

func isZeroMoney(m *money.Money) bool { return pricing.IsZero(m) }

// bookingModeOf maps a plan's PricingUnit onto the engine's booking mode.
//
// The two say the same thing from different sides: the engine multiplies by
// nights and applies length-of-stay discounts exactly when a price is charged
// per night. Unit.BookingMode used to carry this; PER_NIGHT carries it now, and
// keeping the engine's own vocabulary means its rules and tests did not have to
// move with the schema.
func bookingModeOf(plan *pricing_model.RatePlan) string {
	if plan.PricingUnit != nil && *plan.PricingUnit == pricing_model.PricingUnitPerNight {
		return pricing.ModeNightly
	}
	return pricing.ModeTimeSlot
}

// loadRatePlan fetches the active rate plan for a bookable resource, with the
// money graph the pricing engine needs preloaded.
//
// Keyed by resource *name* rather than by a local id: the resource is an RFC
// 9073 VRESOURCE owned by another service, so there is no row here to join to —
// the name is the only handle both sides share. That is the same reason the
// generated store kept `resource` a plain indexed column instead of a foreign
// key.
//
// Returns (nil, nil) when a resource has no plan. That is a real state, not a
// corruption, and it must not fail the booking: an unpriced resource is a free
// one — an internal meeting room, a shared desk — and refusing to book it would
// make pricing a precondition of scheduling, which is exactly the coupling
// moving money into its own package was meant to remove. Callers skip the price
// breakdown when the plan is nil and book at no charge.
func loadRatePlan(ctx context.Context, db *gorm.DB, resourceName string) (*pricing_model.RatePlan, error) {
	var plan pricing_model.RatePlan
	if err := db.WithContext(ctx).
		Preload("Price").
		Preload("Fees").Preload("Fees.Amount").
		Preload("Taxes").
		Preload("LosDiscounts").Preload("LosDiscounts.AmountOff").
		First(&plan, "resource = ?", resourceName).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, repox.MapGormErr(err)
	}
	return &plan, nil
}
