// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// Live integration tests: the availability engine end to end on a real
// database, once per backend, with the same scenarios.
//
// Skipped unless the backends are named:
//
//	FREEBUSY_TEST_DATABASE_URL="host=127.0.0.1 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" \
//	FREEBUSY_TEST_GRAPHQL_URL=http://localhost:3280/graphql \
//	  go test ./internal/service/allocation/db/ -run Live -v
//
// The database URL is needed for both runs: fixtures are seeded through GORM,
// and the Hasura engine serves the same Postgres. Each run seeds its own site
// under fresh ULIDs, so runs never collide; rows are left behind.

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/common"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/rfc"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/test/live"
	"github.com/the-protobuf-project/runtime-go/ulid"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ist = time.FixedZone("IST", 5*3600+1800)

// day is the date the fixtures book on: far enough ahead that nothing is past.
func day() time.Time {
	d := time.Now().In(ist).AddDate(0, 0, 21)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, ist)
}

func at(h float64) time.Time { return day().Add(time.Duration(h * float64(time.Hour))) }

func win(a, b float64) *interval.Interval {
	return &interval.Interval{StartTime: timestamppb.New(at(a)), EndTime: timestamppb.New(at(b))}
}

func liveDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := live.Env(t, live.DatabaseURL)
	db, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// site is one seeded padel club: three courts, each on its own calendar, open
// 08:00-22:00 daily in IST; court-1 has an opaque booking 18:00-19:00; court-3
// declares a one-hour minimum.
type site struct {
	ou     string
	courts []string
}

func seed(t *testing.T, db *gorm.DB) site {
	t.Helper()
	run := ulid.Generate().String()
	s := site{ou: live.Site(t, db, "it-"+run)}
	create := func(v any) {
		t.Helper()
		if err := db.Omit(clause.Associations).Create(v).Error; err != nil {
			t.Fatalf("seed %T: %v", v, err)
		}
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&common.TimeZone{ID: "Asia/Kolkata"}).Error; err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	dt := availability.CalendarTimeValueCaseDateTime
	endForm := availability.AvailablePeriodEndFormCaseEnd
	for i, n := range []string{"court-1", "court-2", "court-3"} {
		id := n + "-" + run
		calID := "cal-" + id
		create(&calendar.Calendar{ID: calID, Name: "calendars/" + calID, DisplayName: id, TimeZoneID: ptr("Asia/Kolkata")})
		res := &resource.Resource{
			ID: id, Name: "resources/" + id, CalendarID: &calID,
			OrganizationalUnitID: ptr("it-" + run), ResourceTypes: []string{"padel-court"},
		}
		create(res)
		s.courts = append(s.courts, res.Name)
		if i == 2 {
			create(&resource.StructuredData{
				ID: ulid.Generate().String(), ResourceID: id, Schema: ptr(rfc.ProfileSchemaURI),
				Text: ptr(`{"min_duration":"PT1H"}`), ValueCase: ptr(resource.StructuredDataValueCaseText),
			})
		}

		avID, startID, endID := ulid.Generate().String(), ulid.Generate().String(), ulid.Generate().String()
		opens, closes := at(8).AddDate(0, 0, -7), at(22).AddDate(0, 0, -7)
		create(&availability.CalendarTime{ID: startID, DateTime: &opens, ValueCase: &dt})
		create(&availability.CalendarTime{ID: endID, DateTime: &closes, ValueCase: &dt})
		create(&availability.Availability{ID: avID, Name: "calendars/" + calID + "/availabilities/" + avID, CalendarID: calID})
		create(&availability.AvailablePeriod{
			ID: ulid.Generate().String(), AvailabilityID: avID, StartID: startID, EndID: &endID,
			RecurrenceRule: ptr("FREQ=DAILY"), EndFormCase: &endForm,
		})

		if i == 0 {
			evID, es, ee := ulid.Generate().String(), ulid.Generate().String(), ulid.Generate().String()
			from, to := at(18), at(19)
			edt := event.CalendarTimeValueCaseDateTime
			create(&event.CalendarTime{ID: es, DateTime: &from, ValueCase: &edt, EventID: evID})
			create(&event.CalendarTime{ID: ee, DateTime: &to, ValueCase: &edt, EventID: evID})
			opaque, ends := event.TransparencyOpaque, event.EventEndsCaseEnd
			create(&event.Event{
				ID: evID, Name: "calendars/" + calID + "/events/" + evID, CalendarID: calID,
				StartID: es, EndID: &ee, Transparency: &opaque, EndsCase: &ends,
			})
		}
	}
	return s
}

