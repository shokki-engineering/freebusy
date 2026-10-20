// Package internal is the transport/bootstrap layer: it builds the hybrid
// gRPC/HTTP/MCP server and registers the freebusy services assembled by
// internal/runtime. The protobuf/gRPC translation lives under internal/runtime;
// the database layer stays agnostic to it.
package internal

import (
	"context"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/pricing"
	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/pricing/v1/pricingpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/oh-tarnished/freebusy/shared"
)

// Service is the registered gRPC adapter. It embeds the assembled service
// implementations, so it satisfies each of their gRPC server interfaces
// (promocode, pricing, the availability engine's allocation and hold services,
// reservations, and any future service interfaces composed in here).
type Service struct {
	promocodepbv1.PromoCodeServiceServer
	pricingpbv1.RatePlanServiceServer
	allocationpbv1.AllocationServiceServer
	allocationpbv1.HoldServiceServer
	reservationpbv1.ReservationServiceServer

	// allocation is the availability engine's server, retained so its hold
	// sweeper can be started in StartBackground.
	allocation *allocation.Server
	// reservations is retained so its reconciler can be started in
	// StartBackground.
	reservations *reservation.Server
	// conn is the shared database connection every domain runs on, retained so
	// StartBackground can publish its pool health.
	conn *database.Connection
}

// NewService wraps the assembled service servers as the registered Service.
// The engine and reservation servers are passed as concrete types so their
// background work can be started; the engine serves both AllocationService and
// HoldService.
func NewService(
	promoCode promocodepbv1.PromoCodeServiceServer,
	ratePlans *pricing.Server,
	engine *allocation.Server,
	reservations *reservation.Server,
) *Service {
	return &Service{
		PromoCodeServiceServer:   promoCode,
		RatePlanServiceServer:    ratePlans,
		AllocationServiceServer:  engine,
		HoldServiceServer:        engine,
		ReservationServiceServer: reservations,
		allocation:               engine,
		reservations:             reservations,
	}
}

// StartBackground launches the service's background tasks, tied to ctx: the
// hold sweeper, which converges lapsed holds' stored state; the reservation
// reconciler, which finishes reservations left mid-transition; and the
// database pool monitor, which publishes the shared connection pool's health.
// The goroutines exit when ctx is cancelled (on server Stop/Restart).
func (s *Service) StartBackground(ctx context.Context) {
	if s.reservations != nil {
		s.reservations.StartReconciler(ctx, 0, func(err error) {
			_ = shared.Telemetry.Logger.Error("reservation reconciler failed", map[string]any{"error": err.Error()})
		})
	}
	if s.allocation != nil {
		s.allocation.StartHoldSweeper(ctx, 0, func(err error) {
			_ = shared.Telemetry.Logger.Error("allocation hold sweeper failed", map[string]any{"error": err.Error()})
		})
	}
	database.StartPoolMonitor(ctx, s.conn, 0)
}
