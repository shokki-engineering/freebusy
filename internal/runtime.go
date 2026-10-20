package internal

import (
	"context"
	"github.com/oh-tarnished/freebusy/internal/service/pricing"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/pricing/v1/pricingpbv1"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	allocationdb "github.com/oh-tarnished/freebusy/internal/service/allocation/db"
	"github.com/oh-tarnished/freebusy/internal/service/promocode"
	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	reservationdb "github.com/oh-tarnished/freebusy/internal/service/reservation/db"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

// newServiceInstance assembles the freebusy services on one shared database
// connection (opened from config when conn is nil) and wraps them in the
// registered Service adapter. Called at startup, on every Restart, and — with
// an injected connection — by the e2e suites.
func newServiceInstance(conn *database.Connection) (*Service, error) {
	if conn == nil {
		opened, err := database.Open()
		if err != nil {
			return nil, err
		}
		conn = opened
	}
	engine := allocation.NewServer(allocationdb.New(conn))
	svc := NewService(
		promocode.New(conn),
		pricing.New(conn),
		engine,
		reservation.NewServer(reservationdb.New(conn), engine),
	)
	svc.conn = conn
	return svc, nil
}

// registerGRPCServers returns a server option that registers the freebusy gRPC
// servers with the hybrid server's gRPC multiplexer.
func registerGRPCServers(svc *Service) grpc.Option {
	return grpc.WithGRPCServers(func(s *grpc.GRPCServer) {
		registerServices(s, svc)
	})
}

// registerServices registers every freebusy service on a gRPC server — the
// hybrid server's multiplexer in production, a standalone server in the e2e
// suites.
func registerServices(s *grpc.GRPCServer, svc *Service) {
	promocodepbv1.RegisterPromoCodeServiceServer(s, svc)
	pricingpbv1.RegisterRatePlanServiceServer(s, svc)
	allocationpbv1.RegisterAllocationServiceServer(s, svc)
	allocationpbv1.RegisterHoldServiceServer(s, svc)
	reservationpbv1.RegisterReservationServiceServer(s, svc)
}

// NewGRPCServer assembles the freebusy service on conn (opened from config
// when nil) and returns a standalone gRPC server carrying the production
// validation chain with every service registered. It is the seam the e2e
// suites serve over bufconn; the caller owns Serve and Stop.
func NewGRPCServer(conn *database.Connection) (*grpc.GRPCServer, *Service, error) {
	svc, err := newServiceInstance(conn)
	if err != nil {
		return nil, nil, err
	}
	srv, err := grpc.NewStandaloneGRPCServer()
	if err != nil {
		return nil, nil, err
	}
	registerServices(srv, svc)
	return srv, svc, nil
}

// registerHTTPGateways returns a server option that registers the REST gateways
// so protobuf RPCs are also reachable over HTTP/JSON.
func registerHTTPGateways() grpc.Option {
	return grpc.WithHTTPGateways(func(mux *grpc.ServeMux, endpoint string, opts []grpc.DialOption) error {
		if err := promocodepbv1.RegisterPromoCodeServiceHandlerFromEndpoint(context.Background(), mux, endpoint, opts); err != nil {
			return err
		}
		if err := pricingpbv1.RegisterRatePlanServiceHandlerFromEndpoint(context.Background(), mux, endpoint, opts); err != nil {
			return err
		}
		if err := allocationpbv1.RegisterAllocationServiceHandlerFromEndpoint(context.Background(), mux, endpoint, opts); err != nil {
			return err
		}
		if err := allocationpbv1.RegisterHoldServiceHandlerFromEndpoint(context.Background(), mux, endpoint, opts); err != nil {
			return err
		}
		return reservationpbv1.RegisterReservationServiceHandlerFromEndpoint(context.Background(), mux, endpoint, opts)
	})
}

// registerMCPServices returns a server option that exposes the freebusy
// services over the Model Context Protocol, making them discoverable by LLM
// tool routers. Each service is its own MCPServiceFunc, so the hybrid server
// gives each a listener of its own (base MCP port + index) and pushes its
// unary interceptor chain — including request validation — into every tool
// call.
func registerMCPServices(svc *Service) grpc.Option {
	return grpc.WithMCPServices(
		func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
			return promocodepbv1.ServePromoCodeServiceMCP(ctx, svc, cfg)
		},
		func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
			return allocationpbv1.ServeAllocationServiceMCP(ctx, svc, cfg)
		},
		func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
			return allocationpbv1.ServeHoldServiceMCP(ctx, svc, cfg)
		},
		func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
			return reservationpbv1.ServeReservationServiceMCP(ctx, svc, cfg)
		},
	)
}
