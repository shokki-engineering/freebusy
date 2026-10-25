// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// idempotency.go lives apart from assembly.go, which imports freebusy's
// telemetry package, because the generated repository package it builds on is
// also named shared.

import (
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/shared"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/shared/v1/sharedpbv1svc"
	"github.com/the-protobuf-project/runtime-go/grpc/idempotency"
	"google.golang.org/grpc"
)

// idempotencyInterceptor honours request_id (AIP-155) on every RPC that
// declares it, keeping its keys through the generated repository on conn's
// backend -- GORM or GraphQL alike.
func idempotencyInterceptor(conn *database.Connection) grpc.UnaryServerInterceptor {
	keys := shared.New(repox.Conn{Gorm: conn.PgSQLConn, GraphQL: conn.Hasura}).IdempotencyKeys
	return idempotency.Interceptor(&sharedpbv1svc.IdempotencyStore{Keys: keys}, idempotency.Options{
		Domain: sharedpbv1svc.ErrorDomain,
		// A claim that failed for a reason no key explains is the store's
		// own error, mapped as the repositories map it.
		CreateError: func(err error) error { return repox.MapGormErr(err) },
	})
}
