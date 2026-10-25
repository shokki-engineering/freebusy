// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

// Reading the availability read model (allocation.v1.ResourceDay, see
// resourceday.go). Every answer is the engine's: the read model only says
// which resources are worth evaluating. A row's free time is offered less
// booked, so a resource free for a window has a row covering it unless the
// read model is behind its sources, and a row covering a window is a
// candidate the engine may still refuse (a live hold, a booking policy).
// Holds and checks fall back to evaluating every resource in scope whenever
// the candidates fall short, so neither ever depends on the read model being
// current; a search can miss a resource whose row is stale until the next
// write to it or a rebuild.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
)

// DaySearcher searches the read model: its own Postgres table
// (PostgresDays), or a copy elsewhere (OpenSearch).
type DaySearcher interface {
	// SearchDays returns one page of rows matching in, and the next page's
	// token.
	SearchDays(ctx context.Context, in repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error)
	// FacetDays counts the rows matching in by the values of field.
	FacetDays(ctx context.Context, in repox.SearchInput, field string, limit int) ([]repox.Bucket, error)
}

// WithReadModel has the server read candidates from the availability read
// model dp maintains, searched over conn (Connection.SearchConn).
func WithReadModel(dp *DayProjection, conn repox.Conn) Option {
	return func(s *Server) { s.days, s.pg = dp, PostgresDays(conn) }
}

// WithSearcher serves SearchAvailability's candidates from d instead of the
// read model's Postgres table: an OpenSearch copy, say. Holds, checks and
// computations keep reading Postgres.
func WithSearcher(d DaySearcher) Option { return func(s *Server) { s.searcher = d } }

// ReadModel is the read model's maintainer, nil without one.
func (s *Server) ReadModel() *DayProjection { return s.days }

// candidatePage is how many read-model rows one candidate read asks for.
const candidatePage = 1000

// facetSize is how many values a facet returns.
const facetSize = 20

// dayStart is the UTC day t falls on.
func dayStart(t time.Time) time.Time { return t.UTC().Truncate(oneDay) }

// windowInput selects the rows whose free time covers window on its first
// day: the row of that day, holding a free interval that contains the part of
// window inside it.
func windowInput(window engine.Span) repox.SearchInput {
	d := dayStart(window.Start)
	part := window.Intersect(engine.Span{Start: d, End: d.Add(oneDay)})
	return repox.SearchInput{
		Range: &repox.Span{Start: part.Start, End: part.End},
		Where: []filterx.Condition{{Field: "day", Op: filterx.OpEq, Value: d.Format(time.DateOnly)}},
	}
}

// lastDayCovers keeps the names whose row on window's last day also covers
// it there, when window spans more than one day. Days in between are left to
// the engine.
func (s *Server) lastDayCovers(ctx context.Context, d DaySearcher, names []string, window engine.Span) ([]string, error) {
	last := dayStart(window.End.Add(-time.Nanosecond))
	if !last.After(dayStart(window.Start)) || len(names) == 0 {
		return names, nil
	}
	part := window.Intersect(engine.Span{Start: last, End: last.Add(oneDay)})
	keep := map[string]bool{}
	for chunk := range slices.Chunk(names, candidatePage) {
		rows, _, err := d.SearchDays(ctx, repox.SearchInput{
			Range: &repox.Span{Start: part.Start, End: part.End},
			Where: []filterx.Condition{
				{Field: "day", Op: filterx.OpEq, Value: last.Format(time.DateOnly)},
				filterx.In("resource", chunk...),
			},
			PageSize: int32(len(chunk)),
			ReadMask: []string{"resource"},
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			keep[r.GetResource()] = true
		}
	}
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return !keep[n] }), nil
}

// readAll drains a read-model search, returning the resources named.
func readAll(ctx context.Context, d DaySearcher, in repox.SearchInput) ([]string, error) {
	in.PageSize, in.ReadMask = candidatePage, []string{"resource"}
	var out []string
	for {
		rows, next, err := d.SearchDays(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, r.GetResource())
		}
		if next == "" {
			return out, nil
		}
		in.PageToken = next
	}
}

