package hasura

import (
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/service/dbutil"
	"strings"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/commonql/moneysql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/discountsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/redemptionwindowsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/resourceql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopeapplicablepropertiesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopeapplicableunitsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/scopesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/promocodeql/usagelimitsql"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/runtime-go/ulid"
	"github.com/the-protobuf-project/runtime-go/network/graphql"
	"google.golang.org/genproto/googleapis/type/money"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file holds the pure conversions between the protobuf PromoCode and the
// normalized Hasura/GraphQL schema. The protobuf API nests the discount,
// redemption window, usage limits, and scope as sub-messages; the schema stores
// each as its own table (promocode.discounts, .redemption_windows, .usage_limits,
// .scopes) referenced by a foreign key on promocode.resource. Money value-objects
// normalize into common.moneys, and a scope's applicable resources / offerings
// become join rows. Timestamps cross the GraphQL boundary as RFC 3339 strings.

// State strings persisted in the resource.state column (match the DB CHECK).
const (
	stateActive   = "ACTIVE"
	stateDisabled = "DISABLED"
	stateExpired  = "EXPIRED"
)

// Discriminator values for the discount.amount_case column.
const (
	amountCasePercentOff = "PERCENT_OFF"
	amountCaseAmountOff  = "AMOUNT_OFF"
)

// strToTS parses an RFC 3339 timestamptz string from Hasura back into a protobuf
// timestamp, tolerating the few layouts the engine emits. A blank/unparseable
// value yields nil.
func strToTS(s *string) *timestamppb.Timestamp {
	if s == nil || *s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return timestamppb.New(t)
		}
	}
	return nil
}

// moneyInput builds a common.moneys insert with the given id.
func moneyInput(id string, m *money.Money) moneysql.CreateInput {
	return moneysql.CreateInput{
		Id:           id,
		CurrencyCode: m.GetCurrencyCode(),
		Units:        graphql.Int64(m.GetUnits()),
		Nanos:        m.GetNanos(),
	}
}

func moneyFromModel(m *moneysql.CommonMoneys) *money.Money {
	if m == nil {
		return nil
	}
	return &money.Money{
		CurrencyCode: repox.Deref(m.CurrencyCode),
		Units:        int64(repox.Deref(m.Units)),
		Nanos:        repox.Deref(m.Nanos),
	}
}

// graph is the full set of GraphQL inserts a single PromoCode materializes into:
// the resource row plus its child rows, the Money rows they reference, and the
// scope's applicable join rows. The repository commits it as one atomic Tx batch
// (Money rows and children before the resource that references them; join rows
// after the scope they belong to).
type graph struct {
	resource   resourceql.CreateInput
	discount   discountsql.CreateInput
	window     *redemptionwindowsql.CreateInput
	limits     *usagelimitsql.CreateInput
	scope      *scopesql.CreateInput
	moneys     []moneysql.CreateInput
	properties []scopeapplicablepropertiesql.CreateInput
	units      []scopeapplicableunitsql.CreateInput
}

// buildGraph turns a proto PromoCode into the insert graph that backs it, minting
// a fresh ULID for every row and wiring the foreign keys. Identity (Id/Name/Etag)
// of the resource row is left to the caller. now stamps create/update time.
func buildGraph(pc *promocodepbv1.PromoCode, now time.Time) *graph {
	g := &graph{}

	state := stateActive
	if pc.GetDisabled() {
		state = stateDisabled
	}
	nowStr := dbutil.TsToStr(timestamppb.New(now))
	g.resource = resourceql.CreateInput{
		Code:           pc.GetCode(),
		CodeGeneration: codeGenerationName(pc.GetCodeGeneration()),
		DisplayName:    pc.GetDisplayName(),
		Description:    pc.GetDescription(),
		Disabled:       pc.GetDisabled(),
		State:          state,
		CreateTime:     nowStr,
		UpdateTime:     nowStr,
	}

	// Discount is required: always materialize a row. A non-nil amount_off is the
	// fixed-amount arm; otherwise it's the percentage arm.
	dID := ulid.GenerateString()
	g.discount = discountsql.CreateInput{Id: dID}
	if amt := pc.GetDiscount().GetAmountOff(); amt != nil {
		mID := ulid.GenerateString()
		g.moneys = append(g.moneys, moneyInput(mID, amt))
		g.discount.AmountOffId = mID
		g.discount.AmountCase = amountCaseAmountOff
	} else {
		g.discount.PercentOff = pc.GetDiscount().GetPercentOff()
		g.discount.AmountCase = amountCasePercentOff
	}
	g.resource.DiscountId = dID

	if w := pc.GetWindow(); w != nil {
		wID := ulid.GenerateString()
		g.window = &redemptionwindowsql.CreateInput{
			Id:        wID,
			StartTime: dbutil.TsToStr(w.GetStartTime()),
			EndTime:   dbutil.TsToStr(w.GetEndTime()),
		}
		g.resource.WindowId = wID
	}

	if l := pc.GetLimits(); l != nil {
		lID := ulid.GenerateString()
		ul := usagelimitsql.CreateInput{Id: lID}
		if l.GetMaxRedemptions() != nil {
			ul.MaxRedemptions = graphql.Int64(l.GetMaxRedemptions().GetValue())
		}
		if l.GetPerCustomerLimit() != nil {
			ul.PerCustomerLimit = l.GetPerCustomerLimit().GetValue()
		}
		g.limits = &ul
		g.resource.LimitsId = lID
	}

	if sc := pc.GetScope(); sc != nil {
		sID := ulid.GenerateString()
		scp := scopesql.CreateInput{Id: sID}
		if min := sc.GetMinSubtotal(); min != nil {
			mID := ulid.GenerateString()
			g.moneys = append(g.moneys, moneyInput(mID, min))
			scp.MinSubtotalId = mID
		}
		g.scope = &scp
		g.resource.ScopeId = sID
		for _, name := range sc.GetApplicableProperties() {
			g.properties = append(g.properties, scopeapplicablepropertiesql.CreateInput{
				Id:         ulid.GenerateString(),
				ScopeId:    sID,
				PropertyId: name,
			})
		}
		for _, name := range sc.GetApplicableUnits() {
			g.units = append(g.units, scopeapplicableunitsql.CreateInput{
				Id:       ulid.GenerateString(),
				ScopeId:  sID,
				UnitId:   repox.LastSegment(name),
				UnitName: name,
			})
		}
	}

	return g
}

// codeGenerationName is the stored form of a CodeGeneration, empty (omitted,
// so NULL) when unspecified.
func codeGenerationName(v promocodepbv1.CodeGeneration) string {
	if v == promocodepbv1.CodeGeneration_CODE_GENERATION_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(v.String(), "CODE_GENERATION_")
}
