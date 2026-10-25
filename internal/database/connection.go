// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package database owns the database connection plumbing: it opens the configured
// backend and holds the live handles (GORM and/or Hasura), and it selects the
// provider once from the loaded config ([database].provider; GORM by default,
// Hasura opt-in). It is domain-agnostic — each service builds its own
// provider-specific repositories from a Connection (see
// internal/service/<svc>/db), so swapping backends is a configuration change.
package database

import (
	"context"
	"strings"
	"sync"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/the-protobuf-project/runtime-go/cache"
	"gorm.io/gorm"
)

// Provider identifies a database backend implementation.
type Provider string

const (
	// ProviderGorm is the default backend: GORM over the relational database.
	ProviderGorm Provider = "gorm"
	// ProviderHasura is the opt-in backend: Hasura GraphQL.
	ProviderHasura Provider = "hasura"
)

// Connection carries the live backend handles plus the provider that selected
// them. Only the handle for the selected provider needs to be set (PgSQLConn
// for GORM, Hasura for Hasura); Provider tells every consumer — the per-domain
// repository factories — which one to build on, so provider selection travels
// with the connection instead of being re-read from global config.
type Connection struct {
	PgSQLConn *gorm.DB
	Hasura    *freebusyql.Service
	Provider  Provider

	// Caches are the list caches over this connection's repositories; their
	// fields are nil when the cache is off. Changes reports every write a
	// repository built from Repox commits to them, which is what keeps a
	// cached page from outliving a write.
	Caches  Caches
	Changes repox.ChangeObserver
	// References, when set, checks references to resources other services
	// keep, for every repository built from Repox: a write resolves them, a
	// delete asks their referrers, a purge keeps what they still name.
	References *repox.References
	// Projections keeps the projection tables (the availability read model)
	// current with every write a repository or Batch built from Repox makes:
	// inside the write's transaction on GORM, right after it on Hasura.
	Projections *repox.Projections

	// observers hear every write as Changes does, added through Observe.
	observers observerList
	// cacheProvider is the cache UseCaches opened, nil when it is off.
	cacheProvider cache.Provider
}

// observerList is the observers Observe added, safe to read while one is
// being added.
type observerList struct {
	mu   sync.RWMutex
	list []repox.ChangeObserver
}

// Observe makes o hear every write a repository or Batch built from this
// connection's Repox commits -- built before this call or after -- alongside
// Changes. For a service that keeps something derived from records (the
// availability engine's static inputs) and must drop it when one is written.
func (c *Connection) Observe(o repox.ChangeObserver) {
	c.observers.mu.Lock()
	defer c.observers.mu.Unlock()
	c.observers.list = append(c.observers.list, o)
}

// CacheProvider is the cache backend UseCaches opened, or nil when the cache
// is off.
func (c *Connection) CacheProvider() cache.Provider { return c.cacheProvider }

// changes is the observer a Repox handle carries: Changes, then every observer
// Observe added, read at the time of each write.
type changes struct{ c *Connection }

func (ch changes) Changed(ctx context.Context, name string) {
	if ch.c.Changes != nil {
		ch.c.Changes.Changed(ctx, name)
	}
	ch.c.observers.mu.RLock()
	list := ch.c.observers.list
	ch.c.observers.mu.RUnlock()
	for _, o := range list {
		o.Changed(ctx, name)
	}
}

// providerFromConfig resolves the configured provider, defaulting to GORM for an
// empty or unrecognized value. Only Open consults config; everything downstream
// reads Connection.Provider.
func providerFromConfig() Provider {
	switch strings.ToLower(strings.TrimSpace(config.Get().Database.Provider)) {
	case string(ProviderHasura):
		return ProviderHasura
	default:
		return ProviderGorm
	}
}

// Repox returns the handle the generated repositories take, carrying only
// the selected provider's connection: repox picks GraphQL whenever that
// handle is set, so passing both would route a GORM deployment to Hasura.
func (c *Connection) Repox() repox.Conn {
	if c.Provider == ProviderHasura {
		return repox.Conn{GraphQL: c.Hasura, Changes: changes{c}, References: c.References, Projections: c.Projections}
	}
	return repox.Conn{Gorm: c.PgSQLConn, Changes: changes{c}, References: c.References, Projections: c.Projections}
}

// SearchConn is the handle projection searches read through: GORM whenever
// the connection has a Postgres handle, Hasura deployments included -- the
// projection tables live in the same Postgres, and DDN has none of the range
// operators, upserts or GROUP BY a search wants, so over GraphQL a search
// checks ranges on the rows it reads and a count or facet scans at most
// repox.GraphQLScanLimit rows. Writes keep going through Repox. Projections
// rides along so a projection's Apply and Rebuild publish to its feed.
func (c *Connection) SearchConn() repox.Conn {
	if c.PgSQLConn != nil {
		return repox.Conn{Gorm: c.PgSQLConn, Projections: c.Projections}
	}
	return repox.Conn{GraphQL: c.Hasura, Projections: c.Projections}
}
