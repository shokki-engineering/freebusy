// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server is the transport/bootstrap layer: it assembles freebusy's
// services on one database connection (assembly.go), puts them on the hybrid
// gRPC/HTTP/MCP server through the generated registrations (register.go), and
// builds the unary interceptor chain every surface runs them behind -- hints,
// the site check (sites.go) and idempotency. The server and the e2e suites
// assemble freebusy here, so the two cannot drift.
package server

import (
	"cmp"
	"context"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organization/v1/organizationv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/calendar/v1/calendarv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/event/v1/eventv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc7953/availability/v1/availabilityv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc9073/resource/v1/resourcev1grpc"
	"github.com/shokki-engineering/freebusy/internal/caldav"
	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/search"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
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
	organizationv1grpc.OrganizationsServer
	organizational_unitv1grpc.OrganizationalUnitsServer
	resourcev1grpc.ResourcesServer
	calendarv1grpc.CalendarsServer
	availabilityv1grpc.AvailabilitiesServer
	eventv1grpc.EventsServer

	// services are the assembled implementations, retained so their
	// background work can be started and their interceptor chain built.
	services *Services
	// caldav is the CalDAV server, mounted on the HTTP gateway's mux; nil
	// unless [caldav] enabled.
	caldav *caldav.Handler
	// search is the read model's OpenSearch copy; nil unless [search]
	// provider = "opensearch".
	search *search.OpenSearch
}

// Close flushes what the service holds open beyond the database: the
// OpenSearch indexer's queue.
func (s *Service) Close(ctx context.Context) error {
	if s.search == nil {
		return nil
	}
	return s.search.Close(ctx)
}

// NewService wraps the assembled services as the registered Service.
func NewService(services *Services) *Service {
	return &Service{
		AllocationServiceServer:   services.Allocation,
		HoldServiceServer:         services.Allocation,
		ReservationServiceServer:  services.Reservations,
		WebhookServiceServer:      services.Webhooks,
		OrganizationsServer:       services.Organizations,
		OrganizationalUnitsServer: services.Units,
		ResourcesServer:           services.Resources,
		CalendarsServer:           services.Calendars,
		AvailabilitiesServer:      services.Availabilities,
		EventsServer:              services.Events,
		services:                  services,
	}
}

// StartBackground launches the service's background tasks, tied to ctx: the
// hold sweeper, which converges lapsed holds' stored state; the reservation
// reconciler, which finishes reservations left mid-transition; and the
// database pool monitor, which publishes the shared connection pool's health;
// when [webhooks] enabled, webhook delivery; and when [retention] purge, the
// purger, which ends soft deletes past their retention.
// The goroutines exit when ctx is cancelled (on server Stop/Restart).
func (s *Service) StartBackground(ctx context.Context) {
	cfg := config.Get()
	s.services.Start(ctx, Background{
		Converge:        true,
		RebuildOnStart:  cfg.Search.RebuildMode(),
		ReindexOnStart:  cfg.Search.OpenSearch.ReindexMode(),
		Purge:           cfg.Retention.Purge,
		Webhooks:        cfg.Webhooks.Enabled,
		WebhookInterval: time.Duration(cmp.Or(cfg.Webhooks.IntervalSeconds, 5)) * time.Second,
		PoolMonitor:     true,
		Report: func(task string, err error) {
			telemetry.Error(task+" failed", map[string]any{"error": err.Error()})
		},
	})
}