// modelCandidates are the resources at site, among names (all when empty)
// and matching filter, whose rows say they are free for window. ok is false
// when the read model cannot answer -- none configured, or window outside
// its horizon -- and the caller evaluates every resource in scope.
func (s *Server) modelCandidates(ctx context.Context, site string, names []string, filter string, window engine.Span, now time.Time) (cands []string, ok bool, err error) {
	if s.days == nil || !s.days.Covers(window, now) {
		return nil, false, nil
	}
	in := windowInput(window)
	in.Where = append(in.Where, filterx.Condition{Field: "organizational_unit", Op: filterx.OpEq, Value: site})
	if len(names) > 0 {
		in.Where = append(in.Where, filterx.In("resource", names...))
	}
	in.Filter = filter
	if cands, err = readAll(ctx, s.pg, in); err != nil {
		return nil, false, filterProblem(err, "filter")
	}
	cands, err = s.lastDayCovers(ctx, s.pg, cands, window)
	return cands, err == nil, err
}

// filterNames are the resources matching filter among those where selects,
// free or not: their rows on one day of the horizon, today's.
func (s *Server) filterNames(ctx context.Context, where []filterx.Condition, filter, field string) ([]string, error) {
	if s.days == nil {
		return nil, servicex.BadField("INVALID_FILTER", field,
			"this server keeps no search records, so it cannot filter resources",
			"Leave "+field+" empty")
	}
	in := repox.SearchInput{
		Where:  append([]filterx.Condition{{Field: "day", Op: filterx.OpEq, Value: dayStart(s.now()).Format(time.DateOnly)}}, where...),
		Filter: filter,
	}
	names, err := readAll(ctx, s.pg, in)
	if err != nil {
		return nil, filterProblem(err, field)
	}
	return names, nil
}

// scopeOf is the resources a site call may consider: names, narrowed by
// filter. restricted reports that the scope is the list returned even when
// it is empty (a filter nothing matches), rather than every resource.
func (s *Server) scopeOf(ctx context.Context, site string, names []string, filter, field string) (scope []string, restricted bool, err error) {
	if filter == "" {
		return names, len(names) > 0, nil
	}
	if err := validFilter(filter, field); err != nil {
		return nil, false, err
	}
	where := []filterx.Condition{{Field: "organizational_unit", Op: filterx.OpEq, Value: site}}
	if len(names) > 0 {
		where = append(where, filterx.In("resource", names...))
	}
	scope, err = s.filterNames(ctx, where, filter, field)
	return scope, true, err
}

// validFilter is checkFilter as the problem a caller is shown.
func validFilter(filter, field string) error {
	if err := checkFilter(filter); err != nil {
		return servicex.BadField("INVALID_FILTER", field,
			fmt.Sprintf("%s %q is not a filter over resources: %v", field, filter, err),
			`Use AIP-160 over resource_types, display_name, organizational_unit and attributes.<path>, e.g. resource_types:"room" AND attributes.view = "river"`)
	}
	return nil
}

// filterProblem turns a read model's refusal of a filter into INVALID_FILTER,
// or FILTER_UNSUPPORTED when the filter is valid but the backend the read
// model is searched on cannot evaluate it: a `:` on resource_types over
// Hasura DDN, which has no comparison on array columns.
func filterProblem(err error, field string) error {
	if errors.Is(err, filterx.ErrUnsupported) {
		return servicex.BadField("FILTER_UNSUPPORTED", field, fmt.Sprintf("%s cannot be evaluated where this server searches resources: %v", field, err),
			`Match resource types with the request's resource_types field instead of resource_types: in a filter, or configure Postgres ([database.postgres]) for the search records`)
	}
	if errors.Is(err, repox.ErrInvalidArgument) {
		return servicex.BadField("INVALID_FILTER", field, fmt.Sprintf("%s is not a filter over resources: %v", field, err),
			`Use AIP-160 over resource_types, display_name, organizational_unit and attributes.<path>`)
	}
	return err
}

// searchProblem is filterProblem for SearchAvailability's candidate read,
// which also orders: a JSON attribute path in orderBy that the backend cannot
// sort by (Hasura DDN's order_by has no path expression) is
// ORDER_BY_UNSUPPORTED on order_by, anything else is the filter's.
func searchProblem(err error, orderBy string) error {
	if errors.Is(err, filterx.ErrUnsupported) && orderByPath(orderBy) {
		return servicex.BadField("ORDER_BY_UNSUPPORTED", "order_by", fmt.Sprintf("order_by %q cannot be applied where this server searches resources: %v", orderBy, err),
			"Order by resource, organizational_unit, display_name or free_fraction, or configure Postgres ([database.postgres]) for the search records")
	}
	return filterProblem(err, "filter")
}

