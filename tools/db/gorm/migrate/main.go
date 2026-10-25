// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrate is a DEV-ONLY helper: it creates the Postgres schemas and
// runs gorm AutoMigrate for every generated model (migrate.Build), so `just
// run` has tables to talk to. It never drops anything, so it is not how
// production changes schema: that is the versioned migrations, applied with
// `just db apply`. It connects with the configured Postgres settings, the same
// source the server reads.
package main

import (
	"log"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/freebusy"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc"
	"github.com/the-protobuf-project/store/migrate"
)

func main() {
	db, err := migrate.Open(config.Get().Database.Postgres.DSN())
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := migrate.Build(db, migrate.Of(freebusy.Default), migrate.Of(rfc.Default)); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Printf("migrated %d freebusy models and %d rfc models", len(freebusy.Default.Models()), len(rfc.Default.Models()))
}
