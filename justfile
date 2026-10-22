# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

default:
    @just --list

# Code generation: Go, OpenAPI, ORM, TypeScript, docs.
[group('codegen')]
mod gen 'tools/just/gen.just'

# Hasura DDN metadata regeneration after a DB schema change.
[group('hasura')]
mod hasura 'tools/just/hasura.just'

# Envoy grpc-web proxy runtime (up/down).
[group('proxy')]
mod envoy 'tools/just/envoy.just'

# Versioned database migrations: diff, check, apply.
[group('db')]
mod db 'tools/just/db.just'

# Exercise the gRPC API via reflection (server must be running).
[group('api')]
mod grpc 'tools/just/grpc.just'


# Run prek hooks (--all-files to force proto lint/generate when no .proto files staged).
[group('lint')]
prek *args:
    # Auto-fix trailing whitespace and EOF before running hooks
    python3 tools/protobuf/fix-precommit.py .
    prek run --all-files {{ args }}

# Application (server) — run, build, test, and exercise the gRPC API.
# Database settings (provider + connection) come from config: the embedded
# config/freebusy.release.toml defaults, overlaid by config/freebusy.dev.toml
# for local development. Switch backends by editing [database].provider there.

# Ports come from config too. Any key can be overridden from the environment,
# FREEBUSY_<SECTION>__<KEY> with double underscores between levels:
#   FREEBUSY_SERVER__GRPC__PORT=50052 just run

# Run the server (DB provider + connection come from config; dev overlay applies).
[group('app')]
run:
    go run .

# DEV ONLY: create the Postgres schemas + AutoMigrate every generated model.
# Run once before `just run`. Uses the same config as the server.
[group('app')]
migrate:
    go run ./tools/db/gorm/migrate

# Dev convenience: migrate the schema, then run the server.
[group('app')]
dev: migrate run

# Compile everything.
[group('build')]
build:
    go build ./...

# Vet everything.
[group('build')]
vet:
    go vet ./...

# Format all Go files.
[group('build')]
fmt:
    gofmt -w .

# Tidy module dependencies.
[group('build')]
gotidy:
    go mod tidy

# Remove the Go build/test caches.
[group('build')]
clean:
    go clean -cache -testcache

# Generated code follows its generator, not gofmt, so only hand-written code
# is format-checked.
# Format, vet, staticcheck, buf lint and the copyright headers: CI's lint job.
[group('lint')]
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(gofmt -l . | grep -v '^protobuf/generated/' || true)
    if [ -n "$unformatted" ]; then echo "gofmt -w these files:"; echo "$unformatted"; exit 1; fi
    go mod verify
    (cd protobuf && go mod verify)
    go vet ./...
    go run honnef.co/go/tools/cmd/staticcheck@2026.2.2 ./...
    buf lint
    go run ./tools/license

# The live suites skip here: they need the variables test-live sets.
# Unit tests with the race detector.
[group('test')]
test:
    go test -race -count=1 ./...

# Needs a running Postgres, Hasura DDN engine and Redis (`just migrate`, then
# `ddn run docker-start` in hasura/; Redis serves the list cache's suite). A
# suite that cannot reach its backend fails rather than skips. One package at a time (-p 1): the suites share the
# database, and the binary suite runs a whole server -- reconciler, sweeper,
# webhook delivery -- that would act on another package's rows.
# Every test, live suites included, against Postgres, Hasura and Redis.
[group('test')]
test-live dsn="host=127.0.0.1 port=5432 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" url="http://localhost:3280/graphql" redis="127.0.0.1:6379":
    FREEBUSY_TEST_REQUIRE_LIVE=1 \
    FREEBUSY_TEST_POSTGRES_DSN="{{ dsn }}" \
    FREEBUSY_TEST_DATABASE_URL="{{ dsn }}" \
    FREEBUSY_TEST_GRAPHQL_URL="{{ url }}" \
    FREEBUSY_TEST_REDIS_ADDR="{{ redis }}" \
    go test -race -count=1 -p 1 ./...

# Bazel: the BUILD files are Gazelle's, generated from the Go sources.
# Regenerate the BUILD.bazel files after adding, moving or importing packages.
[group('bazel')]
gazelle:
    bazel run //:gazelle
    go run ./tools/license -fix

# Everything through Bazel: build, then the tests (live suites skip).
[group('bazel')]
bazel-check:
    bazel build //...
    bazel test //...

# The image -- the static binary and nothing else -- loaded into Docker as freebusy:latest.
[group('bazel')]
image:
    bazel run //image:load

# Run the image against the local Postgres (`just migrate` first), configured
# only by environment variables, with a read-only filesystem.
# The image, run against the local Postgres on http://localhost:18080.
[group('bazel')]
image-run: image
    docker run --rm --read-only -p 51051:50051 -p 18080:8080 -p 18081:8081 \
      -e FREEBUSY_DATABASE__POSTGRES__HOST=host.docker.internal \
      -e FREEBUSY_DATABASE__POSTGRES__PASSWORD=postgrespassword \
      -e FREEBUSY_DATABASE__POSTGRES__SSLMODE=disable \
      freebusy:latest

# Every file's copyright and SPDX header (tools/license); the
# regeneration recipes run it after the generators.
# Write any missing copyright and SPDX headers.
[group('lint')]
license:
    go run ./tools/license -fix

# Build, lint and unit tests: what CI runs without a database.
[group('test')]
check: build lint test
