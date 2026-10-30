<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# freebusy

## Go imports

- Never alias an import. Refer to a package by the name it declares:
  `resourcev1.Resource` from
  `buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1`,
  `eventv1grpc.EventsServer` from its `.../event/v1/eventv1grpc` package.
- When two imports in one file declare the same package name, split the code so
  each file needs only one of them. Don't rename either import.
- Generated code follows its generator. Keep `go_package` overrides in
  `tools/protobuf/buf/*.buf.gen.yaml` on each package's real name, so that no
  aliases appear there either.

## Generated code

- Everything that touches the database is generated: GORM models, stores,
  converters, filters, the repositories over GORM and GraphQL, and the
  freebusyql GraphQL client. All of it comes from protoc-gen-store and the other
  plugins of The Protobuf Project (protoc-gen-cache, protoc-gen-mcp, ...), run
  through buf. Hand-written code is business logic that connects the generated
  pieces, and nothing else.
- If the generated layer can't express something a service needs, extend the
  plugin (~/Projects/personal/orm for protoc-gen-store) and regenerate. Don't
  write the query, the converter or the adapter by hand.
- Never edit Hasura metadata (`deploy/hasura/**/*.hml`) by hand. Regenerate it with
  `just hasura regen`, and do so only when a schema change requires it.

## API rules

- Run api-linter on proto changes. The only rule that may be disabled is
  `core::0215::foreign-type-reference`; fix every other finding.

## Shared Postgres and Hasura

- freebusy and aperio share one local Postgres and one Hasura DDN engine.
  Postgres is aperio's dev server (127.0.0.1:5433), with a database per app:
  `freebusydb` and `aperio`.
- aperio owns the supergraph and runs the engine (127.0.0.1:3280). freebusy
  owns its subgraph (`deploy/hasura/freebusy`: metadata, connector) and its
  `ddn` regeneration, and publishes it as the `freebusy-subgraph` image,
  which aperio mounts through a Docker volume. Nothing here runs an engine
  or refers to a path in aperio's repository.
- Each subgraph prefixes its GraphQL names (`graphqlRootFieldPrefix` and
  `graphqlTypeNamePrefix` in its `subgraph.yaml`): `freebusy_` here,
  `aperio_` there. The two schemas share names. The prefixes go into
  `tools/protobuf/buf/config/store.graphql.yaml` (`root_field_prefix`,
  `type_name_prefix`), so freebusyql keeps its Go names and speaks the
  prefixed ones.
- Never start a second engine or Postgres for one app.

## Repository

- Don't commit or stage. The owner reviews the working tree and commits.
