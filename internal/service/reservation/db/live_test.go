package db

// Live integration tests: reservations end to end on a real database, once per
// backend, driving the real engine.
//
//	FREEBUSY_TEST_DATABASE_URL="host=127.0.0.1 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" \
//	FREEBUSY_TEST_GRAPHQL_URL=http://localhost:3280/graphql \
//	  go test ./internal/service/reservation/db/ -run Live -v
//
// Fixtures are seeded through GORM for both runs. Each run seeds its own site.

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/livetest"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
	"github.com/shokki-engineering/freebusy/internal/service/reservation"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/oh-tarnished/runtime-go/ulid"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type backend struct {
	alloc allocation.Store
	res   reservation.Store
}

func backends(t *testing.T) (*gorm.DB, map[string]backend) {
	t.Helper()
	dsn := livetest.Env(t, livetest.DatabaseURL)
	gdb, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	gconn := &database.Connection{PgSQLConn: gdb, Provider: database.ProviderGorm}
	out := map[string]backend{"gorm": {alloc: db.New(gconn), res: New(gconn)}}
	if raw := livetest.Optional(t, livetest.GraphQLURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := freebusyql.Connect(u)
		if err != nil {
			t.Fatal(err)
		}
		hconn := &database.Connection{Hasura: svc, Provider: database.ProviderHasura}
		out["hasura"] = backend{alloc: db.New(hconn), res: New(hconn)}
	}
	return gdb, out
}

// seed creates one site with one court and no published availability, which
// the engine reads as always open.
func seed(t *testing.T, db *gorm.DB) (site, court string) {
	t.Helper()
	run := ulid.GenerateString()
	ou := "it-" + run
	res := &resource.Resource{ID: "court-" + run, Name: "resources/court-" + run, OrganizationalUnitID: &ou, ResourceTypes: []string{"padel-court"}}
	if err := db.Omit(clause.Associations).Create(res).Error; err != nil {
		t.Fatal(err)
	}
	return "organizationalUnits/" + ou, res.Name
}

