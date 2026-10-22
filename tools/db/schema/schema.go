// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schema builds freebusy's whole Postgres schema from the generated
// registries: freebusy's own tables and the RFC catalogue it books against,
// with the schemas, extensions and exclusion constraints each registry
// declares. The dev migrate tool runs it on the development database; the
// migrations tool runs it on a scratch database that versioned migrations are
// diffed against.
package schema

import (
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Open connects to dsn the way building the schema needs: foreign keys are not
// created while migrating, so cross-schema references impose no table order.
func Open(dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
}

// Build creates or updates every table on db. It returns how many models each
// registry holds.
//
// The two registries come from different proto modules with separate
// lifecycles, so the generator emits one each -- and each Migrate takes its
// own package's Migrator type, which is why this is written out twice rather
// than looped.
func Build(db *gorm.DB) (freebusyModels, rfcModels int, err error) {
	if err := freebusy.Default.EnsureSchemas(db); err != nil {
		return 0, 0, err
	}
	if err := freebusy.Default.Migrate(db); err != nil {
		return 0, 0, err
	}
	if err := rfc.Default.EnsureSchemas(db); err != nil {
		return 0, 0, err
	}
	if err := rfc.Default.Migrate(db); err != nil {
		return 0, 0, err
	}
	return len(freebusy.Default.Models()), len(rfc.Default.Models()), nil
}
