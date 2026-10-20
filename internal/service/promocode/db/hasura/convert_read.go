// Read-side assembly: rows back to the PromoCode proto.
package hasura

import (
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/commonql/moneysql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/discountsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/redemptionwindowsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/resourceql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopeapplicablepropertiesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopeapplicableunitsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/usagelimitsql"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"time"

	"github.com/oh-tarnished/freebusy/internal/service/promocode/discount"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// parts holds a stored resource row and the child rows fetched to hydrate it.
type parts struct {
	res        *resourceql.PromocodeResource
	discount   *discountsql.PromocodeDiscounts
	amountOff  *moneysql.CommonMoneys
	window     *redemptionwindowsql.PromocodeRedemptionWindows
	limits     *usagelimitsql.PromocodeUsageLimits
	scope      *scopesql.PromocodeScopes
	minSub     *moneysql.CommonMoneys
	properties []scopeapplicablepropertiesql.PromocodeScopeApplicableProperties
	units      []scopeapplicableunitsql.PromocodeScopeApplicableUnits
}

// fromParts assembles the protobuf PromoCode from a stored resource row and its
// fetched child rows.
func fromParts(p parts) *promocodepbv1.PromoCode {
	res := p.res
	pc := &promocodepbv1.PromoCode{
		Name:            res.Name,
		Code:            repox.Deref(res.Code),
		CodeGeneration:  codeGenerationValue(repox.Deref(res.CodeGeneration)),
		DisplayName:     repox.Deref(res.DisplayName),
		Description:     repox.Deref(res.Description),
		Discount:        discountFromModel(p.discount, p.amountOff),
		Window:          windowFromModel(p.window),
		Limits:          limitsFromModel(p.limits),
		Scope:           scopeFromParts(p.scope, p.minSub, p.properties, p.units),
		RedemptionCount: int64(repox.Deref(res.RedemptionCount)),
		Disabled:        repox.Deref(res.Disabled),
		CreateTime:      strToTS(&res.CreateTime),
		UpdateTime:      strToTS(&res.UpdateTime),
		Etag:            repox.Deref(res.Etag),
	}
	// Derive the lifecycle state from the window/flags rather than trusting the
	// possibly-stale stored value (a code becomes EXPIRED purely with time).
	pc.State = discount.EffectiveState(pc, time.Now().UTC())
	return pc
}

func discountFromModel(d *discountsql.PromocodeDiscounts, amountOff *moneysql.CommonMoneys) *promocodepbv1.Discount {
	if d == nil {
		return nil
	}
	out := &promocodepbv1.Discount{}
	if d.AmountCase != nil && *d.AmountCase == amountCaseAmountOff {
		out.Amount = &promocodepbv1.Discount_AmountOff{AmountOff: moneyFromModel(amountOff)}
	} else {
		out.Amount = &promocodepbv1.Discount_PercentOff{PercentOff: repox.Deref(d.PercentOff)}
	}
	return out
}

func windowFromModel(w *redemptionwindowsql.PromocodeRedemptionWindows) *promocodepbv1.RedemptionWindow {
	if w == nil {
		return nil
	}
	return &promocodepbv1.RedemptionWindow{
		StartTime: strToTS(w.StartTime),
		EndTime:   strToTS(w.EndTime),
	}
}

func limitsFromModel(l *usagelimitsql.PromocodeUsageLimits) *promocodepbv1.UsageLimits {
	if l == nil {
		return nil
	}
	out := &promocodepbv1.UsageLimits{}
	if l.MaxRedemptions != nil {
		out.MaxRedemptions = wrapperspb.Int64(int64(*l.MaxRedemptions))
	}
	if l.PerCustomerLimit != nil {
		out.PerCustomerLimit = wrapperspb.Int32(*l.PerCustomerLimit)
	}
	return out
}

func scopeFromParts(s *scopesql.PromocodeScopes, minSub *moneysql.CommonMoneys, res []scopeapplicablepropertiesql.PromocodeScopeApplicableProperties, off []scopeapplicableunitsql.PromocodeScopeApplicableUnits) *promocodepbv1.Scope {
	if s == nil {
		return nil
	}
	out := &promocodepbv1.Scope{MinSubtotal: moneyFromModel(minSub)}
	for i := range res {
		out.ApplicableProperties = append(out.ApplicableProperties, res[i].PropertyId)
	}
	for i := range off {
		out.ApplicableUnits = append(out.ApplicableUnits, off[i].UnitName)
	}
	return out
}

// codeGenerationValue reads the stored CodeGeneration ("MANUAL", "AUTO"); a
// NULL from a code created before the column existed reads as UNSPECIFIED.
func codeGenerationValue(s string) promocodepbv1.CodeGeneration {
	return promocodepbv1.CodeGeneration(promocodepbv1.CodeGeneration_value["CODE_GENERATION_"+s])
}
