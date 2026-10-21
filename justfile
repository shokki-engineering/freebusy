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
# Format, vet, staticcheck and buf lint: CI's lint job.
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

# The live suites skip here: they need the variables test-live sets.
# Unit tests with the race detector.
[group('test')]
test:
    go test -race -count=1 ./...

# Needs a running Postgres and Hasura DDN engine (`just migrate`, then
# `ddn run docker-start` in hasura/). A suite that cannot reach its backend
# fails rather than skips.
# Every test, live suites included, against Postgres and Hasura.
[group('test')]
test-live dsn="host=127.0.0.1 port=5432 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" url="http://localhost:3280/graphql":
    FREEBUSY_TEST_REQUIRE_LIVE=1 \
    FREEBUSY_TEST_POSTGRES_DSN="{{ dsn }}" \
    FREEBUSY_TEST_DATABASE_URL="{{ dsn }}" \
    FREEBUSY_TEST_GRAPHQL_URL="{{ url }}" \
    go test -race -count=1 ./...

# Build, lint and unit tests: what CI runs without a database.
[group('test')]
check: build lint test
