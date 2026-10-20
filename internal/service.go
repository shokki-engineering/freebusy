// Package internal is the transport/bootstrap layer: it builds the hybrid
// gRPC/HTTP/MCP server and registers the freebusy services assembled by
// internal/runtime. The protobuf/gRPC translation lives under internal/runtime;
// the database layer stays agnostic to it.
package internal

import (
	"context"

	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/pricing"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/pricing/v1/pricingpbv1"
	"github.com/oh-tarnished/freebusy/shared"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/scheduling"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/schedule/v1/schedulepbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/scheduling/v1/schedulingpbv1"
)

// Service is the registered gRPC adapter. It embeds the assembled service
// implementations, so it satisfies each of their gRPC server interfaces
// (promocode, pricing, schedule, scheduling, and any future service
// interfaces composed in here).
type Service struct {
	promocodepbv1.PromoCodeServiceServer
	schedulepbv1.ScheduleServiceServer
	schedulingpbv1.SchedulingServiceServer
	pricingpbv1.RatePlanServiceServer
	allocationpbv1.AllocationServiceServer
	allocationpbv1.HoldServiceServer

	// allocation is the availability engine's server, retained so its hold
	// sweeper can be started in StartBackground.
	allocation *allocation.Server
	// scheduling is the concrete scheduling server, retained so background tasks (the
	// hold sweeper) can be started against it in StartBackground.
	scheduling *scheduling.Server
	// conn is the shared database connection every domain runs on, retained so
	// StartBackground can publish its pool health.
	conn *database.Connection
}

// NewService wraps the assembled service servers as the registered Service. The
// scheduling server is passed as its concrete type so its background hold sweeper can
// be started; it still satisfies schedulingpbv1.SchedulingServiceServer for embedding.
// The property server is concrete for the same kind of reason: it implements
// both the PropertyService and the LicenceService.
func NewService(
	promoCode promocodepbv1.PromoCodeServiceServer,
	schedule schedulepbv1.ScheduleServiceServer,
	scheduling *scheduling.Server,
	ratePlans *pricing.Server,
	engine *allocation.Server,
) *Service {
	return &Service{
		PromoCodeServiceServer:  promoCode,
		ScheduleServiceServer:   schedule,
		SchedulingServiceServer: scheduling,
		RatePlanServiceServer:   ratePlans,
		AllocationServiceServer: engine,
		HoldServiceServer:       engine,
		scheduling:              scheduling,
		allocation:              engine,
	}
}

// StartBackground launches the service's background tasks, tied to ctx: the
// scheduling and allocation hold sweepers, which converges lapsed holds' stored state, and the
// database pool monitor, which publishes the shared connection pool's health.
// The goroutines exit when ctx is cancelled (on server Stop/Restart).
func (s *Service) StartBackground(ctx context.Context) {
	if s.scheduling != nil {
		s.scheduling.StartHoldSweeper(ctx, 0)
	}
	if s.allocation != nil {
		s.allocation.StartHoldSweeper(ctx, 0, func(err error) {
			_ = shared.Telemetry.Logger.Error("allocation hold sweeper failed", map[string]any{"error": err.Error()})
		})
	}
	database.StartPoolMonitor(ctx, s.conn, 0)
}
