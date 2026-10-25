// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Loading and validating the embedded + overlay TOML, at startup or on first
// use -- never at import.
package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/grpc/options"
)

// EnvPrefix starts every environment variable that overrides a configuration
// key: FREEBUSY_<SECTION>__<KEY>, double underscores between levels.
const EnvPrefix = "FREEBUSY_"

var (
	// loadedConfig holds the parsed configuration once Load has run.
	loadedConfig *AppConfig
	loadErr      error
	loadOnce     sync.Once
)

// Load reads the configuration -- the embedded release defaults, the
// development profile, then FREEBUSY_ environment variables -- validates it,
// and keeps it for Get. It reads once: later calls return the first result.
// The server calls it at startup; nothing in freebusy reads configuration at
// import.
func Load() (*AppConfig, error) {
	loadOnce.Do(func() { loadedConfig, loadErr = load() })
	return loadedConfig, loadErr
}

func load() (*AppConfig, error) {
	// 1. Embedded production defaults (always present).
	data, err := configFS.ReadFile("freebusy.release.toml")
	if err != nil {
		return nil, fmt.Errorf("embedded config not found: %w", err)
	}
	if err := telemetry.LoadTomlBytes(data); err != nil {
		return nil, fmt.Errorf("failed to load embedded config: %w", err)
	}

	// 2. The development profile: internal/config/freebusy.dev.toml from the working
	// directory when it is there (a checkout, so edits apply without a
	// rebuild), else the embedded copy when FREEBUSY_PROFILE=dev asks for it.
	// FREEBUSY_PROFILE=release loads none, even in a checkout -- how a
	// production migration run from one keeps to the release defaults. Keys
	// the profile defines win; everything else falls through to the release
	// defaults.
	var overlay []byte
	switch profile := os.Getenv(ProfileVar); profile {
	case "release":
	case "", "dev":
		var err error
		if overlay, err = os.ReadFile(devOverlayPath); err == nil || profile == "" {
			break
		}
		if overlay, err = configFS.ReadFile("freebusy.dev.toml"); err != nil {
			return nil, fmt.Errorf("embedded dev profile not found: %w", err)
		}
	default:
		return nil, fmt.Errorf("%s=%q: want dev or release", ProfileVar, profile)
	}
	if overlay != nil {
		if err := telemetry.LoadTomlBytes(overlay); err != nil {
			return nil, fmt.Errorf("failed to load the dev profile: %w", err)
		}
	}

	// 3. Environment variables, last so they win: how a container with nothing
	// but the binary in it is configured. FREEBUSY_DATABASE__POSTGRES__HOST=db.
	if err := telemetry.LoadEnv(EnvPrefix); err != nil {
		return nil, fmt.Errorf("failed to load environment overrides: %w", err)
	}

	cfg := &AppConfig{}
	if err := telemetry.UnmarshalConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if cfg.Meta.Name == "" || cfg.Meta.Version == "" {
		return nil, fmt.Errorf("config invalid: name=%q version=%q", cfg.Meta.Name, cfg.Meta.Version)
	}
	if !cfg.Server.Environment.IsValid() {
		return nil, fmt.Errorf("config invalid: server.environment=%q (want development|debug|staging|production)",
			cfg.Server.Environment)
	}
	if cfg.Server.EnableMCP && !validMCPTransport(cfg.Server.MCP.Transport) {
		return nil, fmt.Errorf("config invalid: server.mcp.transport=%q (want stdio|streamable-http|sse)",
			cfg.Server.MCP.Transport)
	}
	if p := cfg.Server.MCP.Path; p != "" && !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("config invalid: server.mcp.path=%q (want an absolute path such as /mcp)", p)
	}
	if m := cfg.Database.Postgres.PlanCacheMode; m != "" && !slices.Contains(PlanCacheModes, m) {
		return nil, fmt.Errorf("config invalid: database.postgres.plan_cache_mode=%q (want %s)", m, strings.Join(PlanCacheModes, "|"))
	}
	if m := cfg.Search.RebuildOnStart; m != "" && !slices.Contains(OnStartModes, m) {
		return nil, fmt.Errorf("config invalid: search.rebuild_on_start=%q (want %s)", m, strings.Join(OnStartModes, "|"))
	}
	if m := cfg.Search.OpenSearch.ReindexOnStart; m != "" && !slices.Contains(OnStartModes, m) {
		return nil, fmt.Errorf("config invalid: search.opensearch.reindex_on_start=%q (want %s)", m, strings.Join(OnStartModes, "|"))
	}
	if cfg.Server.ShutdownTimeoutSeconds < 0 {
		return nil, fmt.Errorf("config invalid: server.shutdown_timeout_seconds=%d (want 0 for the default, or more)", cfg.Server.ShutdownTimeoutSeconds)
	}
	if cfg.Server.ExperimentalHTTP3 && !cfg.Server.TLS.Enabled() {
		return nil, errors.New("config invalid: server.experimental_http3 requires server.tls.cert_file and server.tls.key_file")
	}
	return cfg, nil
}

// validMCPTransport reports whether t is one of the supported MCP transports.
// MCPTransport carries no validity method of its own, so the check lives here.
func validMCPTransport(t options.MCPTransport) bool {
	switch t {
	case options.MCPTransportStdio, options.MCPTransportStreamableHTTP, options.MCPTransportSSE:
		return true
	default:
		return false
	}
}

// Get returns the configuration, loading it on first use, and panics when it
// is invalid: for the server and its tools, which cannot run without it. The
// server loads it explicitly first (Load), so a bad configuration is reported
// at startup rather than at whichever call reads it first.
func Get() *AppConfig {
	cfg, err := Load()
	if err != nil {
		panic(err.Error())
	}
	return cfg
}
