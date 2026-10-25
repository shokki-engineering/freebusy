// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
)

// A filter or order the search backend cannot evaluate (Hasura DDN: tag
// containment, ordering by a JSON path) is told apart from a malformed one,
// and blamed on the field that asked for it.
func TestSearchProblemReasons(t *testing.T) {
	unsupported := repox.MapFilterxErr(fmt.Errorf("%w: tags", filterx.ErrUnsupported))
	invalid := repox.MapFilterxErr(fmt.Errorf("%w: no such field", filterx.ErrInvalid))
	for _, tc := range []struct {
		err           error
		orderBy       string
		reason, field string
	}{
		{unsupported, "", "FILTER_UNSUPPORTED", "filter"},
		{unsupported, "free_fraction desc", "FILTER_UNSUPPORTED", "filter"},
		{unsupported, "attributes.max_occupancy desc", "ORDER_BY_UNSUPPORTED", "order_by"},
		{invalid, "attributes.max_occupancy", "INVALID_FILTER", "filter"},
	} {
		var p *problem.Problem
		if !errors.As(searchProblem(tc.err, tc.orderBy), &p) || p.Reason != tc.reason || p.Field != tc.field {
			t.Errorf("searchProblem(%v, %q) = %+v, want %s on %s", tc.err, tc.orderBy, p, tc.reason, tc.field)
		}
		if !errors.Is(p, repox.ErrInvalidArgument) {
			t.Errorf("searchProblem(%v, %q) is not INVALID_ARGUMENT", tc.err, tc.orderBy)
		}
	}
	if err := checkOrderBy("attributes.max_occupancy desc, display_name"); err != nil {
		t.Errorf("ordering by the declared json key: %v", err)
	}
	if err := checkOrderBy("attributes.view"); err == nil {
		t.Error("ordering by an undeclared attribute path was accepted")
	}
}

// AIP-160's OR, NOT, - and parentheses are part of the filter grammar: each
// filter here is accepted and renders the boolean it says, never a free-text
// "OR"/"NOT" searched in display_name (which matched nothing) or a field
// named "(attributes..." or "-attributes..." (which failed). Needs filterx
// from protoc-gen-store v1.6.3; with v1.6.2's parser every case fails.
func TestFilterBooleanGrammar(t *testing.T) {
	for _, tc := range []struct {
		filter string
		want   []string // fragments the SQL must contain
	}{
		{`attributes.surface = "clay" OR attributes.surface = "grass"`, []string{" OR "}},
		{`attributes.surface = "clay" OR display_name = "Court 1"`, []string{" OR ", "display_name"}},
		{`NOT attributes.indoor = true`, []string{"NOT"}},
		{`-attributes.indoor:true`, []string{"NOT"}},
		{`(attributes.surface = "clay" OR attributes.surface = "grass") AND attributes.indoor = false`, []string{" OR ", " AND "}},
	} {
		if err := validFilter(tc.filter, "filter"); err != nil {
			t.Errorf("validFilter(%q) = %v, want nil", tc.filter, err)
			continue
		}
		conds, err := filterx.Parse(tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		sql, _, err := filterx.Gorm[struct{}](allocation.ResourceDays.Spec.Filter).Where(conds)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range tc.want {
			if !strings.Contains(sql, w) {
				t.Errorf("filter %q renders %s, want it to contain %q", tc.filter, sql, w)
			}
		}
		if strings.Contains(sql, "ILIKE") {
			t.Errorf("filter %q renders %s: a keyword became a free-text search", tc.filter, sql)
		}
	}
	for _, bad := range []string{`(attributes.indoor = true`, `attributes.indoor = true OR`} {
		if err := validFilter(bad, "filter"); err == nil {
			t.Errorf("validFilter(%q) accepted an unbalanced or dangling expression", bad)
		}
	}
}
