// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrations keeps migrations/ -- the versioned SQL that changes a
// production database -- in step with the generated models, and applies it.
//
// Nothing in migrations/ is written by hand. The desired schema is what the
// generated registries build (tools/db/schema) on a scratch database, and
// atlas (https://atlasgo.io) diffs it against the directory's history to write
// the next file. Atlas's community build does not manage extensions, so this
// tool writes those itself, read off the scratch database: a migration that
// only creates the extensions the schema has started to use.
//
//	migrations diff <name>   write the next migration, if the models changed
//	migrations check         fail if the models changed without a migration
//	migrations apply [flags] apply pending migrations to the configured database;
//	                         flags pass through to atlas (e.g. --baseline <version>)
//
// It needs the atlas CLI on PATH, and diff and check need a Postgres server
// (the configured one) where it may create and drop two scratch databases.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/config"
	"github.com/shokki-engineering/freebusy/tools/db/schema"
	"gorm.io/gorm"
)

// dir is the migration directory, relative to the repository root.
const dir = "migrations"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "diff":
		if len(os.Args) != 3 {
			usage()
		}
		err = diff(dir, os.Args[2])
	case "check":
		err = check()
	case "apply":
		err = atlas(append([]string{"migrate", "apply", "--dir", "file://" + dir,
			"--url", config.Get().Database.Postgres.URL("")}, os.Args[2:]...)...)
	default:
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: migrations diff <name> | check | apply [atlas flags]")
	os.Exit(2)
}

// diff writes the migrations that bring into's history up to the models.
func diff(into, name string) error {
	pg := config.Get().Database.Postgres
	desired, dev := pg.DBName+"_atlas_desired", pg.DBName+"_atlas_dev"
	admin, err := schema.Open(pg.DSN())
	if err != nil {
		return err
	}
	for _, db := range []string{desired, dev} {
		if err := recreate(admin, db); err != nil {
			return err
		}
		defer func() { _ = admin.Exec(`DROP DATABASE IF EXISTS "` + db + `"`).Error }()
	}

	target := pg
	target.DBName = desired
	db, err := schema.Open(target.DSN())
	if err != nil {
		return err
	}
	if _, _, err := schema.Build(db); err != nil {
		return fmt.Errorf("build the desired schema: %w", err)
	}
	if err := extensions(db, into); err != nil {
		return err
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return atlas("migrate", "diff", name, "--dir", "file://"+into,
		"--to", pg.URL(desired), "--dev-url", pg.URL(dev))
}

// check fails when diff would write anything: a model changed and nobody ran
// `just db diff`.
func check() error {
	tmp, err := os.MkdirTemp("", "migrations-check")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, os.DirFS(dir)); err != nil {
		return err
	}
	before, _ := os.ReadDir(tmp)
	if err := diff(tmp, "check"); err != nil {
		return err
	}
	after, _ := os.ReadDir(tmp)
	if len(after) != len(before) {
		return fmt.Errorf("the generated models changed and %s/ has no migration for it: run `just db diff <name>` and commit the file it writes", dir)
	}
	return nil
}

// recreate drops and creates an empty database.
func recreate(admin *gorm.DB, name string) error {
	if err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `"`).Error; err != nil {
		return err
	}
	return admin.Exec(`CREATE DATABASE "` + name + `"`).Error
}

var createExtension = regexp.MustCompile(`(?i)CREATE EXTENSION IF NOT EXISTS "([^"]+)"`)

// extensions writes a migration creating the extensions db uses that no
// migration in into creates yet, and rehashes the directory.
func extensions(db *gorm.DB, into string) error {
	var used []string
	if err := db.Raw(`SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`).Scan(&used).Error; err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(into, "*.sql"))
	if err != nil {
		return err
	}
	var created []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, m := range createExtension.FindAllStringSubmatch(string(src), -1) {
			created = append(created, m[1])
		}
	}
	var stmts []string
	for _, ext := range used {
		if !slices.Contains(created, ext) {
			stmts = append(stmts, fmt.Sprintf("-- Create extension %q\nCREATE EXTENSION IF NOT EXISTS %q;\n", ext, ext))
		}
	}
	if len(stmts) == 0 {
		return nil
	}
	// One second before atlas names its own file, so the extensions come
	// first in the history.
	name := time.Now().UTC().Add(-time.Second).Format("20060102150405") + "_extensions.sql"
	if err := os.WriteFile(filepath.Join(into, name), []byte(strings.Join(stmts, "")), 0o644); err != nil {
		return err
	}
	return atlas("migrate", "hash", "--dir", "file://"+into)
}

// atlas runs the atlas CLI, its output passed through.
func atlas(args ...string) error {
	if _, err := exec.LookPath("atlas"); err != nil {
		return fmt.Errorf("atlas is not on PATH: install it (brew install atlas, or https://atlasgo.io/docs#installation)")
	}
	cmd := exec.Command("atlas", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
