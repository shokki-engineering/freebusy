// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/database/cache/freebusy/reservationcache"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/shared"
	"github.com/the-protobuf-project/runtime-go/cache"
	"github.com/the-protobuf-project/runtime-go/cache/redis"
)

// Caches are the generated list caches ((cache.v1.cache).list) a Connection's
// repositories are served through. A nil field is a resource read straight
// from the database.
type Caches struct {
	Reservations *reservationcache.ReservationListCache
}

// attachCaches opens the list caches when [cache] is enabled and makes them
// the connection's change observer.
//
// A cache that cannot be opened is logged and left off rather than failing
// startup: every read it would have served is still correct from the
// database, only slower. One consequence is worth knowing when several
// instances share a cache: an instance running without it moves no versions,
// so its writes reach the others' cached pages only when those expire -- a
// minute for reservations.
func (c *Connection) attachCaches(ctx context.Context) {
	p, err := OpenCacheProvider(ctx)
	if err == nil && p != nil {
		err = c.UseCaches(ctx, p)
	}
	if err != nil {
		shared.Telemetry.Logger.Warn("List caches are off: the cache could not be opened", "error", err)
	}
}

// UseCaches opens every list cache on p and observes the connection's writes
// with them.
func (c *Connection) UseCaches(ctx context.Context, p cache.Provider) error {
	db, err := reservationcache.OpenReservationListCache(ctx, p)
	if err != nil {
		return err
	}
	// The list cache reads its pages through Volatile; measured there, its
	// gets are the hit rate (cache_gets_total, strategy volatile).
	db.Volatile = cache.WithVolatileTelemetry(db.Volatile, shared.Telemetry.Meter())
	reservations, err := reservationcache.NewReservationListCache(db, reservationcache.WithListErrors(reportCacheError))
	if err != nil {
		return err
	}
	c.Caches = Caches{Reservations: reservations}
	c.Changes = repox.Changes{reservations}
	return nil
}

// reportCacheError logs a cache failure a list cache absorbed. None fails a
// request, but a version that did not move leaves a site's pages stale for
// their ttl, so each is worth a line.
func reportCacheError(_ context.Context, resource, op string, err error) {
	shared.Telemetry.Logger.Warn("List cache failure", "resource", resource, "op", op, "error", err)
}

// OpenCacheProvider builds the cache.Provider the generated decorators are
// constructed against, or returns (nil, nil) when [config.CacheConfig.Enabled]
// is false.
//
// The backend lives here rather than in the protos on purpose. cache.v1 names
// strategies — ASIDE, INDEXED, DOCUMENT, VOLATILE — and never a driver, because
// which server serves them is deployment policy: the same annotated schema runs
// on Redis in production and on whatever a test brings up. Prefix is config for
// the same reason, and separates one deployment's keys from another's sharing a
// server.
//
// Redis and Dragonfly are the same driver behind RESP, so pointing `address` at
// either works unchanged. memcached is not: it has no server-side sets, and the
// generated constructor for an INDEXED resource refuses at startup rather than
// building something that would miss every lookup it exists to serve.
//
// A nil Provider is a supported result, not an error. Callers treat it as "no
// cache configured" and use the undecorated store, which is always correct —
// just slower.
func OpenCacheProvider(ctx context.Context) (cache.Provider, error) {
	cfg := config.Get().Cache
	if !cfg.Enabled {
		return nil, nil
	}

	client, err := redis.NewClient(ctx, redis.Config{
		Address:  cfg.Address,
		Username: cfg.Username,
		Password: cfg.Password,
		Database: cfg.Database,
		PoolSize: cfg.PoolSize,
	})
	if err != nil {
		return nil, fmt.Errorf("open cache redis %q: %w", cfg.Address, err)
	}

	// DefaultTTL is deliberately left zero: every cached resource carries its own
	// ttl in cache.v1, so a default here would silently apply to any resource
	// whose annotation was forgotten — exactly the entry that then never expires.
	// RequireTTL turns that omission into an error at the first write instead.
	return redis.New(client, cache.Config{
		Prefix:     cfg.Prefix,
		RequireTTL: true,
	}), nil
}
