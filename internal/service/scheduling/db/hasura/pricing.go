package hasura

import (
	"context"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/propertyql/unitsql"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/service/dbutil"

	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopeapplicableunitsql"
	feesql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/propertyql/feesql"
	losdiscountsql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/propertyql/losdiscountsql"
	taxesql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/propertyql/taxesql"
	"github.com/oh-tarnished/freebusy/internal/service/scheduling/pricing"
	"google.golang.org/genproto/googleapis/type/money"
)

// pricingInputs loads a unit's pricing configuration (base price, fees, taxes,
// LOS discounts) and, when promoID is set, the promo's discount and scope, into
// the provider-neutral pricing.Inputs. The caller fills Nights and Units.
func (r *BookingRepository) pricingInputs(ctx context.Context, unit *unitsql.PropertyUnits, promoID string) (pricing.Inputs, error) {
	in := pricing.Inputs{BookingMode: unit.BookingMode}

	if unit.PriceId != nil {
		m, err := r.money(ctx, *unit.PriceId)
		if err != nil {
			return pricing.Inputs{}, err
		}
		in.Price = m
	}

	fees, err := r.svc.Query.Property.Fees.List(ctx, feesql.List().Where(feesql.UnitId.Eq(unit.Id)))
	if err != nil {
		return pricing.Inputs{}, dbutil.MapHasuraErr(err)
	}
	for i := range fees {
		f := &fees[i]
		var amt *money.Money
		if f.AmountId != nil {
			if amt, err = r.money(ctx, *f.AmountId); err != nil {
				return pricing.Inputs{}, err
			}
		}
		in.Fees = append(in.Fees, pricing.Fee{
			Code:        f.Code,
			DisplayName: repox.Deref(f.DisplayName),
			PricingUnit: repox.Deref(f.PricingUnit),
			Percent:     f.Percent,
			Amount:      amt,
			Taxable:     repox.Deref(f.Taxable),
		})
	}

	taxes, err := r.svc.Query.Property.Taxes.List(ctx, taxesql.List().Where(taxesql.UnitId.Eq(unit.Id)))
	if err != nil {
		return pricing.Inputs{}, dbutil.MapHasuraErr(err)
	}
	for i := range taxes {
		in.Taxes = append(in.Taxes, pricing.Tax{Code: taxes[i].Code, DisplayName: repox.Deref(taxes[i].DisplayName), Percent: dbutil.BigdecimalToFloat(taxes[i].Percent)})
	}

	los, err := r.svc.Query.Property.LosDiscounts.List(ctx, losdiscountsql.List().Where(losdiscountsql.UnitId.Eq(unit.Id)))
	if err != nil {
		return pricing.Inputs{}, dbutil.MapHasuraErr(err)
	}
	for i := range los {
		d := &los[i]
		var amt *money.Money
		if d.AmountOffId != nil {
			if amt, err = r.money(ctx, *d.AmountOffId); err != nil {
				return pricing.Inputs{}, err
			}
		}
		in.LosDiscounts = append(in.LosDiscounts, pricing.LosDiscount{
			MinNights:  d.MinNights,
			PercentOff: d.PercentOff,
			AmountOff:  amt,
		})
	}

	if promoID != "" {
		p, err := r.loadPromo(ctx, promoID)
		if err != nil {
			return pricing.Inputs{}, err
		}
		in.Promo = p
	}
	return in, nil
}

// loadPromo hydrates a promo code's discount and scope into a pricing.Promo.
func (r *BookingRepository) loadPromo(ctx context.Context, promoID string) (*pricing.Promo, error) {
	res, err := r.svc.Query.Promocode.Resource.Get(ctx, promoID)
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	if res == nil {
		return nil, nil
	}
	p := &pricing.Promo{Code: repox.Deref(res.Code), DisplayName: repox.Deref(res.DisplayName)}

	if res.DiscountId != "" {
		d, err := r.svc.Query.Promocode.Discounts.Get(ctx, res.DiscountId)
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		if d != nil {
			p.PercentOff = d.PercentOff
			if d.AmountOffId != nil {
				if p.AmountOff, err = r.money(ctx, *d.AmountOffId); err != nil {
					return nil, err
				}
			}
		}
	}
	if res.ScopeId != nil {
		scope, err := r.svc.Query.Promocode.Scopes.Get(ctx, *res.ScopeId)
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		if scope != nil && scope.MinSubtotalId != nil {
			if p.MinSubtotal, err = r.money(ctx, *scope.MinSubtotalId); err != nil {
				return nil, err
			}
		}
		units, err := r.svc.Query.Promocode.ScopeApplicableUnits.List(ctx, scopeapplicableunitsql.List().Where(scopeapplicableunitsql.ScopeId.Eq(*res.ScopeId)))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		for i := range units {
			p.ApplicableUnitIDs = append(p.ApplicableUnitIDs, units[i].UnitId)
		}
	}
	return p, nil
}

func (r *BookingRepository) money(ctx context.Context, id string) (*money.Money, error) {
	m, err := r.svc.Query.Common.Moneys.Get(ctx, id)
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	return moneyFromSchema(m), nil
}
