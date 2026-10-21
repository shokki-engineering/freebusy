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

# Server ports (exported so the runtime picks them up). Override on the CLI or
# via the matching FREEBUSY_* env var.
grpc_port := env_var_or_default("FREEBUSY_GRPC_PORT", "50051")
http_port := env_var_or_default("FREEBUSY_HTTP_PORT", "8080")

export FREEBUSY_GRPC_PORT := grpc_port
export FREEBUSY_HTTP_PORT := http_port

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

# Run the unit tests (pure-logic packages; no database required).
[group('test')]
test:
    go test ./...

# Verbose unit tests for the pulse-free pure-logic packages.
[group('test')]
test-unit:
    go test -v ./internal/discount/ ./internal/database/repository/ ./internal/service/gorm/

# Live Hasura/DDN integration tests — needs the local engine (`ddn run docker-start`).
[group('test')]
test-hasura url="http://localhost:3280/graphql":
    FREEBUSY_TEST_GRAPHQL_URL={{ url }} go test ./internal/service/booking/db/hasura/ -run Live -v

# End-to-end server suite over bufconn against live Postgres (run `just migrate` first).
[group('test')]
e2e-gorm dsn="host=localhost port=5432 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable":
    FREEBUSY_TEST_POSTGRES_DSN="{{ dsn }}" go test ./internal/e2e/ -run 'TestE2E_Gorm|TestRepositorySmoke.*Gorm' -count=1 -v

# End-to-end server suite over bufconn against the live DDN engine (`ddn run docker-start`).
[group('test')]
e2e-hasura url="http://localhost:3280/graphql":
    FREEBUSY_TEST_GRAPHQL_URL={{ url }} go test ./internal/e2e/ -run 'TestE2E_Hasura|TestRepositorySmoke.*GraphQL' -count=1 -v

# CI-style gate: build, vet, and test.
[group('test')]
check: build vet test
