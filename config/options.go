// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "github.com/the-protobuf-project/runtime-go/grpc/options"

// GetGRPCOptions builds the hybrid-server options entirely from the loaded
// config, so identity, listeners, feature flags, and environment all come from
// the TOML files (release defaults overlaid by the dev overlay) — no hardcoded
// values here.
func GetGRPCOptions() options.Options {
	cfg := Get()
	srv := cfg.Server
	return options.Options{
		ServiceName:       cfg.Meta.Name,
		Version:           cfg.Meta.Version,
		Description:       cfg.Meta.Description,
		GRPC:              options.GRPCOptions{Host: srv.GRPC.Host, Port: srv.GRPC.Port},
		HTTP:              options.HTTPOptions{Host: srv.HTTP.Host, Port: srv.HTTP.Port},
		MCP:               options.MCPOptions{Host: srv.MCP.Host, Port: srv.MCP.Port, Transport: srv.MCP.Transport},
		EnableHTTP:        srv.EnableHTTP,
		EnableHealth:      srv.EnableHealth,
		EnableMCP:         srv.EnableMCP,
		Environment:       srv.Environment,
		ExperimentalHttp3: srv.ExperimentalHTTP3,
	}
}
