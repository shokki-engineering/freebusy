// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command migrations keeps migrations/ -- the versioned SQL that changes a
// production database -- in step with the generated models, and applies it.
//
// Nothing in migrations/ is written by hand. The desired schema is what the
// generated registries build (tools/db/schema) on a scratch database, and
// atlas (https://atlasgo.io) diffs it against the directory's history to write
// the next file. Atlas's community build manages neither extensions nor
// functions and triggers, so this tool writes those itself, read off the
// scratch database: a migration ahead of atlas's that creates the extensions
// the schema has started to use, and one after it that (re)creates the
// functions and triggers it has gained or changed -- the no_overlap lock
// triggers among them.
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
	if err := atlas("migrate", "diff", name, "--dir", "file://"+into,
		"--to", pg.URL(desired), "--dev-url", pg.URL(dev)); err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	return routines(db, into, name)
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

// routines writes a migration (re)creating the functions and triggers db has
// that into's history does not -- or has with another definition -- and
// rehashes the directory. A function is CREATE OR REPLACE; a trigger is
// dropped first, so a changed one is replaced.
func routines(db *gorm.DB, into, name string) error {
	var funcs []string
	// Functions of the schema's own, not of an extension.
	if err := db.Raw(`SELECT pg_get_functiondef(p.oid) FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind = 'f'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
		ORDER BY n.nspname, p.proname`).Scan(&funcs).Error; err != nil {
		return err
	}
	type trigger struct{ Name, Table, Def string }
	var triggers []trigger
	if err := db.Raw(`SELECT t.tgname AS name, t.tgrelid::regclass::text AS "table", pg_get_triggerdef(t.oid) AS def
		FROM pg_trigger t WHERE NOT t.tgisinternal ORDER BY 2, 1`).Scan(&triggers).Error; err != nil {
		return err
	}
	history, err := historyOf(into)
	if err != nil {
		return err
	}
	var stmts []string
	for _, f := range funcs {
		if f = strings.TrimSpace(f); !strings.Contains(history, f) {
			stmts = append(stmts, f+";\n")
		}
	}
	for _, t := range triggers {
		if !strings.Contains(history, t.Def) {
			stmts = append(stmts, fmt.Sprintf("DROP TRIGGER IF EXISTS %q ON %s;\n%s;\n", t.Name, t.Table, t.Def))
		}
	}
	if len(stmts) == 0 {
		return nil
	}
	// One second after atlas names its own file, so these follow the tables
	// they are on.
	file := time.Now().UTC().Add(time.Second).Format("20060102150405") + "_" + name + "_routines.sql"
	if err := os.WriteFile(filepath.Join(into, file), []byte(strings.Join(stmts, "\n")), 0o644); err != nil {
		return err
	}
	return atlas("migrate", "hash", "--dir", "file://"+into)
}

// historyOf is every migration in dir, one after another.
func historyOf(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		b.Write(src)
	}
	return b.String(), nil
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