// searchToken is SearchAvailability's page token: where in the read model's
// order the next page starts.
type searchToken struct {
	// Page is the read-model token of the candidate page to resume in.
	Page string `json:"p,omitempty"`
	// Skip is how many of that page's matches earlier pages returned.
	Skip int `json:"s,omitempty"`
	// Site, when quantity is above 1, is a site already known to have
	// quantity matches, so its rest are returned without counting again.
	Site string `json:"o,omitempty"`
}

func (t searchToken) encode() string {
	raw, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSearchToken(s string) (searchToken, error) {
	var t searchToken
	if s == "" {
		return t, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(raw, &t)
	}
	if err != nil || t.Skip < 0 {
		return t, servicex.BadField("INVALID_PAGE_TOKEN", "page_token", "page_token is not one this search returned",
			"Send next_page_token from the previous response unchanged, with the same other fields, or leave it empty for the first page")
	}
	return t, nil
}

// facetable are the fields SearchAvailability counts by, besides any
// attributes.<path>.
var facetable = []string{"resource_types", "organizational_unit", "organization", "display_name"}

// checkSearch validates SearchAvailability's filter, order_by and facets.
func checkSearch(req *allocationpbv1.SearchAvailabilityRequest) error {
	if err := validFilter(req.GetFilter(), "filter"); err != nil {
		return err
	}
	if o := req.GetOrderBy(); o != "" {
		if err := checkOrderBy(o); err != nil {
			return servicex.BadField("INVALID_ORDER_BY", "order_by", fmt.Sprintf("order_by %q: %v", o, err),
				`Order by resource, organizational_unit, display_name, free_fraction or attributes.max_occupancy, each optionally followed by " desc"`)
		}
	}
	for _, f := range req.GetFacets() {
		if !slices.Contains(facetable, f) && !strings.HasPrefix(f, "attributes.") {
			return servicex.BadField("INVALID_FACET", "facets", fmt.Sprintf("cannot count resources by %q", f),
				"Count by resource_types, organizational_unit, organization, display_name or attributes.<path>")
		}
	}
	return nil
}

// searchModel answers SearchAvailability from the read model: candidate rows
// in the requested order, a page at a time, each evaluated by the engine, and
// with quantity above 1 a site's matches returned only when it has quantity
// of them.
func (s *Server) searchModel(ctx context.Context, req *allocationpbv1.SearchAvailabilityRequest, window engine.Span) (*allocationpbv1.SearchAvailabilityResponse, error) {
	now := s.now()
	quantity := max(int(req.GetQuantity()), 1)
	limit, _ := rpc.PageBounds(req.GetPageSize(), "")
	tok, err := decodeSearchToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	searcher := cmp.Or[DaySearcher](s.searcher, s.pg)
	in := windowInput(window)
	in.Where = append(in.Where, filterx.Condition{Field: "organization", Op: filterx.OpEq, Value: req.GetParent()})
	if ous := req.GetOrganizationalUnits(); len(ous) > 0 {
		in.Where = append(in.Where, filterx.In("organizational_unit", ous...))
	}
	// resource_types is a text[] column. Over Hasura (a deployment without
	// Postgres configured) the DDN metadata has no array comparison, so the
	// types are matched on the rows read; pages may then come back short.
	types := req.GetResourceTypes()
	if g, ok := searcher.(interface{ overGraphQL() bool }); ok && g.overGraphQL() {
		searcher = typedRows{searcher, types}
	} else if len(types) > 0 {
		alts := make([]filterx.Condition, len(types))
		for i, t := range types {
			alts[i] = filterx.Condition{Field: "resource_types", Op: filterx.OpHas, Value: t}
		}
		in.Where = append(in.Where, filterx.Or(alts...))
	}
	in.Filter = req.GetFilter()

	out := &allocationpbv1.SearchAvailabilityResponse{}
	for _, f := range req.GetFacets() {
		buckets, err := searcher.FacetDays(ctx, in, f, facetSize)
		if err != nil {
			return nil, filterProblem(err, "facets")
		}
		sf := &allocationpbv1.SearchFacet{Field: f}
		for _, b := range buckets {
			sf.Buckets = append(sf.Buckets, &allocationpbv1.FacetBucket{Value: b.Value, Count: b.Count})
		}
		out.Facets = append(out.Facets, sf)
	}

	in.OrderBy = cmp.Or(req.GetOrderBy(), "organizational_unit, resource")
	if quantity > 1 && req.GetOrderBy() != "" {
		in.OrderBy = "organizational_unit, " + req.GetOrderBy()
	}
	in.PageSize = int32(min(max(2*limit, 100), candidatePage))
	in.ReadMask = []string{"resource", "organizational_unit", "resource_types", "display_name"}

	type entry struct {
		m    *allocationpbv1.AvailabilityMatch
		page string
		idx  int
	}
	done := false
	// accept returns e, or, when the page is full, makes e where the next
	// page starts.
	accept := func(e entry, site string) {
		if len(out.Matches) == limit {
			out.NextPageToken = searchToken{Page: e.page, Skip: e.idx, Site: site}.encode()
			done = true
			return
		}
		out.Matches = append(out.Matches, e.m)
	}
	var group []entry
	groupSite := ""
	flush := func() {
		if len(group) >= quantity || (groupSite != "" && groupSite == tok.Site) {
			for _, e := range group {
				if accept(e, groupSite); done {
					break
				}
			}
		}
		group = nil
	}
	page, skip := tok.Page, tok.Skip
	for !done {
		in.PageToken = page
		rows, next, err := searcher.SearchDays(ctx, in)
		if err != nil {
			return nil, searchProblem(err, req.GetOrderBy())
		}
		matches, err := s.verifyRows(ctx, searcher, rows, window, now)
		if err != nil {
			return nil, err
		}
		for i, m := range matches {
			if i < skip {
				continue
			}
			e := entry{m: m, page: page, idx: i}
			if quantity == 1 {
				if accept(e, ""); done {
					break
				}
				continue
			}
			if m.GetOrganizationalUnit() != groupSite {
				if flush(); done {
					break
				}
				groupSite = m.GetOrganizationalUnit()
			}
			group = append(group, e)
		}
		skip = 0
		if done {
			break
		}
		if next == "" {
			flush()
			break
		}
		page = next
	}
	return out, nil
}

// verifyRows evaluates candidate rows for window and returns, in the rows'
// order, the matches the engine confirms.
func (s *Server) verifyRows(ctx context.Context, d DaySearcher, rows []*allocationpbv1.ResourceDay, window engine.Span, now time.Time) ([]*allocationpbv1.AvailabilityMatch, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(rows))
	var sites []string
	for _, r := range rows {
		names = append(names, r.GetResource())
		if !slices.Contains(sites, r.GetOrganizationalUnit()) {
			sites = append(sites, r.GetOrganizationalUnit())
		}
	}
	names, err := s.lastDayCovers(ctx, d, names, window)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	st, err := s.gather(ctx, ResourceQuery{OrganizationalUnits: sites, Names: names}, horizon(window), now)
	if err != nil {
		return nil, err
	}
	free := map[string]bool{}
	for _, r := range st.resources {
		if r.Why(window, now) == engine.ReasonNone {
			free[r.Name] = true
		}
	}
	var out []*allocationpbv1.AvailabilityMatch
	for _, r := range rows {
		if !free[r.GetResource()] {
			continue
		}
		out = append(out, &allocationpbv1.AvailabilityMatch{
			Resource:           r.GetResource(),
			OrganizationalUnit: r.GetOrganizationalUnit(),
			Window:             intervalOf(window),
			ResourceTypes:      r.GetResourceTypes(),
			DisplayName:        r.GetDisplayName(),
		})
	}
	return out, nil
}

// typedRows keeps the rows carrying any of types, for a searcher that cannot
// match them in its query. Facets count every row the query matches.
type typedRows struct {
	DaySearcher
	types []string
}

func (t typedRows) SearchDays(ctx context.Context, in repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error) {
	rows, next, err := t.DaySearcher.SearchDays(ctx, in)
	if err != nil || len(t.types) == 0 {
		return rows, next, err
	}
	rows = slices.DeleteFunc(rows, func(r *allocationpbv1.ResourceDay) bool {
		return !slices.ContainsFunc(r.GetResourceTypes(), func(x string) bool { return slices.Contains(t.types, x) })
	})
	return rows, next, nil
}
