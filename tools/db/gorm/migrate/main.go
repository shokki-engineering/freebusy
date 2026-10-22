// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrate is a DEV-ONLY helper: it creates the Postgres schemas and runs
// gorm AutoMigrate for every generated freebusy model (all domains), so `just run`
// has tables to talk to. It never drops anything, so it is not how production
// changes schema: that is the versioned migrations in migrations/, applied with
// `just db apply` (see tools/db/migrations). It connects with the Postgres
// settings from the loaded config (config/freebusy.dev.toml overlaying the
// embedded defaults) — the same source the server reads.
//
// It delegates to the generated registries (tools/db/schema), so new entities
// are covered automatically on the next `just gen store`.
package main

import (
	"log"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/tools/db/schema"
)

func main() {
	db, err := schema.Open(config.Get().Database.Postgres.DSN())
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	fb, rfc, err := schema.Build(db)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Printf("migrated %d freebusy models and %d rfc models", fb, rfc)
}
