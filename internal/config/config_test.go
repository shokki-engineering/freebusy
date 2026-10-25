// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
	"time"
)

// TestConfigLoadsReleaseDefaults verifies the embedded release config parses and
// validates. During tests the working directory is the package dir, so the dev
// overlay (internal/config/freebusy.dev.toml, resolved relative to the repo root) is not
// loaded — these assertions reflect the embedded release defaults.
func TestConfigLoadsReleaseDefaults(t *testing.T) {
	cfg := Get()
	if cfg.Meta.Name != "freebusy" {
		t.Errorf("Meta.Name = %q, want %q", cfg.Meta.Name, "freebusy")
	}
	if cfg.Meta.Version == "" {
		t.Error("Meta.Version is empty")
	}
	if cfg.Database.Provider != "gorm" {
		t.Errorf("Database.Provider = %q, want %q", cfg.Database.Provider, "gorm")
	}
	if got := cfg.Database.Postgres.DSN(); !strings.Contains(got, "dbname=freebusydb") {
		t.Errorf("DSN() = %q, want it to contain dbname=freebusydb", got)
	}
	if cfg.Server.Environment != "production" {
		t.Errorf("Server.Environment = %q, want %q", cfg.Server.Environment, "production")
	}
	if cfg.Server.GRPC.Port != 50051 {
		t.Errorf("Server.GRPC.Port = %d, want %d", cfg.Server.GRPC.Port, 50051)
	}
	if !cfg.Server.EnableMCP || cfg.Server.MCP.Transport != "streamable-http" || cfg.Server.MCP.Path != "/mcp" || !cfg.Server.MCP.Stateless {
		t.Errorf("Server.MCP = %+v, want EnableMCP=true transport=streamable-http path=/mcp stateless=true", cfg.Server.MCP)
	}
	// 30 s grace - 5 s preStop - 5 s to flush telemetry: the chart's numbers.
	if got := cfg.Server.ShutdownTimeout(); got != 20*time.Second {
		t.Errorf("Server.ShutdownTimeout() = %v, want 20s", got)
	}
}

// TestPostgresPoolDefaults verifies the embedded release config bounds the pool
// and that Pool() fills defaults for anything left unset.
func TestPostgresPoolDefaults(t *testing.T) {
	pool := Get().Database.Postgres.Pool()
	if pool.MaxOpen != 25 || pool.MaxIdle != 25 {
		t.Errorf("pool bounds = open:%d idle:%d, want 25/25", pool.MaxOpen, pool.MaxIdle)
	}
	if pool.MaxLifetime != 30*time.Minute || pool.MaxIdleTime != 5*time.Minute {
		t.Errorf("pool lifetimes = %v/%v, want 30m/5m", pool.MaxLifetime, pool.MaxIdleTime)
	}

	zero := PostgresConfig{}.Pool()
	if zero.MaxOpen != 25 || zero.MaxIdle != 25 || zero.MaxLifetime != 30*time.Minute || zero.MaxIdleTime != 5*time.Minute {
		t.Errorf("zero-config pool = %+v, want defaults 25/25/30m/5m", zero)
	}
	custom := PostgresConfig{MaxOpenConns: 10}.Pool()
	if custom.MaxOpen != 10 || custom.MaxIdle != 10 {
		t.Errorf("custom pool = open:%d idle:%d, want idle to follow open (10/10)", custom.MaxOpen, custom.MaxIdle)
	}
}

// TestGetGRPCOptions verifies the config maps cleanly onto the runtime options.
func TestGetGRPCOptions(t *testing.T) {
	opts := GetGRPCOptions()
	if opts.ServiceName != "freebusy" {
		t.Errorf("ServiceName = %q, want freebusy", opts.ServiceName)
	}
	if opts.GRPC.Host != "0.0.0.0" || opts.GRPC.Port != 50051 {
		t.Errorf("GRPC = %+v, want host=0.0.0.0 port=50051", opts.GRPC)
	}
	if !opts.EnableHTTP || !opts.EnableHealth || !opts.EnableMCP {
		t.Errorf("enable flags = http:%v health:%v mcp:%v, want all true",
			opts.EnableHTTP, opts.EnableHealth, opts.EnableMCP)
	}
	if string(opts.Environment) != "production" {
		t.Errorf("Environment = %q, want production", opts.Environment)
	}
}

// TestPlanCacheMode verifies every connection forces custom plans by
// default, can be set to another mode, and can leave it to the database.
func TestPlanCacheMode(t *testing.T) {
	if got := Get().Database.Postgres.DSN(); !strings.HasSuffix(got, " plan_cache_mode=force_custom_plan") {
		t.Errorf("release DSN() = %q, want plan_cache_mode=force_custom_plan", got)
	}
	if got := (PostgresConfig{}).DSN(); !strings.Contains(got, "plan_cache_mode=force_custom_plan") {
		t.Errorf("zero-config DSN() = %q, want plan_cache_mode=force_custom_plan", got)
	}
	if got := (PostgresConfig{PlanCacheMode: "auto"}).DSN(); !strings.HasSuffix(got, " plan_cache_mode=auto") {
		t.Errorf("auto DSN() = %q, want plan_cache_mode=auto", got)
	}
	if got := (PostgresConfig{PlanCacheMode: PlanCacheModeDatabase}).DSN(); strings.Contains(got, "plan_cache_mode") {
		t.Errorf("database DSN() = %q, want no plan_cache_mode", got)
	}
}
