// Package main provides the application shell for the freebusy service.
// It wires together configuration, the gRPC/HTTP server, and shared
// resources (logging, telemetry) behind a simple Start / Wait / Stop API.
package main

import (
	"fmt"

	"github.com/oh-tarnished/freebusy/config"
	"github.com/oh-tarnished/freebusy/internal"
	"github.com/oh-tarnished/freebusy/shared"
)

// App is the top-level application instance. It owns the gRPC/HTTP server
// and exposes lifecycle methods for the main goroutine to call.
type App struct {
	// Name is the service name read from configuration.
	Name string
	// Version is the service version read from the VERSION file.
	Version string
	// server is the underlying hybrid gRPC/HTTP/MCP server.
	server *internal.Server
}

// InitializeApp creates an App from the loaded configuration. It validates
// that name and version are present, but does not start the server.
func InitializeApp() *App {
	cfg := config.Get()

	if cfg.Meta.Name == "" || cfg.Meta.Version == "" {
		_ = shared.Telemetry.Logger.Error("Invalid configuration: name or version is empty",
			"name", cfg.Meta.Name,
			"version", cfg.Meta.Version)
		panic("cannot initialize app with empty name or version from config")
	}

	shared.Telemetry.Logger.Debug("Initializing application from config",
		"name", cfg.Meta.Name, "version", cfg.Meta.Version)

	app := &App{
		Name:    cfg.Meta.Name,
		Version: cfg.Meta.Version,
	}

	shared.Telemetry.Logger.Debug("App instance created",
		"name", app.Name, "version", app.Version)
	return app
}

// Start creates the hybrid server and begins serving gRPC, HTTP, and MCP
// traffic. Non-blocking — call Wait() afterwards to keep the process alive.
// An error means nothing is being served, and the process should exit.
func (a *App) Start() (string, error) {
	shared.Telemetry.Logger.Debug("Starting application",
		"name", a.Name, "version", a.Version)

	message := "Starting " + a.Name + " version " + a.Version

	server, err := internal.NewServer(a.Name, a.Version)
	if err != nil {
		_ = shared.Telemetry.Logger.Error("Failed to create server", "error", err)
		return "", fmt.Errorf("failed to create server: %w", err)
	}
	a.server = server

	if err := a.server.Start(); err != nil {
		_ = shared.Telemetry.Logger.Error("Failed to start server", "error", err)
		return "", fmt.Errorf("failed to start server: %w", err)
	}

	shared.Telemetry.Logger.Info("Application started",
		"name", a.Name, "version", a.Version)
	return message, nil
}

// Stop gracefully drains connections, shuts the server down, and releases
// shared resources (Telemetry logger/tracer).
func (a *App) Stop() error {
	shared.Telemetry.Logger.Debug("Stopping application", "name", a.Name)

	if a.server != nil {
		if err := a.server.Stop(); err != nil {
			_ = shared.Telemetry.Logger.Error("Error stopping server", "error", err)
			return fmt.Errorf("failed to stop server: %w", err)
		}
	}

	if err := shared.Close(); err != nil {
		shared.Telemetry.Logger.Warn("Error during shutdown", "error", err)
		return err
	}

	shared.Telemetry.Logger.Info("Application stopped", "name", a.Name)
	return nil
}

// Wait blocks until the server receives SIGINT or SIGTERM. Returns
// immediately if Start() has not been called.
func (a *App) Wait() {
	if a.server != nil {
		shared.Telemetry.Logger.Debug("Waiting for shutdown signal")
		a.server.Wait()
		shared.Telemetry.Logger.Info("Shutdown signal received")
	}
}

// Restart performs a hot restart of the server without tearing down the
// entire process. Useful for applying config changes at runtime.
func (a *App) Restart() error {
	shared.Telemetry.Logger.Debug("Restarting server", "app", a.Name)

	if a.server == nil {
		shared.Telemetry.Logger.Warn("Cannot restart: server not initialized")
		return fmt.Errorf("server not initialized")
	}

	if err := a.server.Restart(); err != nil {
		_ = shared.Telemetry.Logger.Error("Failed to restart server", "error", err)
		return fmt.Errorf("failed to restart server: %w", err)
	}

	shared.Telemetry.Logger.Info("Server restarted", "app", a.Name)
	return nil
}
