# freebusy

**A generalized availability and booking engine.** freebusy knows what can be
booked, when it is free, and who is holding it — and nothing about why. A padel
club, a meeting-room system, a parking operator, a clinic's appointment book
and an equipment-rental desk are the same problem to it: things with
calendars, open at some times, busy at others, taken for a while and then
either kept or let go.

It is built on calendar standards rather than on any one industry's model:
bookable things are RFC 9073 resources, their timelines RFC 5545 calendars,
their opening hours RFC 7953 availability. It speaks gRPC, REST, MCP (for AI
agents) and CalDAV, stores everything in Postgres — directly or through Hasura
DDN — and makes double booking impossible at the database.

```mermaid
flowchart LR
    Q["Is it free?"] --> H["Hold it"] --> S["Settle elsewhere<br/>(pay, sign, approve)"] --> R["Reserve it"]
    R --> C["Cancel / reschedule"]
    H -. "expires if not reserved" .-> F["Free again"]
```

## Contents

- [What it is for](#what-it-is-for)
- [How it works](#how-it-works)
- [Running in production](#running-in-production)
- [Configuration](#configuration)
- [Using the API](#using-the-api)
- [Webhooks](#webhooks)
- [CalDAV](#caldav)
- [Developing freebusy](#developing-freebusy)
- [Testing and CI](#testing-and-ci)
- [Repository layout](#repository-layout)
- [Troubleshooting](#troubleshooting)
- [Status](#status)
- [Licence](#licence)

## What it is for

Anything booked by time, in any quantity, from any number of sites:

| Use case | Resources | Booked by |
|---|---|---|
| Sports facilities | courts, pitches, lanes | slots |
| Workspaces | meeting rooms, desks, studios | slots |
| Parking | bays, chargers | slots or days |
| Rentals | vehicles, tools, equipment | days |
| Accommodation | rooms, cabins, pitches | nights |
| Appointments | practitioners, chairs, treatment rooms | slots |
| Education | classrooms, labs, instruments | slots or terms |

freebusy deliberately stops at time. Everything that makes a booking belong to
one industry lives elsewhere and refers to freebusy by name:

| Concern | Where it lives |
|---|---|
| Prices, discounts, deposits, refunds | a payment service — it refers to a reservation by name |
| Customers, guests, staff identities | a user service — a reservation names a `users/{user}` |
| Products, rate plans, channel rules | the vertical built on top |
| Anything else a booking must remember | the reservation's free-form `attributes` |

## How it works

### The model

```mermaid
flowchart TD
    ORG["Organization<br/><i>the operator</i>"] --> OU["Organizational unit<br/><i>a site: a club, a car park, a clinic</i>"]
    OU --> RES["Resource · RFC 9073<br/><i>the bookable thing, typed: padel-court, desk, van</i>"]
    RES --> CAL["Calendar · RFC 5545<br/><i>its timeline, in its time zone</i>"]
    CAL --> AV["Availability · RFC 7953<br/><i>when it is offered: opening hours</i>"]
    CAL --> EV["Events · RFC 5545<br/><i>when it is busy: maintenance, imported bookings</i>"]
    OU --> HOLD["Hold<br/><i>a short claim on N resources for a window</i>"]
    HOLD --> RSV["Reservation<br/><i>a hold made permanent</i>"]
```

- **Resources are booked by type and quantity, not by name.** A caller asks for
  "two padel courts at this site, 18:00–19:00"; the engine picks which two,
  preferring the ones that leave the fewest unusable gaps.
- **Free time** is what a resource's availability offers, less its calendar's
  events and less what live holds and reservations claim. Recurring events
  expand with their RRULE, EXDATE and RDATE; an edited single occurrence
  (RECURRENCE-ID) replaces the instance it names; every time is read in its own
  zone, so a weekly 10:00 stays at 10:00 across daylight-saving changes.
- **Booking rules** — minimum and maximum length, how far ahead, how late —
  come from each resource's RFC 9073 `STRUCTURED-DATA`. freebusy never needs to
  know what a "night" or a "session" means to you.
- **No double booking, ever.** A Postgres exclusion constraint refuses two
  overlapping claims on one resource, however many servers race for it.

### The pieces

```mermaid
flowchart LR
    subgraph Clients
        APP["Apps and services"]
        AGT["AI agents"]
        CALC["Calendar apps"]
    end
    subgraph freebusy["freebusy — one binary"]
        GRPC["gRPC :50051"]
        REST["REST :8080"]
        MCP["MCP :8081"]
        DAV["CalDAV /dav/"]
        SVC["Services<br/>allocation · holds · reservations<br/>inventory · webhooks"]
        ENG["Engine<br/>pure functions over time"]
        BG["Background<br/>hold sweeper · reconciler<br/>webhook delivery"]
        REPO["Generated repositories"]
    end
    PG[("Postgres")]
    DDN["Hasura DDN<br/>(optional)"]
    HOOK["Your endpoints"]

    APP --> GRPC & REST
    AGT --> MCP
    CALC --> DAV
    GRPC & REST & MCP & DAV --> SVC
    SVC --> ENG
    SVC --> REPO
    BG --> REPO
    REPO -- "provider = gorm" --> PG
    REPO -- "provider = hasura" --> DDN --> PG
    BG -- "signed CloudEvents" --> HOOK
```

- **One process, four surfaces.** The same service code answers gRPC, the REST
  gateway, MCP tool calls and CalDAV, with the same validation everywhere.
- **The engine is pure.** Availability is computed by functions over plain
  values — no database, no clock of its own — so what `CheckAvailability`
  answers is exactly what `CreateHold` enforces.
- **Every database call is generated** from the protobuf contract (GORM models,
  GraphQL client, repositories, filters). Hand-written code is business logic.
- **Two interchangeable backends.** Postgres directly (GORM) or through Hasura
  DDN; every test runs on both.

### A booking, end to end

```mermaid
sequenceDiagram
    autonumber
    participant C as Your app
    participant F as freebusy
    participant P as Your payment service
    participant W as Your webhook endpoint

    C->>F: SearchAvailability(type, quantity, window)
    F-->>C: sites with enough free
    C->>F: CreateHold(site, window, quantity, idempotency_key)
    F-->>C: Hold ACTIVE, expires in N minutes
    F--)W: freebusy.hold.active
    C->>P: take payment
    P-->>C: paid
    C->>F: CreateReservation(hold)
    F-->>C: Reservation CONFIRMED
    F--)W: freebusy.reservation.confirmed
    Note over C,F: Not reserved in time? The hold expires,<br/>the resources are free again, and<br/>freebusy.hold.expired is sent.
```

### Lifecycles

```mermaid
stateDiagram-v2
    direction LR
    state "Hold" as H {
        [*] --> ACTIVE
        ACTIVE --> RELEASED: ReleaseHold
        ACTIVE --> EXPIRED: deadline passes
        ACTIVE --> CONVERTED: CreateReservation
    }
    state "Reservation" as R {
        [*] --> PENDING
        PENDING --> CONFIRMED: hold converted
        PENDING --> FAILED: hold lapsed
        CONFIRMED --> CANCELLING: CancelReservation
        CANCELLING --> CANCELLED
        CONFIRMED --> RESCHEDULING: RescheduleReservation
        RESCHEDULING --> RESCHEDULED: new hold confirmed
        RESCHEDULING --> CONFIRMED: new hold lapsed
    }
```

`PENDING`, `CANCELLING` and `RESCHEDULING` are written *before* the work they
describe, so a crash never leaves the books wrong: a background reconciler
finds any reservation stuck in one and finishes it.

## Running in production

The production artifact is a container image holding **one file**: a
statically linked `/freebusy` binary, built for `linux/amd64` and
`linux/arm64`. No base image, no shell, no libc, no configuration files — the
binary carries its configuration, the time-zone database and root
certificates, and is configured by environment variables. It runs as an
unprivileged user on a read-only filesystem.

### 1. Build the image

```sh
bazel build //image                         # the two-platform image index
bazel run //image:push -- --tag 1.0.0       # to ghcr.io/shokki-engineering/freebusy
bazel run //image:load                      # or into the local Docker as freebusy:latest
```

### 2. Prepare Postgres

Postgres 18 with a database for freebusy. Create the schema from a checkout of
this repository, pointed at your database with the same environment variables
the server uses:

```sh
FREEBUSY_PROFILE=release \
FREEBUSY_DATABASE__POSTGRES__HOST=db.internal \
FREEBUSY_DATABASE__POSTGRES__USER=freebusy \
FREEBUSY_DATABASE__POSTGRES__PASSWORD=… \
FREEBUSY_DATABASE__POSTGRES__DBNAME=freebusydb \
go run ./tools/db/gorm/migrate
```

`FREEBUSY_PROFILE=release` keeps a checkout's development profile out of it,
so only the release defaults and your variables apply. Migrations are
additive and idempotent; run them before each upgrade.

### 3. Run the server

```sh
docker run -d --name freebusy --read-only \
  -p 50051:50051 -p 8080:8080 -p 8081:8081 \
  -e FREEBUSY_DATABASE__POSTGRES__HOST=db.internal \
  -e FREEBUSY_DATABASE__POSTGRES__USER=freebusy \
  -e FREEBUSY_DATABASE__POSTGRES__PASSWORD=… \
  -e FREEBUSY_DATABASE__POSTGRES__DBNAME=freebusydb \
  ghcr.io/shokki-engineering/freebusy:1.0.0
```

Or with Compose:

```yaml
services:
  freebusy:
    image: ghcr.io/shokki-engineering/freebusy:1.0.0
    read_only: true
    ports: ["50051:50051", "8080:8080", "8081:8081"]
    environment:
      FREEBUSY_DATABASE__POSTGRES__HOST: postgres
      FREEBUSY_DATABASE__POSTGRES__PASSWORD: ${POSTGRES_PASSWORD}
      FREEBUSY_DATABASE__POSTGRES__SSLMODE: disable   # inside a private network only
      FREEBUSY_OTLP_ENDPOINT: otel-collector:4317
    depends_on: [postgres]
  postgres:
    image: postgres:18.4
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: freebusydb
    volumes: ["pgdata:/var/lib/postgresql"]
volumes:
  pgdata: {}
```

### 4. Check it

```sh
curl http://localhost:8080/health
grpc_health_probe -addr=localhost:50051     # the standard gRPC health service
```

### Production checklist

- **Ports**: 50051 gRPC, 8080 HTTP (REST and `/health`), 8081 MCP. Several
  replicas may run at once; everything they share is in Postgres.
- **TLS**: the release defaults serve plain gRPC and HTTP — terminate TLS at
  your load balancer, or mount a certificate and set
  `FREEBUSY_SERVER__TLS__CERT_FILE` and `FREEBUSY_SERVER__TLS__KEY_FILE`.
- **Database TLS**: `sslmode` defaults to `require`.
- **Telemetry**: set `FREEBUSY_OTLP_ENDPOINT` (`host:port`) to export traces,
  metrics and logs; `FREEBUSY_SITE` and `FREEBUSY_SITE_NAME` label them.
- **Webhooks** send only to `https` endpoints on public addresses unless you
  allow otherwise.
- **Authentication is not built yet.** Keep freebusy on a private network,
  reachable only by your own services, until it is (see [Status](#status)).
- **CalDAV** is off by default in production for the same reason.

## Configuration

Every configuration file is compiled into the binary. Settings are layered,
later layers winning:

```mermaid
flowchart LR
    A["Release defaults<br/>config/freebusy.release.toml<br/><i>embedded</i>"] --> B["Development profile<br/>config/freebusy.dev.toml<br/><i>from a checkout, or embedded with<br/>FREEBUSY_PROFILE=dev; none with<br/>FREEBUSY_PROFILE=release</i>"]
    B --> C["Environment<br/>FREEBUSY_SECTION__KEY"]
    C --> D(["running config"])
```

Any key can be set from the environment as `FREEBUSY_<SECTION>__<KEY>`, with
**double underscores** between levels:

```sh
FREEBUSY_DATABASE__PROVIDER=hasura
FREEBUSY_DATABASE__POSTGRES__HOST=db.internal
FREEBUSY_SERVER__HTTP__PORT=8443
FREEBUSY_WEBHOOKS__MAX_ATTEMPTS=12
```

(Always the double-underscore form: a variable without one reads every
underscore as a level, so `FREEBUSY_SERVER_ENABLE_HTTP` would mean
`server.enable.http`.)

| Section | Settings |
|---|---|
| `[server]` | `environment`, `enable_http`, `enable_mcp`, `enable_health` |
| `[server.grpc]`, `[server.http]`, `[server.mcp]` | `host`, `port`; MCP's `transport` |
| `[server.tls]` | `cert_file`, `key_file` — when set, gRPC and HTTP both serve TLS |
| `[database]` | `provider`: `gorm` or `hasura` |
| `[database.postgres]` | `host`, `port`, `user`, `password`, `dbname`, `sslmode`, pool sizes |
| `[database.hasura]` | `url` of the engine's GraphQL endpoint |
| `[caldav]` | `enabled` |
| `[webhooks]` | `enabled`, `allow_insecure_endpoints`, `allow_private_networks`, `interval_seconds`, `max_attempts`, `retention_days` |

Release defaults versus the development profile:

| Setting | Release | Development |
|---|---|---|
| `server.environment` | production (no gRPC reflection) | debug |
| `server.enable_http` | true | false |
| `server.tls` | none | a local mkcert pair in `.certs/` |
| `database.postgres` | `127.0.0.1`, `sslmode=require`, no password | the local Docker Postgres |
| `caldav.enabled` | false | true |
| `webhooks.allow_insecure_endpoints` | false | true |
| `webhooks.allow_private_networks` | false | true |

## Using the API

| Service | What it does |
|---|---|
| `AllocationService` | `ComputeAvailability`, `CheckAvailability`, `BatchComputeAvailability`, `SearchAvailability` |
| `HoldService` | `CreateHold`, `GetHold`, `ListHolds`, `ExtendHold`, `ReleaseHold`, `DeleteHold` |
| `ReservationService` | `CreateReservation`, `GetReservation`, `ListReservations`, `CancelReservation`, `RescheduleReservation` |
| `WebhookService` | webhooks (create, update, pause, resume, rotate secret, delete) and their deliveries (list, get, retry) |
| `Resources`, `Calendars`, `Availabilities`, `Events` | the inventory, plus `QueryFreeBusy` and `ExpandEvent` |

The contract is in `protobuf/` (and published to the Buf Schema Registry as
`buf.build/shokki-engineering/freebusy`); REST paths and JSON follow it by
gRPC transcoding, documented in `protobuf/generated/openapi/`. MCP serves each
service at its own path on one port, e.g.
`/freebusy/allocation/v1/allocationservice/mcp`.

**Publishing inventory.** Create a calendar (with its time zone), a resource
on it (`resource_types`, `organizational_unit`, and its booking rules in
`structured_data`), availability for its opening hours, and events for
closures. All of it can also be written by any calendar app over CalDAV.

**Booking.**

1. **Ask** — `SearchAvailability` across sites, `ComputeAvailability` for one
   site's slots or days, `CheckAvailability` for one exact window (with the
   reasons when the answer is no).
2. **Hold** — `CreateHold` with a window, a quantity and an
   `idempotency_key`. `ExtendHold` and `ReleaseHold` change it.
3. **Settle** — payment, forms, approvals: in your services.
4. **Reserve** — `CreateReservation` from the hold. Safe to retry: a hold
   backs at most one reservation.
5. **Change** — `CancelReservation` frees the resources;
   `RescheduleReservation` moves the booking onto a new hold you took first,
   so the new time is secured before the old one is let go.

Errors follow AIP-193: `INVALID_ARGUMENT` for a bad request,
`FAILED_PRECONDITION` for a state that forbids the call (with a machine
reason), `ABORTED` for a stale `etag`.

## Webhooks

A webhook subscribes an HTTPS endpoint to changes in holds, reservations and
inventory; each change arrives as a signed [CloudEvent](https://cloudevents.io).

| Record | Event types `freebusy.<record>.<…>` |
|---|---|
| hold | `active`, `released`, `expired`, `converted` |
| reservation | `pending`, `confirmed`, `cancelling`, `cancelled`, `failed`, `rescheduling`, `rescheduled` |
| resource, calendar, event, availability | `created`, `updated`, `deleted` |

A webhook may limit itself to some types and to one site.

```mermaid
flowchart LR
    REC[("holds · reservations<br/>inventory")] -- "read by update_time<br/>from a stored cursor" --> DISP["Dispatcher"]
    DISP -- "one delivery per<br/>webhook × change<br/>(id derived: no duplicates)" --> DEL[("deliveries")]
    DEL --> SEND["Sender<br/>claims, signs, POSTs"]
    SEND -- "2xx" --> OK["SUCCEEDED"]
    SEND -- "error · timeout · non-2xx" --> RETRY["retry: 30 s, doubling to 6 h"]
    RETRY --> SEND
    RETRY -- "after max_attempts" --> FAIL["FAILED<br/>RetryDelivery resends"]
```

- **Nothing is lost.** Changes are read from the records themselves, so a
  crash between a write and its announcement cannot drop one. Two quick
  changes to one record may arrive as one event with the later state; every
  event carries the whole record.
- **Signed** per [Standard Webhooks](https://www.standardwebhooks.com):
  `webhook-id`, `webhook-timestamp`, and `webhook-signature: v1,<base64
  HMAC-SHA256 of "id.timestamp.body">` with the `whsec_…` secret
  `CreateWebhook` returns. Any Standard Webhooks library verifies it. After
  `RotateWebhookSecret` the old secret signs too, for 24 hours.
- **Safe by default**: `https` only, no redirects followed, and no
  connections to loopback, private, link-local or CGNAT addresses — checked
  on the address DNS returned — unless `allow_private_networks` is set.
- Finished deliveries are pruned after `retention_days`.

## CalDAV

With `[caldav] enabled = true`, a CalDAV server (RFC 4791) is mounted on the
HTTP port under `/dav/`, with discovery at `/.well-known/caldav`. Apple
Calendar, Thunderbird and DAVx⁵ can add it as an account; calendars, events —
edited single occurrences included — and availability read and write through
the same code as the API, and `free-busy-query` is answered by the engine.
It has no authentication yet, so it is a development surface.

## Developing freebusy

### Tools

| Tool | Version | For |
|---|---|---|
| Go | 1.26.5 (`go.mod`) | building and testing |
| Docker | recent | Postgres, the Hasura engine |
| [just](https://just.systems) | 1.40+ | every task below |
| [buf](https://buf.build) | 1.74+ | code generation, `buf lint` |
| Bazelisk | reads `.bazelversion` (Bazel 9) | the Bazel build and the image |
| mkcert | any | the development TLS certificate |
| Hasura DDN CLI (`ddn`) | v3.9.4 | the Hasura backend (needs `ddn auth login`) |
| api-linter | 2.3.1 | AIP lint |

Code generation also needs The Protobuf Project's plugins, ahead of anything
older on `PATH`:

```sh
brew tap the-protobuf-project/homebrew-tap
brew install --cask protoc-gen-store protoc-gen-mcp protoc-gen-cache protoc-gen-openapi-solo gnostic
```

### First run

```sh
docker compose -f docker/postgres/docker-compose.yaml up -d   # Postgres 18 with freebusydb
just migrate                                                   # the schema
mkcert -install && mkdir -p .certs \
  && (cd .certs && mkcert freebusy-dev localhost 127.0.0.1)    # the dev certificate
just run                                                       # the server, dev profile
```

The dev profile serves gRPC with TLS (`grpcurl -insecure localhost:50051 list`,
reflection on). Set `enable_http = true` in `config/freebusy.dev.toml` for
REST on `https://localhost:8080`; edits there apply on the next `just run`.

### The Hasura backend

```sh
ddn auth login                  # once
cd hasura                       # create hasura/.env — below
ddn supergraph build local
ddn run docker-start            # engine on :3280, connector on :9794
```

`hasura/.env` (not committed):

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

Then `FREEBUSY_DATABASE__PROVIDER=hasura just run`.

### Code generation

```mermaid
flowchart LR
    P["protobuf/<br/>the contract"] -- "just gen go · rfc" --> G["Go messages, gRPC,<br/>REST gateway, MCP"]
    P -- "just gen store" --> S["GORM models, repositories,<br/>GraphQL client, filters"]
    P -- "just gen openapi · docs" --> O["OpenAPI, READMEs"]
    S -- "just migrate" --> DB[("Postgres")]
    DB -- "just hasura regen" --> M["Hasura metadata<br/>+ GraphQL client"]
```

After a contract change: `just gen go`, `just gen rfc`, `just gen store`,
`just migrate`, `just hasura regen`. Rules (also in `CLAUDE.md`):

- Never edit generated code or Hasura metadata (`hasura/**/*.hml`) by hand.
  When the generated layer cannot express something, extend the generator.
- `just gen store` deletes the hand-written
  `internal/database/hasura/graphqlx`; restore it from git afterwards.
- No Go import aliases; when two packages share a name, split the file.
- api-linter must pass; only `core::0215::foreign-type-reference` may be
  disabled.

### Bazel

`go build` and Bazel build the same thing; Bazel adds reproducible
cross-compilation and the image. Gazelle writes every `BUILD.bazel` from the Go
sources — run `just gazelle` after adding or moving packages or imports (and
`bazel mod tidy` first after a `go.mod` change).

```sh
just bazel-check         # bazel build //... && bazel test //...
just image               # the image for this machine, into Docker
just image-run           # and run it against the local Postgres
```

## Testing and CI

```sh
just test         # unit tests, race detector; the live suites skip
just test-live    # everything, on both backends — needs Postgres and Hasura running
just lint         # gofmt, go mod verify, vet, staticcheck, buf lint
just check        # build + lint + test
just bazel-check  # the same through Bazel
```

The live suites read `FREEBUSY_TEST_POSTGRES_DSN`, `FREEBUSY_TEST_DATABASE_URL`
and `FREEBUSY_TEST_GRAPHQL_URL`; `just test-live` sets them, adds
`FREEBUSY_TEST_REQUIRE_LIVE=1` (an unset one fails instead of skipping) and runs
one package at a time, since the suites share the database. One suite builds
the real binary, starts it, and drives it as a client would — REST, MCP, and
webhooks delivered by its own background loop.

CI (`.github/workflows`) runs independent jobs — lint, protobuf build and
breaking changes, build, unit tests, Bazel (BUILD files current, build, test,
image), and the live suites against Postgres and the Hasura engine — plus
strict AIP lint. Dependabot's weekly updates merge themselves when every
check passes. The repository needs:

- a `HASURA_DDN_PAT` secret (Actions and Dependabot): the DDN CLI will not
  build without a login;
- branch protection on `main` requiring every job — without it, auto-merge
  merges at once.

## Repository layout

```
config/                 configuration: release defaults + dev profile, both embedded
docker/                 local Postgres; the grpc-web Envoy proxy
hasura/                 Hasura DDN project: metadata, connector, engine
image/                  the container image (Bazel, rules_oci)
internal/
  caldav/               the CalDAV server
  database/             the connection, and the generated GORM / GraphQL / repository layers
  e2e/                  end-to-end suites, the binary suite among them
  livetest/             the live-suite gate
  service/
    allocation/         availability and holds; engine/ is the pure arithmetic
    reservation/        reservations and the reconciler
    rfcsvc/             the inventory services, ExpandEvent, QueryFreeBusy
    webhooksvc/         webhooks: API, dispatcher, sender
protobuf/
  freebusy/             the contract: allocation, reservation, shared, webhook
  generated/            generated code — never edited
tools/                  just recipes, buf templates, generator helpers
```

## Troubleshooting

| Symptom | Cause |
|---|---|
| `curl http://localhost:8080` answers 400 in development | the dev profile serves HTTPS: use `https://` |
| An MCP client finds no service on port 8082+ | every MCP service shares one port; each has its own path |
| Live tests fail intermittently — deliveries already sent, reservations already reconciled | another freebusy server is running against the same database; its background loops act on the tests' rows |
| `ddn supergraph build local`: "Not logged in" | `ddn auth login` (CI: the `HASURA_DDN_PAT` secret) |
| `go build` fails on a clean machine fetching a module | a dependency is private or missing from the public Go proxy; every dependency must be fetchable by anyone |
| `bazel run //:gazelle`: "unknown repo" | run `bazel mod tidy` first |
| A dependency's own BUILD files break Bazel | add it to the `gazelle_override(build_file_generation = "clean")` list in `MODULE.bazel` |
| `internal/database/hasura/graphqlx` disappeared | `just gen store` deletes it; restore it from git |
| The server prints "failed to start server" and exits 1 | a port is taken, or the configuration is invalid |

## Status

Ready to build on. Not built yet:

- **Authentication and authorization** on every surface (planned: OIDC,
  e.g. Keycloak). Until then, private networks only.
- **Hasura event windowing**: on Hasura, event reads fetch a calendar's whole
  history and filter in code; Postgres narrows them in SQL.
- **"This and future" edits** to a recurring series (RANGE=THISANDFUTURE) are
  refused; calendar apps split the series instead.
- **Atomic CalDAV writes** of a series together with its edited occurrences.
- **Encryption at rest** for webhook secrets.

## Licence

Copyright © 2026 Srikanth K. All rights reserved, except as granted by the
licences below.

Dual-licensed.

- **[AGPL-3.0-or-later](./LICENSE)** — free for open-source work, forever.
- **[Commercial](./LICENSE-COMMERCIAL.md)** — for proprietary or closed-source
  products, and for offering freebusy as a network service without publishing
  your source.

The `.proto` contract and the code generated from it (everything under
[`protobuf/`](./protobuf), and the module on the Buf Schema Registry) are
**[Apache-2.0](./protobuf/LICENSE)**, so a proprietary application that merely
*talks to* a freebusy server needs no commercial licence. The line is drawn at
building on, or modifying, the server itself.

Contributions are accepted under the [Contributor Licence Agreement](./CLA.md):
sign off every commit (`git commit -s`).

---

© 2026 Srikanth K. All rights reserved.
