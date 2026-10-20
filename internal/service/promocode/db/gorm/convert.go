package gorm

import (
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/common"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/promocode"
	"github.com/oh-tarnished/freebusy/internal/service/promocode/discount"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/runtime-go/ulid"
	"google.golang.org/genproto/googleapis/type/money"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// This file holds the pure (side-effect-free) conversions between the protobuf
// PromoCode and the normalized GORM storage models. The protobuf API nests the
// discount, redemption window, usage limits, and scope as sub-messages; the
// schema stores each as its own belongs-to child table (promocode.discounts,
// promocode.redemption_windows, promocode.usage_limits, promocode.scopes) with a
// foreign key on promocode.resource. Money value-objects normalize into the
// shared common.moneys table, and a scope's applicable resources / offerings
// become join rows. The join columns store the full API resource name verbatim
// so the list values round-trip exactly.

// strOrNil maps an empty proto string (which cannot represent NULL) to a nil
// column pointer, so unset optional strings stay NULL in the database.
func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func tsToTime(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// moneyToModel builds a new Money row (with a fresh ULID id) from a proto Money,
// or returns nil when m is nil.
func moneyToModel(m *money.Money) *common.Money {
	if m == nil {
		return nil
	}
	return &common.Money{
		ID:           ulid.GenerateString(),
		CurrencyCode: strOrNil(m.GetCurrencyCode()),
		Units:        repox.Ptr(m.GetUnits()),
		Nanos:        repox.Ptr(m.GetNanos()),
	}
}

func int64Wrapper(w *wrapperspb.Int64Value) *int64 {
	if w == nil {
		return nil
	}
	return repox.Ptr(w.GetValue())
}

func int32Wrapper(w *wrapperspb.Int32Value) *int32 {
	if w == nil {
		return nil
	}
	return repox.Ptr(w.GetValue())
}

// promoGraph is the full set of rows a single PromoCode materializes into: the
// resource row plus its belongs-to children, the Money rows they reference, and
// the scope's applicable-resources / -offerings join rows. The repository
// persists it in one transaction (moneys and children before the resource that
// references them; join rows after the scope they belong to).
type promoGraph struct {
	promo    *promocode.PromoCode
	discount *promocode.Discount
	window   *promocode.RedemptionWindow
	limits   *promocode.UsageLimits
	scope    *promocode.Scope
	moneys   []*common.Money
}

// buildGraph turns a proto PromoCode into the row graph that backs it, minting a
// fresh ULID for every row and wiring the foreign keys. Identity (ID/Name/Etag)
// of the resource row is left to the caller, which owns id assignment and the
// transaction.
func buildGraph(pc *promocodepbv1.PromoCode) *promoGraph {
	g := &promoGraph{}

	state := promocode.PromoCodeStateActive
	if pc.GetDisabled() {
		state = promocode.PromoCodeStateDisabled
	}
	g.promo = &promocode.PromoCode{
		Code:           repox.Ptr(pc.GetCode()),
		DisplayName:    strOrNil(pc.GetDisplayName()),
		Description:    strOrNil(pc.GetDescription()),
		State:          &state,
		Disabled:       repox.Ptr(pc.GetDisabled()),
		CodeGeneration: promocode.CodeGenerationPtrFromProto(pc.GetCodeGeneration()),
	}

	// Discount is required: always materialize a row. A non-nil amount_off is the
	// fixed-amount arm; otherwise it's the percentage arm.
	g.discount = &promocode.Discount{ID: ulid.GenerateString()}
	if amt := moneyToModel(pc.GetDiscount().GetAmountOff()); amt != nil {
		g.moneys = append(g.moneys, amt)
		g.discount.AmountOffID = &amt.ID
		g.discount.AmountCase = repox.Ptr(promocode.DiscountAmountCaseAmountOff)
	} else {
		g.discount.PercentOff = repox.Ptr(pc.GetDiscount().GetPercentOff())
		g.discount.AmountCase = repox.Ptr(promocode.DiscountAmountCasePercentOff)
	}
	g.promo.DiscountID = g.discount.ID

	if w := pc.GetWindow(); w != nil {
		g.window = &promocode.RedemptionWindow{
			ID:        ulid.GenerateString(),
			StartTime: tsToTime(w.GetStartTime()),
			EndTime:   tsToTime(w.GetEndTime()),
		}
		g.promo.WindowID = &g.window.ID
	}

	if l := pc.GetLimits(); l != nil {
		g.limits = &promocode.UsageLimits{
			ID:               ulid.GenerateString(),
			MaxRedemptions:   int64Wrapper(l.GetMaxRedemptions()),
			PerCustomerLimit: int32Wrapper(l.GetPerCustomerLimit()),
		}
		g.promo.LimitsID = &g.limits.ID
	}

	if sc := pc.GetScope(); sc != nil {
		g.scope = &promocode.Scope{ID: ulid.GenerateString()}
		if min := moneyToModel(sc.GetMinSubtotal()); min != nil {
			g.moneys = append(g.moneys, min)
			g.scope.MinSubtotalID = &min.ID
		}
		// applicable_properties / applicable_units are plain text[] columns now
		// that they name resources another service owns: a cross-service
		// reference cannot be a join table, because there is no local table to
		// join to. The generated converter maps both arrays, so nothing is
		// assembled by hand here.
		g.scope.ApplicableProperties = sc.GetApplicableProperties()
		g.scope.ApplicableUnits = sc.GetApplicableUnits()
		g.promo.ScopeID = &g.scope.ID
	}

	return g
}

// fromModel assembles the protobuf PromoCode from a stored resource row and its
// preloaded associations. The generated converter covers the flat fields and
// the whole belongs-to graph (discount + amount money, window, limits, scope +
// min money), including the scope's applicable-name arrays; only the derived
// state is layered on here.
func fromModel(m *promocode.PromoCode) *promocodepbv1.PromoCode {
	pc := promocode.PromoCodeToProto(m)
	// Derive the lifecycle state from the window/flags rather than trusting the
	// possibly-stale stored value (a code becomes EXPIRED purely with time).
	pc.State = discount.EffectiveState(pc, time.Now().UTC())
	return pc
}
