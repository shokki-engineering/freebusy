// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package main provides the application shell for the freebusy service.
// It wires together configuration, the gRPC/HTTP server, and shared
// resources (logging, telemetry) behind a simple Start / Wait / Stop API.
package main

import (
	"fmt"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/server"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

// App is the top-level application instance. It owns the gRPC/HTTP server
// and exposes lifecycle methods for the main goroutine to call.
type App struct {
	// Name is the service name read from configuration.
	Name string
	// Version is the service version read from the VERSION file.
	Version string
	// server is the underlying hybrid gRPC/HTTP/MCP server.
	server *server.Server
}

// InitializeApp builds the process's telemetry pipeline, loads the
// configuration, and creates an App from it. It validates that name and
// version are present, but does not start the server.
func InitializeApp() *App {
	// Telemetry first, then configuration: what used to happen as the
	// packages initialised, now explicit, in the same order. Neither can be
	// done without, so a failure stops the process as it always did.
	if err := telemetry.Init(); err != nil {
		fmt.Printf("ERROR: Failed to create Telemetry: %v\n", err)
		panic(err)
	}
	cfg, err := config.Load()
	if err != nil {
		panic(err.Error())
	}

	if cfg.Meta.Name == "" || cfg.Meta.Version == "" {
		telemetry.Error("Invalid configuration: name or version is empty",
			"name", cfg.Meta.Name,
			"version", cfg.Meta.Version)
		panic("cannot initialize app with empty name or version from config")
	}

	telemetry.Debug("Initializing application from config",
		"name", cfg.Meta.Name, "version", cfg.Meta.Version)

	app := &App{
		Name:    cfg.Meta.Name,
		Version: cfg.Meta.Version,
	}

	telemetry.Debug("App instance created",
		"name", app.Name, "version", app.Version)
	return app
}

// Start creates the hybrid server and begins serving gRPC, HTTP, and MCP
// traffic. Non-blocking — call Wait() afterwards to keep the process alive.
// An error means nothing is being served, and the process should exit.
func (a *App) Start() (string, error) {
	telemetry.Debug("Starting application",
		"name", a.Name, "version", a.Version)

	message := "Starting " + a.Name + " version " + a.Version + " (commit " + buildRevision() + ")"

	// A Bazel build records no VCS information, so the startup banner would
	// read "dev": hand it the commit stamped at link time instead.
	if buildCommit != "" && buildCommit != "dev" && buildCommit != "unknown" {
		grpc.BuildRevision = buildCommit
	}

	server, err := server.NewServer(a.Name, a.Version)
	if err != nil {
		telemetry.Error("Failed to create server", "error", err)
		return "", fmt.Errorf("failed to create server: %w", err)
	}
	a.server = server

	if err := a.server.Start(); err != nil {
		telemetry.Error("Failed to start server", "error", err)
		return "", fmt.Errorf("failed to start server: %w", err)
	}

	telemetry.Info("Application started",
		"name", a.Name, "version", a.Version, "commit", buildRevision())
	return message, nil
}

// Stop gracefully drains connections, shuts the server down, and releases
// shared resources (Telemetry logger/tracer).
func (a *App) Stop() error {
	telemetry.Debug("Stopping application", "name", a.Name)

	if a.server != nil {
		if err := a.server.Stop(); err != nil {
			telemetry.Error("Error stopping server", "error", err)
			return fmt.Errorf("failed to stop server: %w", err)
		}
	}

	// Flushing telemetry can fail -- no collector listening, a network gone --
	// without the server having stopped any less cleanly, so it is reported,
	// not returned: a supervisor reading the exit code sees a clean stop.
	if err := telemetry.Close(); err != nil {
		telemetry.Warn("Telemetry was not flushed on shutdown", "error", err)
	}

	telemetry.Info("Application stopped", "name", a.Name)
	return nil
}

// Wait blocks until the server receives SIGINT or SIGTERM. Returns
// immediately if Start() has not been called.
func (a *App) Wait() {
	if a.server != nil {
		telemetry.Telemetry.Logger.Debug("Waiting for shutdown signal")
		a.server.Wait()
		telemetry.Telemetry.Logger.Info("Shutdown signal received")
	}
}
