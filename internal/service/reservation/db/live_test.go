// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

// Live integration tests: reservations end to end on a real database, once per
// backend, driving the real engine.
//
//	FREEBUSY_TEST_DATABASE_URL="host=127.0.0.1 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" \
//	FREEBUSY_TEST_GRAPHQL_URL=http://localhost:3280/graphql \
//	  go test ./internal/service/reservation/db/ -run Live -v
//
// Fixtures are seeded through GORM for both runs. Each run seeds its own court
// and asks only about it.

import (
	"context"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
	"github.com/shokki-engineering/freebusy/internal/service/reservation"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/shokki-engineering/freebusy/test/live"
	"github.com/the-protobuf-project/runtime-go/ulid"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// holdTTL is short so the suite can watch holds lapse, but long enough that a
// hold outlives the reservation made from it on a loaded machine: at one
// second, the race detector and a full parallel run made that a coin toss.
const (
	holdTTL = 3 * time.Second
	pastTTL = holdTTL + 200*time.Millisecond
)

type backend struct {
	alloc allocation.Store
	res   reservation.Store
}

func backends(t *testing.T) (*gorm.DB, map[string]backend) {
	t.Helper()
	dsn := live.Env(t, live.DatabaseURL)
	gdb, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	gconn := &database.Connection{PgSQLConn: gdb, Provider: database.ProviderGorm}
	out := map[string]backend{"gorm": {alloc: db.New(gconn), res: New(gconn)}}
	if raw := live.Optional(t, live.GraphQLURL); raw != "" {
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

// seed creates one court with no published availability, which the engine
// reads as always open.
func seed(t *testing.T, db *gorm.DB) (court string) {
	t.Helper()
	run := ulid.Generate().String()
	res := &resource.Resource{ID: "court-" + run, Name: "resources/court-" + run, ResourceTypes: []string{"padel-court"}}
	if err := db.Omit(clause.Associations).Create(res).Error; err != nil {
		t.Fatal(err)
	}
	return res.Name
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
			court := []string{seed(t, db)}
			engine := allocation.NewServer(b.alloc, allocation.WithHoldTTL(holdTTL))
			srv := reservation.NewServer(b.res, engine)
			hold := func(h int) *allocationpbv1.Hold {
				t.Helper()
				out, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Hold: &allocationpbv1.Hold{Window: win(h), CandidateResources: court}})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			free := func(h int) int32 {
				t.Helper()
				res, err := engine.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Resources: court, Window: win(h)})
				if err != nil {
					t.Fatal(err)
				}
				return res.GetFreeCount()
			}
			create := func(holdName string) (*reservationpbv1.Reservation, error) {
				attrs, _ := structpb.NewStruct(map[string]any{"players": 4})
				return srv.CreateReservation(ctx, &reservationpbv1.CreateReservationRequest{
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
					r.GetAttributes().GetFields()["players"].GetNumberValue() != 4 || r.GetConfirmTime() == nil ||
					r.GetLastChange() != reservationpbv1.Reservation_CHANGE_STATE {
					t.Fatalf("reservation %v", r)
				}
				again, err := create(h.GetName())
				if err != nil || again.GetName() != r.GetName() || again.GetEtag() != r.GetEtag() {
					t.Fatalf("retry: %v %v", again, err)
				}

				time.Sleep(pastTTL)
				if n := free(10); n != 0 {
					t.Fatalf("a confirmed reservation lost its court when the hold TTL passed: %d free", n)
				}

				list, err := srv.ListReservations(ctx, &reservationpbv1.ListReservationsRequest{
					Filter: `state = RESERVATION_STATE_CONFIRMED AND hold = "` + h.GetName() + `"`,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(list.GetReservations()) != 1 || list.GetReservations()[0].GetHold() != h.GetName() {
					t.Fatalf("list %v", list.GetReservations())
				}

				// UpdateReservation marks its write CHANGE_DETAILS; the cancel
				// after it is a change of state again.
				u, err := srv.UpdateReservation(ctx, &reservationpbv1.UpdateReservationRequest{
					Reservation: &reservationpbv1.Reservation{Name: r.GetName(), Customer: "users/ben"},
					UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"customer"}},
				})
				if err != nil || u.GetCustomer() != "users/ben" || u.GetLastChange() != reservationpbv1.Reservation_CHANGE_DETAILS ||
					u.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED {
					t.Fatalf("update: %v %v", u, err)
				}

				for range 2 {
					c, err := srv.CancelReservation(ctx, &reservationpbv1.CancelReservationRequest{Name: r.GetName(), Reason: "rain"})
					if err != nil || c.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED || c.GetCancelReason() != "rain" ||
						c.GetLastChange() != reservationpbv1.Reservation_CHANGE_STATE || c.GetCustomer() != "users/ben" {
						t.Fatalf("cancel: %v %v", c, err)
					}
				}
				if n := free(10); n != 1 {
					t.Fatalf("cancelled reservation still holds its court: %d free", n)
				}
			})

			t.Run("lapsed hold", func(t *testing.T) {
				h := hold(12)
				time.Sleep(pastTTL)
				if _, err := create(h.GetName()); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("got %v", err)
				}
			})

			t.Run("reconciler finishes a stranded create", func(t *testing.T) {
				h := hold(14)
				name := "reservations/" + h.GetName()[len("holds/"):]
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
				time.Sleep(pastTTL) // only conversions count now
				if a, b := free(18), free(19); a != 1 || b != 0 {
					t.Fatalf("after move: old window %d free (want 1), new window %d free (want 0)", a, b)
				}

				lapsed := hold(20)
				time.Sleep(pastTTL)
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

			t.Run("extend in place", func(t *testing.T) {
				// There is one court: extending 22:00-23:00 to 22:00-24:00
				// can only keep it if the booking's own claim does not count.
				r, err := create(hold(22).GetName())
				if err != nil {
					t.Fatal(err)
				}
				longer := &interval.Interval{StartTime: win(22).GetStartTime(), EndTime: win(23).GetEndTime()}
				if _, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Hold: &allocationpbv1.Hold{Window: longer, CandidateResources: court}}); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("a plain hold over the booking's own time: %v, want FailedPrecondition", err)
				}
				nh, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Hold: &allocationpbv1.Hold{Window: longer, CandidateResources: court, Replaces: r.GetHold()}})
				if err != nil {
					t.Fatalf("hold replacing the booking: %v", err)
				}
				if !slices.Equal(nh.GetResources(), r.GetResources()) {
					t.Fatalf("the extension moved courts: %v, was %v", nh.GetResources(), r.GetResources())
				}
				// The hour added is the new hold's; the hour shared is still the
				// booking's. Neither is free to anyone else.
				if a, b := free(22), free(23); a != 0 || b != 0 {
					t.Fatalf("while the extension is held: %d and %d free, want 0 and 0", a, b)
				}
				// Only the move of that booking may use it.
				if _, err := create(nh.GetName()); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("CreateReservation from a replacing hold: %v, want FailedPrecondition", err)
				}
				rep, err := srv.RescheduleReservation(ctx, &reservationpbv1.RescheduleReservationRequest{Name: r.GetName(), Hold: nh.GetName()})
				if err != nil {
					t.Fatal(err)
				}
				if rep.GetState() != reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED || !slices.Equal(rep.GetResources(), r.GetResources()) ||
					!rep.GetEndTime().AsTime().Equal(longer.GetEndTime().AsTime()) {
					t.Fatalf("replacement %v", rep)
				}
				time.Sleep(pastTTL) // only conversions count now
				if a, b := free(22), free(23); a != 0 || b != 0 {
					t.Fatalf("after the extension: %d and %d free, want 0 and 0 -- the hand-over left a gap", a, b)
				}
				if _, err := srv.CancelReservation(ctx, &reservationpbv1.CancelReservationRequest{Name: rep.GetName()}); err != nil {
					t.Fatal(err)
				}
				if a, b := free(22), free(23); a != 1 || b != 1 {
					t.Fatalf("after cancelling the extension: %d and %d free, want 1 and 1", a, b)
				}
			})

			t.Run("concurrent holds with one idempotency key", func(t *testing.T) {
				key := "cart-" + ulid.Generate().String()
				var wg sync.WaitGroup
				names := make([]string, 8)
				errs := make([]error, 8)
				for i := range 8 {
					wg.Go(func() {
						h, err := engine.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
							Hold: &allocationpbv1.Hold{Window: win(22), CandidateResources: court, IdempotencyKey: key},
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
