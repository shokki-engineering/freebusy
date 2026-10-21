// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"net/url"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/the-protobuf-project/runtime-go/network/runtime"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
)

// Open opens the database backend selected by config ([database].provider) and
// returns a Connection carrying the live handle for that provider. The caller
// passes the Connection to NewFactory.
func Open() (*Connection, error) {
	switch providerFromConfig() {
	case ProviderHasura:
		return openHasura()
	default:
		return openGorm()
	}
}

// openGorm dials Postgres with the libpq DSN rendered from config and bounds
// the connection pool. The pool cap is the process's backpressure point: excess
// concurrent queries queue on the pool instead of piling onto Postgres.
//
// ORM instrumentation is currently OFF, and this is temporary. Instrument wants
// the handle type protoc-gen-store's generated telemetry package aliases, which
// is github.com/the-protobuf-project/telemetry/telemetry-go — while
// runtime-go/grpc still declares NewObserver(*opentelementry.Opentelementry)
// and pins the older opentelementry SDK. shared.Telemetry can only be one of
// them, and runtime-go/grpc is what serves traffic, so it stays on the old SDK
// and this call passes nil.
//
// nil is a documented no-op rather than a crash, so queries simply emit no
// span or metric. Restore it to shared.Telemetry — and delete this note — once
// runtime-go/grpc moves to telemetry-go; the alternative, standing up a second
// telemetry client purely for the ORM, would mean two exporter pipelines in one
// process and was rejected as the worse trade.
func openGorm() (*Connection, error) {
	pg := config.Get().Database.Postgres
	db, err := OpenGorm(pg.DSN(), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := freebusy.Default.Instrument(db, nil); err != nil {
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
