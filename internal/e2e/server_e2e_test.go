// This file end-to-end tests the ASSEMBLED server: the real *internal.Service
// (every domain server on its provider-selected repository) behind the real
// protovalidate interceptor chain, served over an in-memory bufconn listener
// and driven through the generated gRPC clients — exactly the stack a
// production client talks to, minus the TCP socket.
//
// The booking flow is the availability engine's: a hold, then a reservation
// made of it, then a cancellation. The one resource it books is seeded through
// the generated RFC repository, because the engine books only resources it can
// find at a site -- the same rows an operator would publish.
//
// Two env-gated matrices mirror the live-suite conventions:
//
//	FREEBUSY_TEST_POSTGRES_DSN="host=... dbname=freebusydb ..." go test ./internal/e2e/ -run TestE2E_Gorm -v
//	FREEBUSY_TEST_GRAPHQL_URL="http://localhost:3280/graphql"  go test ./internal/e2e/ -run TestE2E_Hasura -v
//
// (run `just migrate` / `just hasura regen` first so the backends are ready).
package e2e

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/oh-tarnished/freebusy/internal"
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	rfcresource "github.com/oh-tarnished/freebusy/internal/database/repository/rfc/resource"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/pricing/v1/pricingpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestE2E_Gorm(t *testing.T) {
	gdb := openTestGorm(t)
	conn := &database.Connection{PgSQLConn: gdb, Provider: database.ProviderGorm}
	cc := dialServer(t, conn)
	serverLifecycle(t, cc, conn, rfcresource.New(repox.Conn{Gorm: gdb}))
}

// TestE2E_Hasura runs the identical client-visible lifecycle with the server
// assembled on the Hasura DDN backend — one behavior, two providers.
func TestE2E_Hasura(t *testing.T) {
	svc := connectTestGraphQL(t)
	conn := &database.Connection{Hasura: svc, Provider: database.ProviderHasura}
	cc := dialServer(t, conn)
	serverLifecycle(t, cc, conn, rfcresource.New(repox.Conn{GraphQL: svc}))
}

// e2eClients bundles the per-service gRPC clients plus the RFC repository used
// to seed and remove the resource the booking flow books -- freebusy serves no
// API for RFC resources, so the test writes them the way an operator's tooling
// would.
type e2eClients struct {
	suffix       string
	promos       promocodepbv1.PromoCodeServiceClient
	ratePlans    pricingpbv1.RatePlanServiceClient
	availability allocationpbv1.AllocationServiceClient
	holds        allocationpbv1.HoldServiceClient
	reservations reservationpbv1.ReservationServiceClient
	resources    rfcresource.Repositories
}

// serverLifecycle drives the services through the wire: interceptor rejections,
// promo validation, rate plans, and the hold-to-reservation flow. Each flow
// registers its own t.Cleanup, so teardown runs LIFO in dependency order.
func serverLifecycle(t *testing.T, cc *grpc.ClientConn, conn *database.Connection, resources rfcresource.Repositories) {
	t.Helper()
	c := &e2eClients{
		suffix:       fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000),
		promos:       promocodepbv1.NewPromoCodeServiceClient(cc),
		ratePlans:    pricingpbv1.NewRatePlanServiceClient(cc),
		availability: allocationpbv1.NewAllocationServiceClient(cc),
		holds:        allocationpbv1.NewHoldServiceClient(cc),
		reservations: reservationpbv1.NewReservationServiceClient(cc),
		resources:    resources,
	}

	rejectionFlow(t, c)
	promoFlow(t, c, conn.Provider)
	ratePlanFlow(t, c)
	reservationFlow(t, c)
}

// rejectionFlow pins that the protovalidate interceptor runs in the assembled
// server, not just in unit tests: a malformed name is refused before any
// repository is touched.
func rejectionFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()

	_, err := c.reservations.GetReservation(ctx, &reservationpbv1.GetReservationRequest{Name: "not-a-reservation-name"})
	wantCode(t, err, codes.InvalidArgument, "malformed reservation name")

	_, err = c.holds.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{
		Parent: "properties/p1",
		Hold:   &allocationpbv1.Hold{},
	})
	wantCode(t, err, codes.InvalidArgument, "a hold's parent must be a site")
}

// wantCode asserts err carries the expected gRPC status code.
func wantCode(t *testing.T, err error, want codes.Code, what string) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("%s: code = %v (err %v), want %v", what, status.Code(err), err, want)
	}
}

// dialServer assembles the full server on conn, serves it over bufconn, and
// returns a connected client conn. Everything is torn down with the test.
func dialServer(t *testing.T, conn *database.Connection) *grpc.ClientConn {
	t.Helper()
	srv, _, err := internal.NewGRPCServer(conn)
	if err != nil {
		t.Fatalf("assemble server: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}