func TestLiveReservations(t *testing.T) {
	db, bs := backends(t)
	start := time.Now().Add(14 * 24 * time.Hour).Truncate(time.Hour)
	win := func(h int) *interval.Interval {
		s := start.Add(time.Duration(h) * time.Hour)
		return &interval.Interval{StartTime: timestamppb.New(s), EndTime: timestamppb.New(s.Add(time.Hour))}
	}
	for name, b := range bs {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			site, _ := seed(t, db)
			engine := allocation.NewServer(b.alloc, allocation.WithHoldTTL(time.Second))
			srv := reservation.NewServer(b.res, engine)
			hold := func(h int) *allocationpbv1.Hold {
				t.Helper()
				out, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: site, Hold: &allocationpbv1.Hold{Window: win(h)}})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			free := func(h int) int32 {
				t.Helper()
				res, err := engine.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Parent: site, Window: win(h)})
				if err != nil {
					t.Fatal(err)
				}
				return res.GetFreeCount()
			}
			create := func(holdName string) (*reservationpbv1.Reservation, error) {
				attrs, _ := structpb.NewStruct(map[string]any{"players": 4})
				return srv.CreateReservation(ctx, &reservationpbv1.CreateReservationRequest{
					Parent:      site,
					Reservation: &reservationpbv1.Reservation{Hold: holdName, Customer: "users/ana", Attributes: attrs},
				})
			}

			t.Run("confirm, survive hold expiry, list, cancel", func(t *testing.T) {
				h := hold(10)
				r, err := create(h.GetName())
				if err != nil {
					t.Fatal(err)
				}
				if r.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || r.GetHold() != h.GetName() ||
					!r.GetStartTime().AsTime().Equal(win(10).GetStartTime().AsTime()) || len(r.GetResources()) != 1 ||
					r.GetAttributes().GetFields()["players"].GetNumberValue() != 4 || r.GetConfirmTime() == nil {
					t.Fatalf("reservation %v", r)
				}
				again, err := create(h.GetName())
				if err != nil || again.GetName() != r.GetName() || again.GetEtag() != r.GetEtag() {
					t.Fatalf("retry: %v %v", again, err)
				}

				time.Sleep(1200 * time.Millisecond) // past the 1s hold TTL
				if n := free(10); n != 0 {
					t.Fatalf("a confirmed reservation lost its court when the hold TTL passed: %d free", n)
				}

				list, err := srv.ListReservations(ctx, &reservationpbv1.ListReservationsRequest{
					Parent: site,
					Filter: `state = RESERVATION_STATE_CONFIRMED AND start_time >= "` + start.UTC().Format(time.RFC3339) + `"`,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(list.GetReservations()) != 1 || list.GetReservations()[0].GetHold() != h.GetName() {
					t.Fatalf("list %v", list.GetReservations())
				}

				for range 2 {
					c, err := srv.CancelReservation(ctx, &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Reason: "rain"})
					if err != nil || c.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED || c.GetCancelReason() != "rain" {
						t.Fatalf("cancel: %v %v", c, err)
					}
				}
				if n := free(10); n != 1 {
					t.Fatalf("cancelled reservation still holds its court: %d free", n)
				}
			})

			t.Run("lapsed hold", func(t *testing.T) {
				h := hold(12)
				time.Sleep(1200 * time.Millisecond)
				if _, err := create(h.GetName()); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("got %v", err)
				}
			})

			t.Run("reconciler finishes a stranded create", func(t *testing.T) {
				h := hold(14)
				name := site + "/reservations/" + h.GetName()[len(site+"/holds/"):]
				if _, err := b.res.Insert(ctx, &reservationpbv1.Reservation{
					Name: name, Hold: h.GetName(), Resources: h.GetResources(),
					StartTime: h.GetWindow().GetStartTime(), EndTime: h.GetWindow().GetEndTime(),
					State: reservationpbv1.ReservationState_RESERVATION_STATE_PENDING,
				}); err != nil {
					t.Fatal(err)
				}
				later := reservation.NewServer(b.res, engine, reservation.WithClock(func() time.Time { return time.Now().Add(time.Minute) }))
				if _, err := later.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
				got, err := srv.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: name})
				if err != nil || got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
					t.Fatalf("after reconcile: %v %v", got.GetState(), err)
				}
				hh, _ := engine.LookupHold(ctx, h.GetName())
				if hh.GetState() != allocationpbv1.HoldState_HOLD_STATE_CONVERTED {
					t.Fatalf("hold %v", hh.GetState())
				}
			})

			t.Run("reschedule", func(t *testing.T) {
				r, err := create(hold(18).GetName())
				if err != nil {
					t.Fatal(err)
				}
				nh := hold(19)
				rep, err := srv.RescheduleReservation(ctx, &reservationpbv1.RescheduleReservationRequest{Name: r.GetName(), Hold: nh.GetName()})
				if err != nil {
					t.Fatal(err)
				}
				if rep.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || rep.GetReplaces() != r.GetName() ||
					!rep.GetStartTime().AsTime().Equal(win(19).GetStartTime().AsTime()) {
					t.Fatalf("replacement %v", rep)
				}
				old, _ := srv.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: r.GetName()})
				if old.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED || old.GetReplacement() != rep.GetName() {
					t.Fatalf("original %v", old)
				}
				time.Sleep(1200 * time.Millisecond) // past the hold TTL: only conversions count now
				if a, b := free(18), free(19); a != 1 || b != 0 {
					t.Fatalf("after move: old window %d free (want 1), new window %d free (want 0)", a, b)
				}

				lapsed := hold(20)
				time.Sleep(1200 * time.Millisecond)
				if _, err := srv.RescheduleReservation(ctx, &reservationpbv1.RescheduleReservationRequest{Name: rep.GetName(), Hold: lapsed.GetName()}); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("move onto lapsed hold: %v", err)
				}
				kept, _ := srv.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: rep.GetName()})
				if kept.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || kept.GetReplacement() != "" || free(19) != 0 {
					t.Fatalf("a failed move must leave the booking as it was: %v, %d free", kept, free(19))
				}

				target := hold(21)
				var wg sync.WaitGroup
				names := make([]string, 6)
				errs := make([]error, 6)
				for i := range 6 {
					wg.Go(func() {
						out, err := srv.RescheduleReservation(ctx, &reservationpbv1.RescheduleReservationRequest{Name: rep.GetName(), Hold: target.GetName()})
						names[i], errs[i] = out.GetName(), err
					})
				}
				wg.Wait()
				for i := range 6 {
					if errs[i] != nil || names[i] != names[0] {
						t.Fatalf("concurrent move %d: %q %v", i, names[i], errs[i])
					}
				}
			})

			t.Run("concurrent holds with one idempotency key", func(t *testing.T) {
				key := "cart-" + ulid.GenerateString()
				var wg sync.WaitGroup
				names := make([]string, 8)
				errs := make([]error, 8)
				for i := range 8 {
					wg.Go(func() {
						h, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
							Parent: site, Hold: &allocationpbv1.Hold{Window: win(22), IdempotencyKey: key},
						})
						names[i], errs[i] = h.GetName(), err
					})
				}
				wg.Wait()
				for i := range 8 {
					if errs[i] != nil || names[i] != names[0] {
						t.Fatalf("caller %d: %q %v", i, names[i], errs[i])
					}
				}
			})

			t.Run("concurrent creates from one hold", func(t *testing.T) {
				h := hold(16)
				var wg sync.WaitGroup
				names := make([]string, 8)
				errs := make([]error, 8)
				for i := range 8 {
					wg.Go(func() {
						r, err := create(h.GetName())
						names[i], errs[i] = r.GetName(), err
					})
				}
				wg.Wait()
				for i := range 8 {
					if errs[i] != nil || names[i] != names[0] {
						t.Fatalf("caller %d: %q %v", i, names[i], errs[i])
					}
				}
				got, _ := srv.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: names[0]})
				if got.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
					t.Fatalf("state %v", got.GetState())
				}
			})
		})
	}
}
