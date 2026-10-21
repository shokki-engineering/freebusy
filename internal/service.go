// Package internal is the transport/bootstrap layer: it builds the hybrid
// gRPC/HTTP/MCP server and registers the freebusy services assembled by
// internal/runtime. The protobuf/gRPC translation lives under internal/runtime;
// the database layer stays agnostic to it.
package internal

import (
	"cmp"
	"context"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/calendar/v1/calendarv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/event/v1/eventv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc7953/availability/v1/availabilityv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc9073/resource/v1/resourcev1grpc"
	"github.com/oh-tarnished/freebusy/config"
	"github.com/oh-tarnished/freebusy/internal/caldav"
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/internal/service/rfcsvc"
	"github.com/oh-tarnished/freebusy/internal/service/webhooksvc"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"github.com/oh-tarnished/freebusy/shared"
)

// Service is the registered gRPC adapter. It embeds the assembled service
// implementations, so it satisfies each of their gRPC server interfaces: the
// availability engine's allocation and hold services, reservations, webhooks,
// and the upstream protobuf-rfc services an operator publishes inventory
// through (Resources, Calendars, Availabilities, Events). Money -- prices, promo
// codes, payment -- is deliberately absent; it belongs to the payment service,
// which references bookings here by name.
type Service struct {
	allocationpbv1.AllocationServiceServer
	allocationpbv1.HoldServiceServer
	reservationpbv1.ReservationServiceServer
	webhookpbv1.WebhookServiceServer
	resourcev1grpc.ResourcesServer
	calendarv1grpc.CalendarsServer
	availabilityv1grpc.AvailabilitiesServer
	eventv1grpc.EventsServer

	// allocation is the availability engine's server, retained so its hold
	// sweeper can be started in StartBackground.
	allocation *allocation.Server
	// reservations is retained so its reconciler can be started in
	// StartBackground.
	reservations *reservation.Server
	// webhooks is retained so its delivery can be started in
	// StartBackground.
	webhooks *webhooksvc.Server
	// conn is the shared database connection every domain runs on, retained so
	// StartBackground can publish its pool health.
	conn *database.Connection
	// caldav is the CalDAV server, mounted on the HTTP gateway's mux; nil
	// unless [caldav] enabled.
	caldav *caldav.Handler
}

// NewService wraps the assembled service servers as the registered Service.
// The engine and reservation servers are passed as concrete types so their
// background work can be started; the engine serves both AllocationService and
// HoldService, and answers the Calendars service's QueryFreeBusy.
func NewService(
	engine *allocation.Server,
	reservations *reservation.Server,
	webhooks *webhooksvc.Server,
	repos rfcsvc.Repositories,
) *Service {
	return &Service{
		AllocationServiceServer:  engine,
		HoldServiceServer:        engine,
		ReservationServiceServer: reservations,
		WebhookServiceServer:     webhooks,
		ResourcesServer:          rfcsvc.NewResourceServer(repos),
		CalendarsServer:          rfcsvc.NewCalendarServer(repos, engine),
		AvailabilitiesServer:     rfcsvc.NewAvailabilityServer(repos),
		EventsServer:             rfcsvc.NewEventServer(repos),
		allocation:               engine,
		reservations:             reservations,
		webhooks:                 webhooks,
	}
}

// StartBackground launches the service's background tasks, tied to ctx: the
// hold sweeper, which converges lapsed holds' stored state; the reservation
// reconciler, which finishes reservations left mid-transition; and the
// database pool monitor, which publishes the shared connection pool's health;
// and, when [webhooks] enabled, webhook delivery.
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
	if cfg := config.Get().Webhooks; s.webhooks != nil && cfg.Enabled {
		interval := time.Duration(cmp.Or(cfg.IntervalSeconds, 5)) * time.Second
		s.webhooks.Start(ctx, interval, func(err error) {
			_ = shared.Telemetry.Logger.Error("webhook delivery failed", map[string]any{"error": err.Error()})
		})
	}
	database.StartPoolMonitor(ctx, s.conn, 0)
}
