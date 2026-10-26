// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/internal/caldav"
	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/search"
	"github.com/shokki-engineering/freebusy/internal/service/webhook"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/events"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

// newServiceInstance assembles the freebusy services on one shared database
// connection (opened from config when conn is nil) and wraps them in the
// registered Service adapter. Called at startup, on every Restart, and — with
// an injected connection — by the e2e suites.
func newServiceInstance(conn *database.Connection) (*Service, error) {
	cfg := config.Get()
	hooks := cfg.Webhooks
	var previous []string
	for k := range strings.SplitSeq(hooks.PreviousSecretKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			previous = append(previous, k)
		}
	}
	sealer, err := events.NewSealer(hooks.SecretKey, previous)
	if err != nil {
		return nil, err
	}
	if sealer == nil && cfg.Server.Environment == "production" {
		telemetry.Warn("Webhook signing secrets are stored unsealed: set FREEBUSY_WEBHOOKS__SECRET_KEY to the output of `openssl rand -base64 32`")
	}
	if conn == nil {
		opened, err := database.Open()
		if err != nil {
			return nil, err
		}
		conn = opened
	}
	if conn.References == nil {
		refs, err := database.DialReferences(cfg.ReferenceServices)
		if err != nil {
			return nil, err
		}
		conn.References = refs
	}
	var osearch *search.OpenSearch
	if cfg.Search.UsesOpenSearch() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		osearch, err = search.Open(ctx, cfg.Search.OpenSearch)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	services := New(conn, Settings{
		OpenSearch:     osearch,
		Retention:      cfg.Retention.Period(),
		MaxEvaluations: cfg.Engine.MaxEvaluations,
		AdmissionWait:  cfg.Engine.AdmissionWait(),
		HorizonDays:    cfg.Search.Horizon(),
		Webhooks: webhook.Options{
			AllowInsecureEndpoints: hooks.AllowInsecureEndpoints,
			AllowPrivateNetworks:   hooks.AllowPrivateNetworks,
			MaxAttempts:            hooks.MaxAttempts,
			Retention:              time.Duration(hooks.RetentionDays) * 24 * time.Hour,
			Sealer:                 sealer,
		},
	})
	svc := NewService(services)
	svc.search = osearch
	// One CalDAV handler answers both the CalDAV tree, when enabled, and the
	// read-only feeds, when they have a secret.
	dav := caldav.New(services.Repos, services.Allocation)
	if cfg.CalDAV.Enabled {
		svc.caldav = dav
	}
	if cfg.Feeds.Secret != "" {
		if svc.feed, err = caldav.NewFeed(dav, []byte(cfg.Feeds.Secret)); err != nil {
			return nil, err
		}
	}
	return svc, nil
}

// NewGRPCServer assembles the freebusy service on conn (opened from config
// when nil) and returns a standalone gRPC server carrying the production
// chain -- validation, then Services.Interceptors -- with every service
// registered. It is the seam the e2e
// suites serve over bufconn; the caller owns Serve and Stop.
func NewGRPCServer(conn *database.Connection) (*grpc.GRPCServer, *Service, error) {
	svc, err := newServiceInstance(conn)
	if err != nil {
		return nil, nil, err
	}
	srv, err := grpc.NewStandaloneGRPCServer(svc.services.Interceptors()...)
	if err != nil {
		return nil, nil, err
	}
	svc.services.Register(srv)
	return srv, svc, nil
}

// mountCalDAV returns a server option that mounts CalDAV and the feeds on
// the HTTP gateway's mux, each only when it is on.
func mountCalDAV(svc *Service) grpc.Option {
	return grpc.WithHTTPGateways(func(mux *grpc.ServeMux, _ string, _ []grpc.DialOption) error {
		if svc.caldav != nil {
			if err := svc.caldav.Mount(mux); err != nil {
				return err
			}
		}
		if svc.feed != nil {
			return svc.feed.Mount(mux)
		}
		return nil
	})
}
