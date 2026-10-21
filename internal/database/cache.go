package database

import (
	"context"
	"fmt"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/the-protobuf-project/runtime-go/cache"
	"github.com/the-protobuf-project/runtime-go/cache/redis"
)

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
