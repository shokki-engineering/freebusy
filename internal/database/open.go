// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"net/url"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/shared"
	"github.com/the-protobuf-project/runtime-go/network/runtime"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
)

// Open opens the database backend selected by config ([database].provider) and
// returns a Connection carrying the live handle for that provider, with the
// list caches attached when [cache] is enabled. The caller passes the
// Connection to NewFactory.
func Open() (*Connection, error) {
	var (
		conn *Connection
		err  error
	)
	switch providerFromConfig() {
	case ProviderHasura:
		conn, err = openHasura()
	default:
		conn, err = openGorm()
	}
	if err != nil {
		return nil, err
	}
	conn.attachCaches(context.Background())
	return conn, nil
}

// openGorm dials Postgres with the libpq DSN rendered from config and bounds
// the connection pool. The pool cap is the process's backpressure point: excess
// concurrent queries queue on the pool instead of piling onto Postgres.
//
// Every query is instrumented through the process's telemetry: a span per
// statement, nested under the calling RPC's, and the ORM's metrics.
func openGorm() (*Connection, error) {
	pg := config.Get().Database.Postgres
	db, err := OpenGorm(pg.DSN(), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := freebusy.Default.Instrument(db, shared.Telemetry); err != nil {
		return nil, fmt.Errorf("instrument postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("postgres pool handle: %w", err)
	}
	pool := pg.Pool()
	sqlDB.SetMaxOpenConns(pool.MaxOpen)
	sqlDB.SetMaxIdleConns(pool.MaxIdle)
	sqlDB.SetConnMaxLifetime(pool.MaxLifetime)
	sqlDB.SetConnMaxIdleTime(pool.MaxIdleTime)
	return &Connection{PgSQLConn: db, Provider: ProviderGorm}, nil
}

// openHasura connects the typed GraphQL client to the configured endpoint,
// sending the admin secret as the x-hasura-admin-secret header when set. The
// W3C traceparent propagator injects the calling context's active span into
// every request, so Hasura's own ddn-engine spans nest as children of ours in
// the same trace.
func openHasura() (*Connection, error) {
	h := config.Get().Database.Hasura
	u, err := url.Parse(h.URL)
	if err != nil {
		return nil, fmt.Errorf("parse hasura url %q: %w", h.URL, err)
	}
	var headers map[string]string
	if h.AdminSecret != "" {
		headers = map[string]string{"x-hasura-admin-secret": h.AdminSecret}
	}
	svc, err := freebusyql.New(runtime.ConnectionOptions{
		URL:             runtime.URLFromStd(u),
		Headers:         headers,
		TracePropagator: propagation.TraceContext{},
	})
	if err != nil {
		return nil, fmt.Errorf("connect hasura: %w", err)
	}
	return &Connection{Hasura: svc, Provider: ProviderHasura}, nil
}
