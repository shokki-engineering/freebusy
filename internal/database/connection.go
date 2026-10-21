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
	"strings"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/database/hasura/freebusyql"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
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
		return repox.Conn{GraphQL: c.Hasura, Changes: c.Changes}
	}
	return repox.Conn{Gorm: c.PgSQLConn, Changes: c.Changes}
}
