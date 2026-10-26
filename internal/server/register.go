// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// register.go puts the services on the server's three surfaces -- gRPC, the
// REST gateway and MCP -- through each package's generated RegisterAll, and
// by hand only the two services protoc-gen-service leaves out (HoldService and
// ReservationService, whose every method is business logic) and the store's
// ReferenceService.

import (
	"context"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/webhook/v1/webhookpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/calendar/v1/calendarv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/event/v1/eventv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc7953/availability/v1/availabilityv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc9073/resource/v1/resourcev1svc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// Options are the hybrid-server options that serve every service on gRPC,
// REST and MCP.
func (s *Services) Options() []grpc.Option {
	var opts []grpc.Option
	for _, o := range [][]grpc.Option{
		allocationpbv1svc.RegisterAll(allocationpbv1svc.Servers{AllocationService: s.Allocation}),
		webhookpbv1svc.RegisterAll(webhookpbv1svc.Servers{WebhookService: s.Webhooks}),
		resourcev1svc.RegisterAll(resourcev1svc.Servers{Resources: s.Resources}),
		calendarv1svc.RegisterAll(calendarv1svc.Servers{Calendars: s.Calendars}),
		availabilityv1svc.RegisterAll(availabilityv1svc.Servers{Availabilities: s.Availabilities}),
		eventv1svc.RegisterAll(eventv1svc.Servers{Events: s.Events}),
	} {
		opts = append(opts, o...)
	}
	return append(opts,
		grpc.WithGRPCServers(s.registerHandWritten),
		grpc.WithHTTPGateways(func(mux *grpc.ServeMux, endpoint string, dial []grpc.DialOption) error {
			if err := allocationpbv1.RegisterHoldServiceHandlerFromEndpoint(context.Background(), mux, endpoint, dial); err != nil {
				return err
			}
			return reservationpbv1.RegisterReservationServiceHandlerFromEndpoint(context.Background(), mux, endpoint, dial)
		}),
		grpc.WithMCPServices(
			func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
				return allocationpbv1.ServeHoldServiceMCP(ctx, s.Allocation, cfg)
			},
			func(ctx context.Context, cfg *grpc.MCPServerConfig) error {
				return reservationpbv1.ServeReservationServiceMCP(ctx, s.Reservations, cfg)
			},
		),
	)
}

// Register registers every service on a standalone gRPC server, the one the
// end-to-end suites serve over bufconn.
func (s *Services) Register(g *grpc.GRPCServer) {
	allocationpbv1svc.RegisterGRPC(g, allocationpbv1svc.Servers{AllocationService: s.Allocation})
	webhookpbv1svc.RegisterGRPC(g, webhookpbv1svc.Servers{WebhookService: s.Webhooks})
	resourcev1svc.RegisterGRPC(g, resourcev1svc.Servers{Resources: s.Resources})
	calendarv1svc.RegisterGRPC(g, calendarv1svc.Servers{Calendars: s.Calendars})
	availabilityv1svc.RegisterGRPC(g, availabilityv1svc.Servers{Availabilities: s.Availabilities})
	eventv1svc.RegisterGRPC(g, eventv1svc.Servers{Events: s.Events})
	s.registerHandWritten(g)
}

// registerHandWritten registers the services no generated registration
// carries.
func (s *Services) registerHandWritten(g *grpc.GRPCServer) {
	allocationpbv1.RegisterHoldServiceServer(g, s.Allocation)
	reservationpbv1.RegisterReservationServiceServer(g, s.Reservations)
	// store.v1.ReferenceService answers which freebusy records name a
	// resource another service keeps (a reservation's customer), so that
	// service can apply each field's on_delete before deleting it. Served
	// only when the generated layer has such fields.
	if sources := referrerSources(s.Conn.Repox()); len(sources) > 0 {
		storepbv1.RegisterReferenceServiceServer(g, repox.NewReferenceServer(sources...))
	}
}
