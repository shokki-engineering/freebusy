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

Then:

```sh
curl -k https://localhost:8080/health
curl -k https://localhost:8080/v1/webhooks
grpcurl -plaintext localhost:50051 list
```

### The Hasura backend

```sh
ddn auth login                 # once; the local build will not run without it
cd hasura
cp .env.example .env 2>/dev/null || true   # then fill in the connector's values (see below)
ddn supergraph build local
ddn run docker-start           # engine on :3280, connector on :9794
```

`hasura/.env` holds the connector's settings; the connection URI points at
`local.hasura.dev`, which the containers resolve to your machine:

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

Configuration is layered:

1. `config/freebusy.release.toml` — the release defaults, **embedded in the
   binary**. A binary with no files beside it runs on these.
2. `config/freebusy.dev.toml` — the development overlay, read from the working
   directory when present.

| Section | What it sets |
|---|---|
| `[app]` | name, version, description |
| `[server]` | environment, which layers run (`enable_http`, `enable_mcp`, `enable_health`) |
| `[server.grpc]` / `[server.http]` / `[server.mcp]` | host and port of each layer; MCP's transport |
| `[server.tls]` | certificate and key for the HTTP layer |
| `[database]` | `provider`: `gorm` or `hasura` |
| `[database.postgres]` | host, port, user, password, database, pool sizes |
| `[database.hasura]` | the engine's GraphQL URL |
| `[cache]` | the backend behind the generated cache decorators |
| `[caldav]` | `enabled` — CalDAV under `/dav/` (development only: unauthenticated) |
| `[webhooks]` | delivery: `enabled`, `allow_insecure_endpoints`, `allow_private_networks`, `interval_seconds`, `max_attempts`, `retention_days` |

The ports can also be set with `FREEBUSY_GRPC_PORT` and `FREEBUSY_HTTP_PORT`.

Defaults that differ between release and development:

| Setting | Release | Development |
|---|---|---|
| `server.enable_http` | true | false (turn on to use REST) |
| `caldav.enabled` | false | true |
| `webhooks.allow_insecure_endpoints` | false | true (`http://localhost` receivers) |
| `webhooks.allow_private_networks` | false | true |

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

---

## Continuous integration

`.github/workflows/ci.yml` runs independent jobs, so one failure never hides
another:

| Job | Runs |
|---|---|
| Format, vet, staticcheck and buf lint | `just lint` |
| Protobuf build and breaking changes | `buf build`; on pull requests `buf breaking` against the base branch |
| Build | `go build ./...` and the server binary |
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

*(Added with the Bazel build — see below.)*

---

## Repository layout

```
config/                 configuration: embedded release defaults + dev overlay
docker/                 local Postgres and the grpc-web Envoy proxy
docs/                   this guide
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
