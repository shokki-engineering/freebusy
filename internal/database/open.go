// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"net/url"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
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

// openGorm dials Postgres with the libpq DSN rendered from config, instruments
// it, and bounds the connection pool. The pool cap is the process's
// backpressure point: excess concurrent queries queue on the pool instead of
// piling onto Postgres.
func openGorm() (*Connection, error) {
	pg := config.Get().Database.Postgres
	db, err := OpenGorm(pg.DSN(), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := Instrument(db); err != nil {
		return nil, err
	}
	if err := BoundPool(db, pg.Pool()); err != nil {
		return nil, err
	}
	return &Connection{PgSQLConn: db, Provider: ProviderGorm}, nil
}

// Instrument traces and measures every query on db through the process's
// telemetry pipeline: a span per statement, nested under the calling RPC's,
// and the ORM's metrics. Without a pipeline it does nothing.
func Instrument(db *gorm.DB) error {
	if telemetry.Telemetry == nil {
		return nil
	}
	if err := freebusy.Default.Instrument(db, telemetry.Telemetry); err != nil {
		return fmt.Errorf("instrument postgres: %w", err)
	}
	return nil
}

// BoundPool applies pool to db's connection pool.
func BoundPool(db *gorm.DB, pool config.PoolSettings) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("postgres pool handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(pool.MaxOpen)
	sqlDB.SetMaxIdleConns(pool.MaxIdle)
	sqlDB.SetConnMaxLifetime(pool.MaxLifetime)
	sqlDB.SetConnMaxIdleTime(pool.MaxIdleTime)
	return nil
}

// openHasura connects to the configured Hasura endpoint (see OpenHasura).
func openHasura() (*Connection, error) {
	h := config.Get().Database.Hasura
	conn, err := OpenHasura(h.URL, h.AdminSecret)
	if err != nil {
		return nil, err
	}
	// Searches read the read model over Postgres when it is configured (see
	// SearchConn); writes stay on Hasura. Without it they run over GraphQL.
	if pg := config.Get().Database.Postgres; pg.Host != "" {
		db, err := OpenGorm(pg.DSN(), &gorm.Config{})
		if err == nil {
			err = BoundPool(db, pg.Pool())
		}
		if err != nil {
			telemetry.Warn("Searches read the read model over Hasura: Postgres could not be opened", "error", err)
			return conn, nil
		}
		conn.PgSQLConn = db
	}
	return conn, nil
}

// OpenHasura connects the typed GraphQL client to the endpoint at rawURL,
// sending adminSecret as the x-hasura-admin-secret header when set. The W3C
// traceparent propagator injects the calling context's active span into every
// request, so Hasura's own ddn-engine spans nest as children of ours in the
// same trace.
func OpenHasura(rawURL, adminSecret string) (*Connection, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse hasura url %q: %w", rawURL, err)
	}
	var headers map[string]string
	if adminSecret != "" {
		headers = map[string]string{"x-hasura-admin-secret": adminSecret}
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
