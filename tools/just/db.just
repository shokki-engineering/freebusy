# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Versioned database migrations (deploy/migrations/). Invoked as `just db <recipe>`.
# The files are generated, never written by hand: the store's migrate command
# (tools/db/migrations, github.com/the-protobuf-project/store/migrate) builds
# the generated models' schema on a scratch database and atlas diffs it
# against the directory's history, writing each file with the license header
# and rehashing atlas.sum. Needs the atlas CLI (v1.3.0, the version the
# command insists on). The database is the configured one; STORE_MIGRATE_URL
# overrides it.
# Recipes run from the repo root so deploy/migrations/ resolves unchanged.
set working-directory := '../..'

# List recipes.
[group('db')]
default:
    @just --list db

# Write the next migration after the generated models change (`just gen store`).
[group('db')]
diff name:
    go run ./tools/db/migrations diff {{ name }}

# Write a migration validating the foreign keys added NOT VALID, once the data is clean.
[group('db')]
validate:
    go run ./tools/db/migrations validate

# Fail if the generated models changed without a migration (CI runs this).
[group('db')]
check:
    go run ./tools/db/migrations check

# An existing database that AutoMigrate built takes `--baseline <version>`
# once, after reconciling it (README, "Migrations").
# Apply pending migrations to the configured database.
[group('db')]
apply *flags:
    go run ./tools/db/migrations apply {{ flags }}
