package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/runtime/idempotency"
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
	opts := []grpc.Option{
		grpc.WithValidation(),
		// Honours `request_id` on every RPC that declares it. Runs after
		// validation (which runtime-go prepends), so a request that never
		// passed validation cannot claim an idempotency key.
		grpc.WithUnaryInterceptors(idempotency.New(svc.conn)),
		registerGRPCServers(svc),
		registerHTTPGateways(svc),
		registerMCPServices(svc),
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

// Restart performs a stop-then-start cycle, recreating the service instance
// so that any in-memory state (e.g. configured defaults) is reset.
func (s *Server) Restart() error {
	if err := s.Stop(); err != nil {
		return fmt.Errorf("failed to stop server during restart: %w", err)
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())
	svc, err := newServiceInstance(nil)
	if err != nil {
		return fmt.Errorf("recreate service instance during restart: %w", err)
	}
	s.svc = svc

	serverOptions, err := serviceOptions(svc)
	if err != nil {
		return fmt.Errorf("rebuild server options during restart: %w", err)
	}
	s.hybridServer = grpc.NewHybridServer(s.options, serverOptions...)

	if err := s.Start(); err != nil {
		return fmt.Errorf("failed to start server during restart: %w", err)
	}
	return nil
}
