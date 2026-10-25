// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrations keeps deploy/migrations/ -- the versioned SQL that changes a
// production database -- in step with the generated models, and applies it:
// the store's migrate command over freebusy's two registries (its own tables
// and the RFC catalogue it books against). See `just db`.
//
//	migrations diff <name> | validate | check | apply [atlas flags]
//
// The database is the configured one ([database.postgres]); STORE_MIGRATE_URL
// or -url overrides it.
package main

import (
	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc"
	"github.com/the-protobuf-project/store/migrate"
)

func main() {
	migrate.Main(migrate.Config{
		URL:          config.Get().Database.Postgres.URL(""),
		Dir:          "deploy/migrations",
		Header:       "-- Copyright 2026 Srikanth K.\n-- SPDX-License-Identifier: AGPL-3.0-or-later\n\n",
		AtlasVersion: migrate.AtlasVersion,
	}, migrate.Of(freebusy.Default), migrate.Of(rfc.Default))
}
