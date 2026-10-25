// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/the-protobuf-project/runtime-go/grpc"
	"github.com/the-protobuf-project/runtime-go/grpc/options"
)

// Server wraps a runtime HybridServer and provides lifecycle helpers
// (Start, Stop, Wait, Restart) for the freebusy service.
type Server struct {
	// options holds the gRPC/HTTP/MCP configuration for this server.
	options options.Options
	// hybridServer is the running rumtime server instance.
	hybridServer *grpc.HybridServer
	// svc is the assembled service, retained so its background tasks (the hold
	// sweeper) can be started against the server's lifecycle context in Start.
	svc *Service
	// ctx and cancel control the server's lifecycle context.
	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer creates a Server configured with name, version, and optional
// overrides. When no options are provided the defaults from config are used. It
// assembles the service instance (which opens the configured database backend),
// so it returns an error when that fails. Call Start() to begin serving.
func NewServer(name, version string, opts ...options.Options) (*Server, error) {
	var serverOpts options.Options
	if len(opts) == 0 {
		serverOpts = config.GetGRPCOptions()
	} else {
		serverOpts = opts[0]
	}
	serverOpts.ServiceName = name
	serverOpts.Version = version

	ctx, cancel := context.WithCancel(context.Background())
	svc, err := newServiceInstance(nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create service instance: %w", err)
	}

	serverOptions, err := serviceOptions(svc)
	if err != nil {
		cancel()
		return nil, err
	}

	return &Server{
		options:      serverOpts,
		hybridServer: grpc.NewHybridServer(serverOpts, serverOptions...),
		svc:          svc,
		ctx:          ctx,
		cancel:       cancel,
	}, nil
}

// serviceOptions builds the hybrid-server options: protovalidate request
// validation (enforced by runtime-go across gRPC, the HTTP gateway, and every
// MCP tool call), the gRPC/HTTP/MCP registrations, plus, when a
// certificate/key pair is configured, the TLS option. The pair is validated up
// front so a bad path returns a clear error instead of the panic
// grpc.WithCertificates would raise.
func serviceOptions(svc *Service) ([]grpc.Option, error) {
	opts := append([]grpc.Option{
		grpc.WithValidation(),
		// After validation (which runtime-go prepends): the chain every
		// surface shares -- the e2e harness included -- see
		// Services.Interceptors.
		grpc.WithUnaryInterceptors(svc.services.Interceptors()...),
		mountCalDAV(svc),
		// Every MCP service together, as one server an agent connects to
		// once. Each still answers at its own path as well, for now.
		grpc.WithMCPEndpoint(config.Get().Server.MCP.Path),
		// Bounds Stop: HTTP gateways drain, then gRPC, within this; 0 keeps
		// the runtime's default.
		grpc.WithShutdownTimeout(config.Get().Server.ShutdownTimeout()),
	},
		// Every service on gRPC, REST and MCP: the generated registrations
		// and the hand-written services beside them.
		svc.services.Options()...)
	if config.Get().Server.MCP.Stateless {
		// No sessions, at the combined path and the per-service ones alike:
		// any replica answers any MCP request.
		opts = append(opts, grpc.WithStatelessMCP())
	}
	tlsCfg := config.Get().Server.TLS
	if tlsCfg.Enabled() {
		if _, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile); err != nil {
			return nil, fmt.Errorf("load TLS certificate (%s, %s): %w", tlsCfg.CertFile, tlsCfg.KeyFile, err)
		}
		opts = append(opts, grpc.WithCertificates(tlsCfg.CertFile, tlsCfg.KeyFile))
	}
	return opts, nil
}

// Start begins serving gRPC, HTTP, and MCP traffic, and launches the service's
// background tasks (the hold sweeper) tied to the server context. Non-blocking.
func (s *Server) Start() error {
	if err := s.hybridServer.Start(); err != nil {
		return fmt.Errorf("failed to start hybrid server: %w", err)
	}
	// Dev-only baseline data (a known organisation), gated by [seed] and
	// idempotent across restarts. Non-fatal: a seeding failure is logged, not
	// allowed to keep the server from serving.
	s.svc.StartBackground(s.ctx)
	return nil
}

// Stop gracefully drains active connections then shuts the server down.
func (s *Server) Stop() error {
	s.cancel()
	if err := s.hybridServer.Stop(); err != nil {
		return fmt.Errorf("error stopping hybrid server: %w", err)
	}
	if err := s.hybridServer.Close(); err != nil {
		return fmt.Errorf("error closing hybrid server: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.svc.Close(ctx); err != nil {
		return fmt.Errorf("error flushing the search indexer: %w", err)
	}
	return nil
}

// Wait blocks until a SIGINT or SIGTERM signal is received, or until the
// server's context is cancelled programmatically.
func (s *Server) Wait() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
	case <-s.ctx.Done():
	}
}
