// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// The availability read model (allocation.v1.ResourceDay) on each backend:
// rebuilt for seeded courts, kept current by a converted hold's own write,
// and read by search, check and hold, with filters and facets.
//
// On Hasura the read model is written through GraphQL: a Batch's projection
// rides in the Batch's own request, and a single repository write projects
// in a request of its own right after it. Searches there run over GraphQL
// here (ranges are checked on the rows read); a deployment reads them over
// Postgres (Connection.SearchConn).

import (
	"context"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/test/live"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readModelConns yields each configured backend's connection.
func readModelConns(t *testing.T) map[string]func() *database.Connection {
	db := liveDB(t)
	out := map[string]func() *database.Connection{
		"gorm": func() *database.Connection {
			return &database.Connection{PgSQLConn: db, Provider: database.ProviderGorm}
		},
	}
	if raw := live.Optional(t, live.GraphQLURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := freebusyql.Connect(u)
		if err != nil {
			t.Fatal(err)
		}
		out["hasura"] = func() *database.Connection {
			return &database.Connection{Hasura: svc, Provider: database.ProviderHasura}
		}
	}
	return out
}

func TestLiveReadModel(t *testing.T) {
	db := liveDB(t)
	for backend, mk := range readModelConns(t) {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			s := seed(t, db)
			conn := mk()
			dp := allocation.NewDayProjection(func(c repox.Conn) allocation.Store { return On(c) }, 60)
			conn.Projections = repox.NewProjections(dp.Projector())
			store := New(conn)
			srv := allocation.NewServer(store, allocation.WithReadModel(dp, conn.SearchConn()))
			search := conn.SearchConn()

			// The courts were seeded around the repositories: backfill them.
			res, err := dp.RebuildResources(ctx, conn.Repox(), s.courts)
			if err != nil {
				t.Fatal(err)
			}
			if want := 3 * 61; res.Upserted < want-3 || res.Upserted > want+3 {
				t.Fatalf("rebuild stored %d rows, want about %d", res.Upserted, want)
			}

			// court-1's event 18:00-19:00 IST is not free time.
			rows := dayRows(t, search, s.courts[0], at(18))
			if len(rows) != 1 {
				t.Fatalf("court-1 has %d rows on the booked day, want 1", len(rows))
			}
			free := spans(rows[0])
			if free.Covers(engine.Span{Start: at(18), End: at(19)}) || !free.Covers(engine.Span{Start: at(9), End: at(18)}) {
				t.Fatalf("court-1 free %v: want 08:00-18:00 and 19:00-22:00 IST", free)
			}

			t.Run("search", func(t *testing.T) {
				out, err := srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{
					Filter: s.only(), Window: win(18, 19), Facets: []string{"resource_types"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := names(out.GetMatches()); !slices.Equal(got, s.courts[1:]) {
					t.Fatalf("search 18-19 found %v, want %v", got, s.courts[1:])
				}
				if f := out.GetFacets(); len(f) != 1 || len(f[0].GetBuckets()) != 1 || f[0].GetBuckets()[0].GetCount() != 2 {
					t.Fatalf("facets %v, want padel-court: 2", f)
				}
				// Paged one at a time, the pages join up.
				var paged []string
				tok := ""
				for {
					out, err := srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(9, 10), PageSize: 1, PageToken: tok})
					if err != nil {
						t.Fatal(err)
					}
					paged = append(paged, names(out.GetMatches())...)
					if tok = out.GetNextPageToken(); tok == "" {
						break
					}
				}
				if !slices.Equal(paged, s.courts) {
					t.Fatalf("paged search found %v, want %v", paged, s.courts)
				}
				// A filter over STRUCTURED-DATA.
				out, err = srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{
					Window: win(9, 10), Filter: s.only() + ` AND attributes.min_duration = "PT1H"`,
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := names(out.GetMatches()); !slices.Equal(got, s.courts[2:]) {
					t.Fatalf("filtered search found %v, want %v", got, s.courts[2:])
				}
			})

			t.Run("check", func(t *testing.T) {
				out, err := srv.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{
					Resources: s.courts, Window: win(9, 10), Filter: `attributes.min_duration = "PT1H"`, Quantity: 2,
				})
				if err != nil {
					t.Fatal(err)
				}
				if out.GetBookable() || out.GetFreeCount() != 1 {
					t.Fatalf("filtered check of 2: bookable %v free %d, want false 1", out.GetBookable(), out.GetFreeCount())
				}
			})

			t.Run("hold converts into the read model", func(t *testing.T) {
				h, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
					Hold: &allocationpbv1.Hold{Window: win(10, 11), CandidateResources: s.courts, CandidateFilter: `attributes.min_duration = "PT1H"`},
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := h.GetResources(); len(got) != 1 || got[0] != s.courts[2] || h.GetCandidateFilter() == "" {
					t.Fatalf("hold took %v (filter %q), want %s", got, h.GetCandidateFilter(), s.courts[2])
				}
				// A live hold leaves the read model alone.
				if spans(dayRows(t, search, s.courts[2], at(10))[0]).AnyOverlap(engine.Span{Start: at(10), End: at(11)}) == false {
					t.Fatal("a live hold was subtracted from the read model")
				}
				if _, err := srv.ConvertHold(ctx, h.GetName()); err != nil {
					t.Fatal(err)
				}
				if spans(dayRows(t, search, s.courts[2], at(10))[0]).AnyOverlap(engine.Span{Start: at(10), End: at(11)}) {
					t.Fatal("the converted hold's window is still free in the read model")
				}
				out, err := srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(10, 11)})
				if err != nil {
					t.Fatal(err)
				}
				if got := names(out.GetMatches()); !slices.Equal(got, s.courts[:2]) {
					t.Fatalf("search 10-11 after the booking found %v, want %v", got, s.courts[:2])
				}
			})

			// Over Hasura (no Postgres handle) DDN has no array comparison and
			// no order by a JSON path: the request's resource_types field is
			// matched on the rows read, while the same test in a filter, or
			// an order by attributes.max_occupancy, is refused with a reason
			// of its own. Over Postgres all three work.
			t.Run("backend limits", func(t *testing.T) {
				out, err := srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{
					Filter: s.only(), Window: win(18, 19), ResourceTypes: []string{"padel-court"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := names(out.GetMatches()); !slices.Equal(got, s.courts[1:]) {
					t.Fatalf("search 18-19 by resource_types found %v, want %v", got, s.courts[1:])
				}
				for _, tc := range []struct {
					req    *allocationpbv1.SearchAvailabilityRequest
					reason string
				}{
					{&allocationpbv1.SearchAvailabilityRequest{Window: win(18, 19), Filter: s.only() + ` AND resource_types:"padel-court"`}, "FILTER_UNSUPPORTED"},
					{&allocationpbv1.SearchAvailabilityRequest{Filter: s.only(), Window: win(18, 19), OrderBy: "attributes.max_occupancy desc"}, "ORDER_BY_UNSUPPORTED"},
				} {
					out, err := srv.SearchAvailability(ctx, tc.req)
					if backend != "hasura" {
						if err != nil || len(out.GetMatches()) != 2 {
							t.Fatalf("%s/%s: %v, %d matches, want 2", tc.req.GetFilter(), tc.req.GetOrderBy(), err, len(out.GetMatches()))
						}
						continue
					}
					if got := reason(err); status.Code(err) != codes.InvalidArgument || got != tc.reason {
						t.Fatalf("%s/%s over Hasura: %v (reason %q), want %s", tc.req.GetFilter(), tc.req.GetOrderBy(), err, got, tc.reason)
					}
				}
			})

			t.Run("bad filter", func(t *testing.T) {
				_, err := srv.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Window: win(9, 10), Filter: "nosuchfield = 1"})
				if err == nil {
					t.Fatal("a filter on an unknown field was accepted")
				}
			})
		})
	}
}

// dayRows reads resource's read-model rows on the UTC day of t.
func dayRows(t *testing.T, conn repox.Conn, resource string, at time.Time) []*allocationpbv1.ResourceDay {
	t.Helper()
	rows, _, err := allocation.PostgresDays(conn).SearchDays(context.Background(), repox.SearchInput{
		Where: []filterx.Condition{
			{Field: "resource", Op: filterx.OpEq, Value: resource},
			{Field: "day", Op: filterx.OpEq, Value: at.UTC().Format(time.DateOnly)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func spans(r *allocationpbv1.ResourceDay) engine.Spans {
	var out engine.Spans
	for _, f := range r.GetFree() {
		out = append(out, allocation.SpanOf(f))
	}
	return out
}

func names(ms []*allocationpbv1.AvailabilityMatch) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.GetResource())
	}
	return out
}

// reason is the ErrorInfo reason err carries, "" for none.
func reason(err error) string {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}
