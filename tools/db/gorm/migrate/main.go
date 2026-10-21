// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrate is a DEV-ONLY helper: it creates the Postgres schemas and runs
// gorm AutoMigrate for every generated freebusy model (all domains), so `just run`
// has tables to talk to. It is not a production migration tool — use real,
// versioned migrations there. It connects with the Postgres settings from the
// loaded config (config/freebusy.dev.toml overlaying the embedded defaults) —
// the same source the server reads.
//
// It delegates to the generated freebusy.Default registry (protorm emits it with
// every model + EnsureSchemas), so new entities are covered automatically on the
// next `just gen orm`.
package main

import (
	"log"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	dsn := config.Get().Database.Postgres.DSN()

	// FK constraints are disabled during migration so cross-schema references
	// (e.g. promocode.resource -> booking.moneys) don't impose a table-creation
	// ordering.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	// Two registries in one Postgres instance: freebusy's own tables, and the
	// RFC catalogue it books against. They come from different proto modules
	// with separate lifecycles, so the generator emits a registry each — and
	// each Migrate takes its own package's Migrator type, which is why this is
	// written out twice rather than looped.
	if err := freebusy.Default.EnsureSchemas(db); err != nil {
		log.Fatalf("ensure freebusy schemas: %v", err)
	}
	if err := freebusy.Default.Migrate(db); err != nil {
		log.Fatalf("auto-migrate freebusy: %v", err)
	}
	if err := rfc.Default.EnsureSchemas(db); err != nil {
		log.Fatalf("ensure rfc schemas: %v", err)
	}
	if err := rfc.Default.Migrate(db); err != nil {
		log.Fatalf("auto-migrate rfc: %v", err)
	}
	log.Printf("migrated %d freebusy models and %d rfc models",
		len(freebusy.Default.Models()), len(rfc.Default.Models()))
}
