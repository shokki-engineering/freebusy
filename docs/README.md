# freebusy

freebusy is a generic availability and booking engine. It knows what can be
booked, when it is free, and who is holding it — and nothing about why. A
padel club, a hotel and a meeting-room system are all the same thing to it:
RFC 9073 resources with calendars, offered by RFC 7953 availability, made busy
by RFC 5545 events and by the holds and reservations freebusy keeps itself.

What it deliberately does not know:

| Concern | Where it lives |
|---|---|
| Prices, discounts, refunds, payment | a payment service, which refers to reservations by name |
| Guests, customers, identity | a user service; a reservation names a `users/{user}` |
| Room types, rate plans, channel rules | the vertical on top (for example an OTA service) |
| Authentication | not built yet (see [Status](#status)) |

Anything else a vertical needs to remember about a booking rides in a
reservation's `attributes`.

---

## Contents

- [Concepts](#concepts)
- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [APIs](#apis)
- [Booking flow](#booking-flow)
- [Webhooks](#webhooks)
- [CalDAV](#caldav)
- [Code generation](#code-generation)
- [Testing](#testing)
- [Continuous integration](#continuous-integration)
- [Building with Bazel and Docker](#building-with-bazel-and-docker)
- [Repository layout](#repository-layout)
- [Troubleshooting](#troubleshooting)
- [Status](#status)

---

## Concepts

```
Organization                         acme
└─ OrganizationalUnit                a site: a club, a hotel, a building
   └─ Resource        (RFC 9073)     the bookable thing: a court, a room, a person
      └─ Calendar     (RFC 5545)     the resource's timeline, with a time zone
         ├─ Availability (RFC 7953)  when it is offered: opening hours
         └─ Event        (RFC 5545)  when it is busy: maintenance, imported bookings

Hold         a short-lived claim on resources for a window, taken by quantity
Reservation  a hold made permanent
```

- **Resources are booked by type and quantity, not by name.** A caller asks for
  "two padel courts at this site, 18:00–19:00"; the engine chooses which.
- **Availability** is the set of time a calendar offers. A calendar with none
  is available at all times (RFC 7953 section 4).
- **Busy time** is a calendar's events (opaque, not cancelled) plus the time
  live holds and reservations claim. Recurring events are expanded with their
  RRULE, EXDATE and RDATE, and edited single occurrences (RECURRENCE-ID) replace
  the instances they name, each time read in its own zone.
- **A hold** takes resources for a window until it expires. The database
  refuses two claims on one resource that overlap (an exclusion constraint),
  so double booking is impossible however many servers race.
- **A reservation** converts a hold so its claim never expires. Cancelling frees
  the resources; rescheduling moves the booking onto a new hold atomically.
- **Booking rules** — minimum and maximum duration, lead time — come from each
  resource's RFC 9073 `STRUCTURED-DATA` profile, so verticals configure them
  without freebusy knowing what a "night" or a "slot" means to them.

---

## Architecture

```
            protobuf/ (the contract)
                 │  buf generate (just gen …)
                 ▼
  generated code ── Go messages, gRPC, REST gateway, MCP tools, OpenAPI,
                    GORM models, GraphQL client, repositories, filters
                 │
                 ▼
  internal/service/*    business logic that connects the generated pieces
                 │
                 ▼
  hybrid server         gRPC + REST (HTTP/1.1, TLS) + MCP + CalDAV
                 │
         ┌───────┴────────┐
         ▼                ▼
   Postgres (GORM)   Hasura DDN (GraphQL) over the same Postgres
```

- **Everything that touches the database is generated** by protoc-gen-store and
  the other plugins of The Protobuf Project. Hand-written code is business
  logic only. If the generated layer cannot express something, the generator is
  extended — see `CLAUDE.md`.
- **Two interchangeable backends.** `[database] provider` picks GORM (Postgres
  directly) or Hasura (the DDN engine over the same Postgres). Every live test
  runs on both.
- **One process, several surfaces.** The same service implementations answer
  gRPC, the REST gateway, MCP tool calls and (in development) CalDAV.
- **Background work** runs in the server: the hold sweeper (expires lapsed
  holds), the reservation reconciler (finishes reservations a crash left
  half-done), webhook delivery, and the database pool monitor.

---

## Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Go | 1.26.5 (`go.mod`) | building and testing |
| Docker | any recent | Postgres, the Hasura engine and connector |
| [just](https://just.systems) | 1.40+ | every task below |
| [buf](https://buf.build) | 1.74+ | code generation, `buf lint` |
| Hasura DDN CLI (`ddn`) | v3.9.4 | the Hasura backend; needs `ddn auth login` |
| mkcert | any | the development TLS certificate |
| api-linter | 2.3.1 | AIP lint (CI runs it too) |
| Bazelisk | any (reads `.bazelversion`, Bazel 9.3) | the Bazel build and the container image |

For code generation you also need the plugins The Protobuf Project ships in its
Homebrew tap:

```sh
brew tap the-protobuf-project/homebrew-tap
brew install --cask protoc-gen-store protoc-gen-mcp protoc-gen-cache \
  protoc-gen-openapi-solo gnostic
```

Homebrew's copies must come first on `PATH`; an older `protoc-gen-store` in
`~/go/bin` silently generates different code. `protoc-gen-store --version`
should match the release the generated headers name.

---

## Getting started

```sh
# 1. Postgres 18, with the freebusydb database the dev config names.
docker compose -f docker/postgres/docker-compose.yaml up -d

# 2. Schemas and tables.
just migrate

# 3. A development TLS certificate: the HTTP port serves HTTPS.
mkcert -install
mkdir -p .certs && (cd .certs && mkcert freebusy-dev localhost 127.0.0.1)

# 4. Run the server (GORM backend by default in config/freebusy.dev.toml).
just run
```

Then (set `enable_http = true` in `config/freebusy.dev.toml` for the REST
calls; the dev profile serves gRPC only by default):

```sh
curl -k https://localhost:8080/health
curl -k https://localhost:8080/v1/webhooks
grpcurl -insecure localhost:50051 list   # TLS with the dev certificate; reflection is on in debug
```

### The Hasura backend

```sh
ddn auth login                 # once; the local build will not run without it
cd hasura                      # then create hasura/.env, below
ddn supergraph build local
ddn run docker-start           # engine on :3280, connector on :9794
```

`hasura/.env` (gitignored) holds the connector's settings; the connection URI
points at `local.hasura.dev`, which the containers resolve to your machine:

```sh
FREEBUSY_FREEBUSY_PGSQL_CONNECTION_URI="postgresql://postgres:postgrespassword@local.hasura.dev:5432/freebusydb"
FREEBUSY_FREEBUSY_PGSQL_HASURA_CONNECTOR_PORT=9794
FREEBUSY_FREEBUSY_PGSQL_READ_URL="http://local.hasura.dev:9794"
FREEBUSY_FREEBUSY_PGSQL_WRITE_URL="http://local.hasura.dev:9794"
FREEBUSY_FREEBUSY_PGSQL_HASURA_SERVICE_TOKEN_SECRET="<a random token>"
FREEBUSY_FREEBUSY_PGSQL_AUTHORIZATION_HEADER="Bearer <the same token>"
FREEBUSY_FREEBUSY_PGSQL_OTEL_EXPORTER_OTLP_ENDPOINT="http://local.hasura.dev:4317"
FREEBUSY_FREEBUSY_PGSQL_OTEL_SERVICE_NAME="freebusy_freebusy_pgsql"
```

Switch the server to it with `provider = "hasura"` under `[database]` in
`config/freebusy.dev.toml`.

---

## Configuration

Every configuration file is compiled into the binary; it runs with nothing
beside it. The layers, later ones winning:

1. **Release defaults** — `config/freebusy.release.toml`, embedded.
   Production-safe; secrets blank.
2. **The development profile** — `config/freebusy.dev.toml`. Read from the
   working directory when it is there (a checkout: edit it, no rebuild), or
   taken from the embedded copy when `FREEBUSY_PROFILE=dev` asks for it.
3. **Environment variables** — `FREEBUSY_<SECTION>__<KEY>`, double
   underscores between levels, override any key:

   ```sh
   FREEBUSY_DATABASE__PROVIDER=hasura
   FREEBUSY_DATABASE__POSTGRES__HOST=db.internal
   FREEBUSY_DATABASE__POSTGRES__PASSWORD=…
   FREEBUSY_SERVER__HTTP__PORT=8443
   FREEBUSY_WEBHOOKS__ALLOW_PRIVATE_NETWORKS=true
   ```

   Always use the double-underscore form: a variable without one reads every
   underscore as a level, so `FREEBUSY_SERVER_ENABLE_HTTP` would mean
   `server.enable.http`.

| Section | What it sets |
|---|---|
| `[app]` | name, version, description |
| `[server]` | environment, which layers run (`enable_http`, `enable_mcp`, `enable_health`) |
| `[server.grpc]` / `[server.http]` / `[server.mcp]` | host and port of each layer; MCP's transport |
| `[server.tls]` | certificate and key; when set, gRPC and HTTP both serve TLS |
| `[database]` | `provider`: `gorm` or `hasura` |
| `[database.postgres]` | host, port, user, password, database, `sslmode`, pool sizes |
| `[database.hasura]` | the engine's GraphQL URL |
| `[cache]` | the backend behind the generated cache decorators |
| `[caldav]` | `enabled` — CalDAV under `/dav/` (development only: unauthenticated) |
| `[webhooks]` | delivery: `enabled`, `allow_insecure_endpoints`, `allow_private_networks`, `interval_seconds`, `max_attempts`, `retention_days` |

Defaults that differ between the release defaults and the dev profile:

| Setting | Release | Development |
|---|---|---|
| `server.environment` | production (no gRPC reflection) | debug |
| `server.enable_http` | true | false |
| `server.tls` | none — plain gRPC and HTTP | `.certs/` mkcert pair |
| `database.postgres` | `127.0.0.1`, `sslmode=require`, no password | the local Docker Postgres |
| `caldav.enabled` | false | true |
| `webhooks.allow_insecure_endpoints` | false | true (`http://localhost` receivers) |
| `webhooks.allow_private_networks` | false | true |

Telemetry is configured separately, from the embedded
`shared/opentelementry.toml`; `FREEBUSY_OTLP_ENDPOINT` (`host:port`) points
export at a collector, and `FREEBUSY_TELEMETRY_ENVIRONMENT`,
`FREEBUSY_PROPERTY` and `FREEBUSY_PROPERTY_NAME` label it.

---

## APIs

| Service | Package | What it does |
|---|---|---|
| AllocationService | `freebusy.allocation.v1` | compute, check, batch and search availability |
| HoldService | `freebusy.allocation.v1` | take, extend, release and delete holds |
| ReservationService | `freebusy.reservation.v1` | confirm holds as reservations; cancel; reschedule |
| WebhookService | `freebusy.webhook.v1` | subscribe endpoints to changes; inspect and retry deliveries |
| Resources, Calendars, Availabilities, Events | `protobuf.rfc*` (upstream) | the inventory, plus `QueryFreeBusy` and `ExpandEvent` |

Every service is reachable four ways:

| Surface | Where (development) |
|---|---|
| gRPC | `localhost:50051`, with reflection |
| REST | `https://localhost:8080/v1/…` (gRPC transcoding; OpenAPI in `protobuf/generated/openapi`) |
| MCP | `http://localhost:8081/<path>` — one port, one path per service, e.g. `/freebusy/webhook/v1/webhookservice/mcp` |
| Health | `https://localhost:8080/health` and the gRPC health service |

With the release defaults (no certificate) the same surfaces are plain:
`grpcurl -plaintext`, `http://…/health`.

Errors follow AIP-193: `INVALID_ARGUMENT` for a bad request (protovalidate runs
on every surface), `FAILED_PRECONDITION` for a state that forbids the call,
`ABORTED` for a stale `etag`.

---

## Booking flow

1. **Publish inventory.** Create a calendar (with its time zone), a resource on
   it with its `resource_types` and `organizational_unit`, and availability for
   its opening hours. Closures and maintenance are events.
2. **Ask.** `SearchAvailability` across sites, `ComputeAvailability` for one
   site's buckets (slots or nights), `CheckAvailability` for one exact window —
   the last answers with reasons when the answer is no.
3. **Hold.** `CreateHold` with a window, a quantity and an `idempotency_key`.
   The hold lives until `expire_time`; `ExtendHold` and `ReleaseHold` change
   that.
4. **Settle elsewhere.** Take payment, collect guest details — in other
   services.
5. **Reserve.** `CreateReservation` from the hold. Retrying is safe: a hold
   backs at most one reservation.
6. **Change.** `CancelReservation` frees the resources; `RescheduleReservation`
   moves the booking onto a new hold the caller took first, so the new slot is
   secured before the old one is let go.

---

## Webhooks

A webhook subscribes an HTTPS endpoint to changes in holds, reservations and
inventory. Each change arrives as a signed [CloudEvent](https://cloudevents.io).

**Event types** are `freebusy.<record>.<what happened>`:

| Record | Types |
|---|---|
| hold | `active`, `released`, `expired`, `converted` |
| reservation | `pending`, `confirmed`, `cancelling`, `cancelled`, `failed`, `rescheduling`, `rescheduled` |
| resource, calendar, event, availability | `created`, `updated`, `deleted` |

A webhook may limit itself to some types, and to one site's holds,
reservations and resources.

**How changes are found.** Changes are read from the records, not announced by
the writes: a dispatcher polls each feed by `update_time` from a stored cursor,
rereading a two-minute lag to catch late commits. A crash cannot lose a change.
Two quick transitions of one record may arrive as one event carrying the later
state; every event carries the whole record.

**Delivery.** `POST` with `Content-Type: application/cloudevents+json`, signed
per [Standard Webhooks](https://www.standardwebhooks.com):

```
webhook-id:        <event id — the same on every retry>
webhook-timestamp: <unix seconds>
webhook-signature: v1,<base64 HMAC-SHA256 of "id.timestamp.body">
```

Verify with any Standard Webhooks library and the secret `CreateWebhook`
returned (`whsec_…`). After `RotateWebhookSecret`, deliveries carry two
signatures, space-separated, for 24 hours — the new key's and the old one's.

Any 2xx accepts. Anything else, a redirect or a timeout is retried after 30
seconds, doubling to at most 6 hours, until `max_attempts` (default 8); then the
delivery is `FAILED` and `RetryDelivery` sends it again. Finished deliveries
are pruned after `retention_days` (default 30).

**Safety.** Endpoints must be `https`. Unless `allow_private_networks` is set,
the sender refuses to connect to loopback, private, link-local and CGNAT
addresses — checked on the address DNS returned, at connect time — and follows
no redirects.

---

## CalDAV

In development (`[caldav] enabled = true`) a CalDAV server (RFC 4791) is
mounted on the HTTP port under `/dav/`, with discovery at
`/.well-known/caldav`. Apple Calendar, Thunderbird and DAVx⁵ can add it as an
account. Calendars, events (including edited single occurrences) and
availability are read and written through the same repositories as the API,
and `free-busy-query` is answered by the engine. It has no authentication,
which is why release configuration leaves it off.

---

## Code generation

```sh
just gen go       # Go messages, gRPC, gateway, MCP (wipes protobuf/generated/go)
just gen rfc      # gateways + MCP for the upstream RFC services — run after `gen go`
just gen openapi  # OpenAPI v3
just gen store    # GORM, repositories, GraphQL client, Prisma, cache decorators
just gen docs     # per-package protobuf READMEs
```

Rules (also in `CLAUDE.md`):

- Never hand-edit generated code or Hasura metadata (`hasura/**/*.hml`).
- After a schema change: `just gen go`, `just gen rfc`, `just gen store`,
  `just migrate`, then `just hasura regen` (which also refreshes the GraphQL
  client).
- `just gen store` deletes `internal/database/hasura/graphqlx`, which is
  hand-written; restore it from git after regenerating.
- No Go import aliases. When two packages share a name, split the file.
- api-linter must pass; the only rule that may be disabled is
  `core::0215::foreign-type-reference`.

The RFC schema module and the generator release are pinned: the RFC module in
`buf.lock` and in `tools/protobuf/buf/{store,rfc}.buf.gen.yaml`, the SDKs and
`runtime-go/rfc` in `go.mod`. Move them together.

---

## Testing

```sh
just test        # unit tests with the race detector; live suites skip
just test-live   # everything, on both backends — needs Postgres and Hasura running
just lint        # gofmt, go mod verify, vet, staticcheck, buf lint
just check       # build + lint + test
just bazel-check # the same build and tests through Bazel
```

The live suites read three variables, which `just test-live` sets:

| Variable | Used by |
|---|---|
| `FREEBUSY_TEST_POSTGRES_DSN` | end-to-end suites (GORM) |
| `FREEBUSY_TEST_DATABASE_URL` | repository and store suites |
| `FREEBUSY_TEST_GRAPHQL_URL` | everything on the Hasura backend |

Unset, a live suite skips. With `FREEBUSY_TEST_REQUIRE_LIVE=1` (set by
`just test-live` and CI) an unset variable is a failure instead, so a run
cannot pass by testing nothing.

`internal/e2e/binary_e2e_test.go` builds the real server, starts it from a
scratch directory with a copy of the dev config, and drives it as a client
would: REST, MCP, and webhooks delivered by the server's own background loop.

The live suites share one database, so `just test-live` runs one package at a
time (`-p 1`): the binary suite's server runs the reconciler, the hold sweeper
and webhook delivery, and in parallel they would act on another package's rows.

---

## Continuous integration

`.github/workflows/ci.yml` runs independent jobs, so one failure never hides
another:

| Job | Runs |
|---|---|
| Format, vet, staticcheck and buf lint | `just lint` |
| Protobuf build and breaking changes | `buf build`; on pull requests `buf breaking` against the base branch |
| Build | `go build ./...` and the server binary |
| Bazel build, tests and image | fails if `bazel run //:gazelle` would change a BUILD file; `bazel build //...`, `bazel test //...`, `bazel build //image` |
| Unit tests | `just test` |
| Live suites | Postgres 18 service, `just migrate`, the Hasura engine and connector, then `just test-live` |

`api-lint.yml` runs api-linter strictly (comment disables ignored).
Dependabot (`.github/dependabot.yml`) groups weekly minor and patch updates,
and `dependabot-auto-merge.yml` merges them once the required checks pass.

Repository setup:

- **`HASURA_DDN_PAT`** — a Hasura personal access token, as an Actions secret
  *and* a Dependabot secret. The DDN CLI will not build the supergraph without
  a login; the live job fails clearly when it is missing.
- **Branch protection on `main`** requiring every job in `ci.yml` and
  `api-lint.yml`. Auto-merge relies on it: without required checks, GitHub
  would merge at once.

---

## Building with Bazel and Docker

`go build` and Bazel build the same thing; Bazel adds reproducible
cross-compilation and the container image.

- **Bzlmod only** (`MODULE.bazel`; Bazel 9 has no WORKSPACE). rules_go and the
  Go SDK version come from `go.mod`; every Go dependency comes from `go.work`
  through Gazelle's `go_deps`, so Bazel resolves exactly what `go build` does.
- **The BUILD files are Gazelle's.** Run `just gazelle` (`bazel run //:gazelle`)
  after adding, moving or importing packages; CI fails when one is stale. The
  only hand-written ones are the root `BUILD.bazel` (Gazelle's directives),
  `protobuf/BUILD.bazel` (marks the generated-code module) and
  `image/BUILD.bazel`.
- **Pure Go** (`.bazelrc`): no cgo anywhere, so every binary is static.

```sh
just bazel-check          # bazel build //... and bazel test //...
bazel build //:freebusy   # the server for this machine
just image                # the image for this machine, into Docker as freebusy:latest
bazel build //image       # the linux/amd64 + linux/arm64 image index
bazel run //image:push -- --tag 1.0.0   # to ghcr.io/oh-tarnished/freebusy
```

### The image

The image has no base layer: its filesystem is one file, `/freebusy`, a
statically linked binary cross-compiled for each platform. No shell, no libc,
no certificate store, no configuration files. What an empty filesystem lacks
the binary carries:

| Needed | Where it comes from |
|---|---|
| configuration | the embedded release defaults and dev profile, plus `FREEBUSY_*` variables |
| time zones | `time/tzdata`, compiled in |
| root certificates for HTTPS webhooks | `golang.org/x/crypto/x509roots/fallback`, used when the system has none |
| a writable directory | none needed: configuration loads from memory, and telemetry falls back to configuring itself in code |

It runs as uid 65532, exposes 50051 (gRPC), 8080 (HTTP) and 8081 (MCP), and
works with a read-only root filesystem:

```sh
docker run --rm --read-only -p 50051:50051 -p 8080:8080 -p 8081:8081 \
  -e FREEBUSY_DATABASE__POSTGRES__HOST=host.docker.internal \
  -e FREEBUSY_DATABASE__POSTGRES__PASSWORD=postgrespassword \
  -e FREEBUSY_DATABASE__POSTGRES__SSLMODE=disable \
  freebusy:latest
```

(`just image-run` does exactly this, on ports 51051, 18080 and 18081.) The
schema must exist first: run `just migrate` against the database, since the
image carries the server only.

To run the development profile in a container, select it and blank its
certificate paths, which point into a checkout's `.certs`:

```sh
docker run --rm --read-only -p 8080:8080 \
  -e FREEBUSY_PROFILE=dev -e FREEBUSY_SERVER__ENABLE_HTTP=true \
  -e FREEBUSY_SERVER__TLS__CERT_FILE= -e FREEBUSY_SERVER__TLS__KEY_FILE= \
  -e FREEBUSY_DATABASE__POSTGRES__HOST=host.docker.internal \
  freebusy:latest
```

---

## Repository layout

```
config/                 configuration: embedded release defaults + dev overlay
docker/                 local Postgres and the grpc-web Envoy proxy
docs/                   this guide
image/                  the container image (Bazel: rules_oci)
hasura/                 Hasura DDN project: metadata, connector, engine
internal/
  caldav/               the CalDAV server
  database/             connection, and the generated GORM / GraphQL / repository layers
  e2e/                  end-to-end suites, the binary suite among them
  livetest/             the live-suite gate
  service/
    allocation/         the engine: availability, holds (engine/ is the arithmetic)
    reservation/        reservations and the reconciler
    rfcsvc/             the upstream RFC services, ExpandEvent, QueryFreeBusy
    webhooksvc/         webhooks: API, dispatcher, sender
protobuf/
  freebusy/             the contract: allocation, reservation, shared, webhook
  generated/            generated code (never edited)
tools/                  just recipes, buf templates, generators' helpers
```

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| `curl http://localhost:8080` answers 400 | the HTTP port serves HTTPS in development; use `https://` (and `-k`, or trust mkcert's CA) |
| An MCP client cannot find a service on port 8082+ | all MCP services share one port; each has its own path |
| Live tests fail intermittently — deliveries already sent, reservations already reconciled | another freebusy server is running against the same database; its background loops act on the tests' rows. Stop it. |
| `ddn supergraph build local` says "Not logged in" | run `ddn auth login` (CI: the `HASURA_DDN_PAT` secret) |
| The Hasura build breaks after a column type changed | run the full `just hasura regen`, not the per-domain form |
| Generated code differs from a colleague's | different plugin versions; check `protoc-gen-store --version` against the generated headers |
| `internal/database/hasura/graphqlx` disappeared | `just gen store` deletes it; restore it from git |
| The server prints "failed to start server" and exits 1 | a port is taken, or the configuration is invalid |
| `go build` fails on a clean machine fetching a module | a dependency is private or missing from the public Go proxy; every dependency must be fetchable by anyone |
| `bazel run //:gazelle` fails with "unknown repo" | run `bazel mod tidy` first: `MODULE.bazel` still names a removed module |
| A dependency's own BUILD files break the Bazel build | add it to the `gazelle_override(build_file_generation = "clean")` list in `MODULE.bazel` |

---

## Status

Ready to build on. Not yet built:

- **Authentication** on every surface (planned: Keycloak / OIDC).
- **Hasura event windowing**: on Hasura, event reads fetch a calendar's whole
  history and filter in code; GORM narrows them in SQL.
- **"This and future" edits** to a recurring series (RANGE=THISANDFUTURE) are
  refused; clients split the series instead.
- **Atomic CalDAV writes** of a series with edited occurrences.
- **Webhook secrets at rest** are stored as written.
- **Client SDKs**: generate them from the BSR module
  (`buf.build/oh-tarnished-registry/freebusy`) — once it is pushed.
