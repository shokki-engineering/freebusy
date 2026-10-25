# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Code generation: protobuf -> Go, OpenAPI, database layer + cache, docs.
# Invoked as `just gen <recipe>` (e.g. `just gen store`). `just gen` runs `all`.
# Recipes run from the repo root so buf/template paths resolve unchanged.
set working-directory := '../..'

# Regenerate everything: Go, OpenAPI (+merge+envoy), database layer + cache,
# service layer, docs.
[group('codegen')]
all: deps go rfc openapi envoy store service docs

# Update buf module dependencies.
[group('codegen')]
deps:
    buf dep update

# Generate the Go stubs.
[group('codegen')]
go:
    buf generate --template tools/protobuf/buf/go.buf.gen.yaml
    # The generators clear their output directories, BUILD.bazel included.
    bazel run //:gazelle
    go run ./tools/license -fix

# Generate the OpenAPI v3 specs -- freebusy's services and the RFC services it
# serves -- and merge them into openapiv3-spec.yaml.
[group('codegen')]
openapi:
    rm -rf protobuf/generated/openapi/freebusy protobuf/generated/openapi/rfc protobuf/generated/openapi/types
    buf generate --template tools/protobuf/buf/openapiv3.buf.gen.yaml
    buf generate --template tools/protobuf/buf/openapiv3.rfc.buf.gen.yaml
    go run ./tools/protobuf/openapi
    go run ./tools/license -fix

# Generate the Envoy grpc-web proxy config (envoy/launch.yaml) from the OpenAPI
# specs: one route per gRPC service -> the freebusy backend.
[group('codegen')]
envoy:
    go run ./tools/protobuf/envoy
    go run ./tools/license -fix

# Generate the HTTP gateway and MCP bindings for the RFC schema module
# (protobuf-rfc's Resources, Calendars and the rest), whose messages and gRPC
# stubs come from the BSR SDK. Separate from `go` because that template cleans
# its output directory, so the two runs would wipe each other.
[group('codegen')]
rfc:
    buf generate --template tools/protobuf/buf/rfc.buf.gen.yaml
    go run ./tools/protobuf/rfcgw
    # The generators clear their output directories, BUILD.bazel included.
    bazel run //:gazelle
    go run ./tools/license -fix

# Generate the database layer and its cache decorators: prisma + gorm schemas,
# the proto-facing repositories, the typed Hasura client, and the read-through
# cache. Requires protoc-gen-store and protoc-gen-cache on PATH.
#
# The graphql target introspects the live DDN engine, so bring it up first
# (`just hasura regen`) or this recipe fails at that step.
[group('codegen')]
store:
    # buf runs the plugins once per input, so the freebusy run cannot see the
    # RFC module's tables. This image of the RFC module, at the commit
    # buf.lock pins, lets protoc-gen-store discover them (store.backend.yaml
    # `references:`) and turn freebusy's references into foreign keys.
    mkdir -p build/protobuf
    buf build buf.build/the-protobuf-project/rfc:$(awk '/name: buf.build\/the-protobuf-project\/rfc$/ {getline; print $2}' buf.lock) -o build/protobuf/rfc.binpb
    buf generate --template tools/protobuf/buf/store.buf.gen.yaml
    # The generators clear their output directories, BUILD.bazel included.
    bazel run //:gazelle
    go run ./tools/license -fix

# Generate the service layer over the repositories: the AIP standard-method
# servers and their hooks (internal/service/generated), RegisterAll, the change
# feeds and store adapters for runtime-go's events and grpc/idempotency, and
# the error reasons declared in proto -- then copy the reasons' tables into
# docs/errors.md. Requires protoc-gen-service on PATH; run after `store`, whose
# repositories the servers persist through.
[group('codegen')]
service:
    buf generate --template tools/protobuf/buf/service.buf.gen.yaml
    go run ./tools/errors -fix
    # The generator clears its output directory, BUILD.bazel included.
    bazel run //:gazelle
    go run ./tools/license -fix

# Write or update the flow files (docs/flows/*.yaml) after the protos change:
# new methods, lifecycle enums and values are added with the events and error
# reasons protoc-gen-service declares, gone ones are marked STALE, and what was
# written by hand is kept.
[group('codegen')]
flows:
    buf generate --template tools/protobuf/buf/docs.scaffold.buf.gen.yaml
    go run ./tools/license -fix

# Generate the API reference: a README per package beside its protos and the
# index at protobuf/README.md, with ER, flow and state diagrams
# (protoc-gen-openapi-solo, mode=markdown, flows from docs/flows).
[group('codegen')]
docs:
    buf generate --template tools/protobuf/buf/docs.buf.gen.yaml
    go run ./tools/license -fix
