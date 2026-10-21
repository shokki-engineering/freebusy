# Code generation: protobuf -> Go, OpenAPI, database layer + cache, docs.
# Invoked as `just gen <recipe>` (e.g. `just gen store`). `just gen` runs `all`.
# Recipes run from the repo root so buf/template paths resolve unchanged.
set working-directory := '../..'

# Regenerate everything: Go, OpenAPI (+merge+envoy), database layer + cache, docs.
[group('codegen')]
all: deps go rfc openapi envoy store docs

# Update buf module dependencies.
[group('codegen')]
deps:
    buf dep update

# Generate the Go stubs.
[group('codegen')]
go:
    buf generate --template tools/protobuf/buf/go.buf.gen.yaml

# Generate the OpenAPI v3 specs and merge them into openapiv3-spec.yaml.
[group('codegen')]
openapi:
    buf generate --template tools/protobuf/buf/openapiv3.buf.gen.yaml
    go run ./tools/protobuf/openapi

# Generate the Envoy grpc-web proxy config (envoy/launch.yaml) from the OpenAPI
# specs: one route per gRPC service -> the freebusy backend.
[group('codegen')]
envoy:
    go run ./tools/protobuf/envoy

# Generate the HTTP gateway and MCP bindings for the RFC schema module
# (protobuf-rfc's Resources, Calendars and the rest), whose messages and gRPC
# stubs come from the BSR SDK. Separate from `go` because that template cleans
# its output directory, so the two runs would wipe each other.
[group('codegen')]
rfc:
    buf generate --template tools/protobuf/buf/rfc.buf.gen.yaml
    go run ./tools/protobuf/rfcgw

# Generate the database layer and its cache decorators: prisma + gorm schemas,
# the proto-facing repositories, the typed Hasura client, and the read-through
# cache. Requires protoc-gen-store and protoc-gen-cache on PATH.
#
# The graphql target introspects the live DDN engine, so bring it up first
# (`just hasura regen`) or this recipe fails at that step.
[group('codegen')]
store:
    buf generate --template tools/protobuf/buf/store.buf.gen.yaml

# Generate protobuf docs (per-module + root README.md) via protodoc.
[group('codegen')]
docs:
    go run ./tools/protobuf/docs protobuf