func ptr[T any](v T) *T { return &v }

// stores yields each configured backend.
func stores(t *testing.T) map[string]func(*gorm.DB) allocation.Store {
	out := map[string]func(*gorm.DB) allocation.Store{
		"gorm": func(g *gorm.DB) allocation.Store {
			return New(&database.Connection{PgSQLConn: g, Provider: database.ProviderGorm})
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
		out["hasura"] = func(*gorm.DB) allocation.Store {
			return New(&database.Connection{Hasura: svc, Provider: database.ProviderHasura})
		}
	}
	return out
}

func TestLiveEngine(t *testing.T) {
	db := liveDB(t)
	for backend, mk := range stores(t) {
		t.Run(backend, func(t *testing.T) {
			s := seed(t, db)
			store := mk(db)
			srv := allocation.NewServer(store)
			ctx := context.Background()

			t.Run("compute", func(t *testing.T) {
				res, err := srv.ComputeAvailability(ctx, &allocationpbv1.ComputeAvailabilityRequest{
					Parent: s.ou, Window: win(7, 10), SlotDuration: durationpb.New(time.Hour),
					Granularity: allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_SLOT,
				})
				if err != nil {
					t.Fatal(err)
				}
				got := counts(res)
				if want := []int32{0, 3, 3}; !equal(got, want) {
					t.Fatalf("07-10 free %v, want %v", got, want)
				}
				res, err = srv.ComputeAvailability(ctx, &allocationpbv1.ComputeAvailabilityRequest{
					Parent: s.ou, Window: win(17, 20), SlotDuration: durationpb.New(time.Hour),
					Granularity: allocationpbv1.AvailabilityGranularity_AVAILABILITY_GRANULARITY_SLOT,
				})
				if err != nil {
					t.Fatal(err)
				}
				if want := []int32{3, 2, 3}; !equal(counts(res), want) {
					t.Fatalf("17-20 free %v, want %v (court-1 booked 18-19)", counts(res), want)
				}
			})

			t.Run("check explains", func(t *testing.T) {
				res, err := srv.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Parent: s.ou, Window: win(18, 19), Quantity: 3})
				if err != nil {
					t.Fatal(err)
				}
				reasons := map[string]allocationpbv1.UnbookableReasonCode{}
				for _, r := range res.GetReasons() {
					reasons[r.GetResource()] = r.GetCode()
				}
				if res.GetBookable() || reasons[s.courts[0]] != allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_BOOKED {
					t.Fatalf("bookable=%v reasons=%v", res.GetBookable(), reasons)
				}
				res, err = srv.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{
					Parent: s.ou, Window: win(12, 12.5), Resources: []string{s.courts[2]},
				})
				if err != nil {
					t.Fatal(err)
				}
				if res.GetBookable() || res.GetReasons()[1].GetCode() != allocationpbv1.UnbookableReasonCode_UNBOOKABLE_REASON_CODE_WINDOW_TOO_SHORT {
					t.Fatalf("court-3 half hour: %v", res.GetReasons())
				}
			})

			t.Run("race for one court", func(t *testing.T) {
				const callers = 10
				var wg sync.WaitGroup
				var mu sync.Mutex
				won, lost := 0, 0
				for range callers {
					wg.Go(func() {
						_, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
							Parent: s.ou,
							Hold:   &allocationpbv1.Hold{Window: win(20, 21), CandidateResources: []string{s.courts[1]}},
						})
						mu.Lock()
						defer mu.Unlock()
						switch status.Code(err) {
						case codes.OK:
							won++
						case codes.FailedPrecondition, codes.Aborted:
							lost++
						default:
							t.Errorf("unexpected: %v", err)
						}
					})
				}
				wg.Wait()
				if won != 1 || lost != callers-1 {
					t.Fatalf("won=%d lost=%d: exactly one caller may get the court", won, lost)
				}
			})

			t.Run("lifecycle", func(t *testing.T) {
				h, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
					Parent: s.ou, Hold: &allocationpbv1.Hold{Window: win(10, 11), Quantity: 3, IdempotencyKey: "it-" + ulid.Generate().String()},
				})
				if err != nil {
					t.Fatal(err)
				}
				if !h.GetWindow().GetStartTime().AsTime().Equal(at(10)) || len(h.GetResources()) != 3 {
					t.Fatalf("hold %v", h)
				}
				again, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
					Parent: s.ou, Hold: &allocationpbv1.Hold{Window: win(10, 11), Quantity: 3, IdempotencyKey: h.GetIdempotencyKey()},
				})
				if err != nil || again.GetName() != h.GetName() {
					t.Fatalf("idempotent retry: %v %v", again.GetName(), err)
				}
				if n := free(t, srv, s, 10, 11); n != 0 {
					t.Fatalf("held: %d free", n)
				}
				list, err := srv.ListHolds(ctx, &allocationpbv1.ListHoldsRequest{Parent: s.ou, Filter: `state = HOLD_STATE_ACTIVE`})
				if err != nil {
					t.Fatal(err)
				}
				var found bool
				for _, l := range list.GetHolds() {
					if l.GetName() == h.GetName() {
						found = l.GetWindow().GetEndTime().AsTime().Equal(at(11))
					}
				}
				if !found {
					t.Fatalf("ListHolds lost the hold or its window: %v", list.GetHolds())
				}

				ext, err := srv.ExtendHold(ctx, &allocationpbv1.ExtendHoldRequest{Name: h.GetName(), Duration: durationpb.New(time.Hour)})
				if err != nil || ext.GetExpireTime().AsTime().Before(time.Now().Add(50*time.Minute)) {
					t.Fatalf("extend: %v %v", ext.GetExpireTime(), err)
				}
				if _, err := srv.ExtendHold(ctx, &allocationpbv1.ExtendHoldRequest{Name: h.GetName(), Duration: durationpb.New(time.Hour), Etag: "stale"}); status.Code(err) != codes.Aborted {
					t.Fatalf("stale etag: %v", err)
				}
				if _, err := srv.ReleaseHold(ctx, &allocationpbv1.ReleaseHoldRequest{Name: h.GetName()}); err != nil {
					t.Fatal(err)
				}
				if n := free(t, srv, s, 10, 11); n != 3 {
					t.Fatalf("after release: %d free", n)
				}
				if _, err := srv.DeleteHold(ctx, &allocationpbv1.DeleteHoldRequest{Name: h.GetName()}); err != nil {
					t.Fatal(err)
				}
				if _, err := srv.GetHold(ctx, &allocationpbv1.GetHoldRequest{Name: h.GetName()}); status.Code(err) != codes.NotFound {
					t.Fatalf("after delete: %v", err)
				}

				c, err := srv.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: s.ou, Hold: &allocationpbv1.Hold{Window: win(14, 15)}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := srv.ConvertHold(ctx, c.GetName()); err != nil {
					t.Fatal(err)
				}
				if n := free(t, srv, s, 14, 15); n != 2 {
					t.Fatalf("converted hold should keep its court: %d free", n)
				}
			})

			t.Run("expiry", func(t *testing.T) {
				quick := allocation.NewServer(store, allocation.WithHoldTTL(time.Second))
				h, err := quick.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: s.ou, Hold: &allocationpbv1.Hold{Window: win(16, 17), Quantity: 3}})
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				if n := free(t, srv, s, 16, 17); n != 3 {
					t.Fatalf("lapsed hold still reserving: %d free", n)
				}
				if _, err := store.ExpireHolds(ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				got, err := store.GetHold(ctx, h.GetName())
				if err != nil {
					t.Fatal(err)
				}
				if got.GetState() != allocationpbv1.HoldState_HOLD_STATE_EXPIRED {
					t.Fatalf("stored state after sweep: %v", got.GetState())
				}
			})
		})
	}
}

func free(t *testing.T, srv *allocation.Server, s site, a, b float64) int32 {
	t.Helper()
	res, err := srv.CheckAvailability(context.Background(), &allocationpbv1.CheckAvailabilityRequest{Parent: s.ou, Window: win(a, b)})
	if err != nil {
		t.Fatal(err)
	}
	return res.GetFreeCount()
}

func counts(res *allocationpbv1.ComputeAvailabilityResponse) []int32 {
	var out []int32
	for _, b := range res.GetBuckets() {
		out = append(out, b.GetFreeCount())
	}
	return out
}

func equal(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
