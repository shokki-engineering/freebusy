// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// The read model's OpenSearch copy: fed by the projection's change feed,
// reloaded by Reindex, searched by SearchAvailability, and falling back to
// Postgres when the cluster cannot answer. OpenSearch is an optional backend,
// so this suite runs only when FREEBUSY_TEST_OPENSEARCH_URL names a cluster
// (a throwaway one: it creates the freebusy_resource_days alias), even under
// FREEBUSY_TEST_REQUIRE_LIVE.

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/search"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
)

func TestLiveOpenSearch(t *testing.T) {
	url := os.Getenv("FREEBUSY_TEST_OPENSEARCH_URL")
	if url == "" {
		t.Skip("FREEBUSY_TEST_OPENSEARCH_URL not set — OpenSearch suite skipped")
	}
	ctx := context.Background()
	db := liveDB(t)
	s := seed(t, db)
	conn := &database.Connection{PgSQLConn: db, Provider: database.ProviderGorm}
	dp := allocation.NewDayProjection(func(c repox.Conn) allocation.Store { return On(c) }, 60)
	conn.Projections = repox.NewProjections(dp.Projector())
	o, err := search.Open(ctx, config.OpenSearchConfig{URL: url, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	var feedErrs []error
	conn.Projections.Feed = o.Feed(func(_ context.Context, err error) { feedErrs = append(feedErrs, err) })
	store := New(conn)

	// The backfill reaches OpenSearch through the feed.
	if _, err := dp.RebuildResources(ctx, conn.Repox(), s.courts); err != nil {
		t.Fatal(err)
	}
	srv := allocation.NewServer(store, allocation.WithReadModel(dp, conn.SearchConn()),
		allocation.WithSearcher(o.Searcher(allocation.PostgresDays(conn.SearchConn()), nil)))
	searchUntil := func(want []string, req *allocationpbv1.SearchAvailabilityRequest) *allocationpbv1.SearchAvailabilityResponse {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			out, err := srv.SearchAvailability(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			// Facets are read just before the matches: the index may refresh
			// in between, so wait for both.
			counted := len(req.GetFacets()) == 0 || len(out.GetFacets()[0].GetBuckets()) > 0
			if (slices.Equal(names(out.GetMatches()), want) && counted) || time.Now().After(deadline) {
				if got := names(out.GetMatches()); !slices.Equal(got, want) {
					t.Fatalf("search found %v, want %v (feed errors %v)", got, want, feedErrs)
				}
				return out
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	out := searchUntil(s.courts[1:], &allocationpbv1.SearchAvailabilityRequest{
		Filter: s.only(), Window: win(18, 19), Facets: []string{"resource_types", "attributes.min_duration"},
	})
	if f := out.GetFacets(); len(f) != 2 || len(f[0].GetBuckets()) != 1 || f[0].GetBuckets()[0].GetCount() != 2 {
		t.Fatalf("facets %v", f)
	}
	searchUntil(s.courts[2:], &allocationpbv1.SearchAvailabilityRequest{Window: win(9, 10), Filter: s.only() + ` AND attributes.min_duration = "PT1H"`})

	// A converted hold reaches the copy through the same feed.
	h, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Hold: &allocationpbv1.Hold{Window: win(10, 11), CandidateResources: s.courts[1:2]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ConvertHold(ctx, h.GetName()); err != nil {
		t.Fatal(err)
	}
	searchUntil([]string{s.courts[0], s.courts[2]}, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(10, 11)})

	// Reindex reloads the copy behind the alias.
	res, err := o.Reindex(ctx, conn.SearchConn())
	if err != nil {
		t.Fatal(err)
	}
	if res.Loaded == 0 || res.Index == "" {
		t.Fatalf("reindex %+v", res)
	}
	// What a starting server checks before reindexing ([search.opensearch]
	// reindex_on_start = "empty"): both hold rows now.
	if empty, err := o.Empty(ctx); err != nil || empty {
		t.Fatalf("index Empty() = %v, %v after a reindex", empty, err)
	}
	if empty, err := dp.Empty(ctx, conn.SearchConn()); err != nil || empty {
		t.Fatalf("read model Empty() = %v, %v after a rebuild", empty, err)
	}
	searchUntil([]string{s.courts[0], s.courts[2]}, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(10, 11)})
	// Served by OpenSearch itself: with a Postgres that always fails behind
	// it, the read still answers.
	strict := allocation.NewServer(store, allocation.WithReadModel(dp, conn.SearchConn()), allocation.WithSearcher(o.Searcher(noPostgres{}, nil)))
	if out, err := strict.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(10, 11), Facets: []string{"resource_types"}}); err != nil || len(out.GetMatches()) != 2 {
		t.Fatalf("OpenSearch alone: %v, %v", names(out.GetMatches()), err)
	}
	if err := o.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// A cluster that cannot answer: searches fall back to Postgres.
	down, err := search.Connect(config.OpenSearchConfig{URL: "http://127.0.0.1:1", TimeoutMS: 500})
	if err != nil {
		t.Fatal(err)
	}
	srv = allocation.NewServer(store, allocation.WithReadModel(dp, conn.SearchConn()),
		allocation.WithSearcher(down.Searcher(allocation.PostgresDays(conn.SearchConn()), nil)))
	searchUntil([]string{s.courts[0], s.courts[2]}, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(10, 11), Facets: []string{"resource_types"}})
}

// noPostgres is a fallback that always fails.
type noPostgres struct{}

func (noPostgres) SearchDays(context.Context, repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error) {
	return nil, "", errors.New("no postgres")
}

func (noPostgres) FacetDays(context.Context, repox.SearchInput, string, int) ([]repox.Bucket, error) {
	return nil, errors.New("no postgres")
}
