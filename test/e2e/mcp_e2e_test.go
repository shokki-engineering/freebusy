// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// This file end-to-end tests the MCP surface: the generated Serve*MCP path
// with the server's unary interceptor chain threaded through — the regression
// test for MCP tool calls bypassing protovalidate. It boots the organisation
// MCP service over streamable HTTP exactly as the hybrid server does (an
// MCPServerConfig carrying UnaryInterceptor) and drives it with the real MCP
// client: an invalid create must come back a validation error, a valid one
// must round-trip, and cancelling the context must stop the listener.
//
// Gated like the other live suites (any provider works; gorm shown):
//
//	FREEBUSY_TEST_POSTGRES_DSN="host=... dbname=freebusydb ..." go test ./internal/e2e/ -run TestMCP -v
package e2e

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/server"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

func TestMCP_ValidationAndLifecycle_Gorm(t *testing.T) {
	db := openTestGorm(t)
	_, svc, err := server.NewGRPCServer(&database.Connection{PgSQLConn: db, Provider: database.ProviderGorm})
	if err != nil {
		t.Fatalf("assemble service: %v", err)
	}

	validate, err := grpc.NewValidationInterceptor()
	if err != nil {
		t.Fatalf("build validation interceptor: %v", err)
	}

	// Reserve a port the way the hybrid server binds one per MCP service.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	cfg := &grpc.MCPServerConfig{
		Name:             "freebusy-e2e",
		Version:          "0.0.0",
		Addr:             addr,
		UnaryInterceptor: validate, // what buildMCPConfigForPort threads through
	}
	go func() { served <- allocationpbv1.ServeAllocationServiceMCP(ctx, svc, cfg) }()

	endpoint := fmt.Sprintf("http://%s%s", addr, allocationpbv1.AllocationServiceMCPDefaultBasePath)
	session := dialMCP(t, endpoint)

	// An invalid call -- no site, no window -- must be rejected by the
	// interceptor before the handler runs. The subject is the MCP surface and
	// the interceptor, not the particular service.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "allocation_service-check_availability_v1",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("invalid call transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("invalid call was accepted: %v", res.Content)
	}
	if text := toolText(res); !strings.Contains(text, "parent") {
		t.Fatalf("rejection does not read as a validation error: %q", text)
	}

	// A valid call round-trips. A site with no resources is a valid question
	// with a definite answer: nothing is free.
	start := time.Now().UTC().AddDate(0, 0, 7).Truncate(time.Hour)
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "allocation_service-check_availability_v1",
		Arguments: map[string]any{
			"parent": fmt.Sprintf("organizationalUnits/mcp-e2e-%d", time.Now().UnixNano()%1_000_000_000),
			"window": map[string]any{
				"start_time": start.Format(time.RFC3339),
				"end_time":   start.Add(time.Hour).Format(time.RFC3339),
			},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("valid call failed: err=%v content=%v", err, res.Content)
	}
	if text := toolText(res); strings.Contains(text, `"bookable": true`) || strings.Contains(text, `"bookable":true`) {
		t.Fatalf("an empty site reported bookable: %s", text)
	}
	_ = session.Close()

	// Context cancellation must stop the listener (the old StartServer blocked
	// in ListenAndServe forever).
	cancel()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeAllocationServiceMCP did not return after context cancellation")
	}
}

// dialMCP connects the real MCP client over streamable HTTP, retrying briefly
// while the server goroutine binds its listener.
func dialMCP(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.0"}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
		if err == nil {
			t.Cleanup(func() { _ = session.Close() })
			return session
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect MCP %s: %v", endpoint, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// toolText flattens a tool result's text content.
func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
