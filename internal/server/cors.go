// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

// corsOption maps [server.cors] onto runtime-go's CORS policy for the HTTP
// port -- the REST gateway, CalDAV and /health. It is nil when CORS is off.
// The MCP port is a listener of its own and is not covered: MCP clients are
// agents, not browser pages.
func corsOption(c config.CORSConfig) (grpc.Option, error) {
	if !c.Enabled {
		return nil, nil
	}
	policy := corsPolicy(c)
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("config invalid: server.cors: %w", err)
	}
	return grpc.WithCORS(policy), nil
}

// corsPolicy is c as a runtime-go policy; empty lists take runtime-go's
// defaults (the GAPIC browser-client headers among them).
func corsPolicy(c config.CORSConfig) grpc.CORSPolicy {
	return grpc.CORSPolicy{
		AllowedOrigins:   c.AllowedOrigins,
		AllowedMethods:   c.AllowedMethods,
		AllowedHeaders:   c.AllowedHeaders,
		ExposedHeaders:   c.ExposedHeaders,
		AllowCredentials: c.AllowCredentials,
		MaxAge:           c.MaxAge(),
	}
}
