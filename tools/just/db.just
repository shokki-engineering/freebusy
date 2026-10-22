# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Versioned database migrations (migrations/). Invoked as `just db <recipe>`.
# The files are generated, never written by hand: the generated models build
# the desired schema on a scratch database and atlas diffs it against the
# directory's history (tools/db/migrations). Needs the atlas CLI.
# Recipes run from the repo root so migrations/ resolves unchanged.
set working-directory := '../..'

# List recipes.
[group('db')]
default:
    @just --list db

# Write the next migration after the generated models change (`just gen store`).
[group('db')]
diff name:
    go run ./tools/db/migrations diff {{ name }}
    go run ./tools/license -fix
    atlas migrate hash --dir file://migrations

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
