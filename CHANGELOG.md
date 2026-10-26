<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Changelog

Notable changes to freebusy, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/): the API under `protobuf/` and the
`FREEBUSY_*` configuration are what a version promises.

To release: move **Unreleased** under a new `## [x.y.z] - YYYY-MM-DD` heading,
set `[app] version` in `config/freebusy.release.toml` to `vx.y.z`, and push the
tag `vx.y.z`. The release workflow refuses a tag that disagrees with either. A
pre-release (`x.y.z-rc.n`) works the same way; it is published as a GitHub
pre-release and does not move the images' `latest` tag.

## [Unreleased]

## [1.0.0-rc.2] - 2026-10-26

### Changed

- **Breaking:** freebusy is a top-level booking engine with no tenants or
  sites. Holds, allocations and reservations are top-level collections
  (`holds/{hold}`, `allocations/{allocation}`, `reservations/{reservation}`;
  `POST /v1/holds`, `POST /v1/reservations`), and `CreateHold`, `ListHolds`,
  `CreateReservation` and `ListReservations` take no `parent`.
  `ComputeAvailability`, `CheckAvailability` and `SearchAvailability` are
  `POST /v1/resources:computeAvailability`, `:checkAvailability` and
  `:searchAvailability`, narrowed by `resources`, `resource_types` and
  `filter` instead of a site or an organization. Grouping resources into
  venues, properties or tenants belongs to whatever is built on top.
- Holds taken with one idempotency key are the same hold everywhere, not per
  site.

### Removed

- The `Organizations` and `OrganizationalUnits` services, the check that
  refused calls naming a missing or deleted site, and the error reasons that
  went with it (`ORGANIZATION_*`, `SITE_*`, `HOLD_AT_ANOTHER_SITE`).
- `SearchAvailabilityRequest.parent`, `organizational_units` and `quantity`
  (a match needed that many free at one site), and
  `AvailabilityMatch.organizational_unit`.
- `Webhook.organizational_unit`: a webhook is sent every change of the types
  it asks for.
- RFC 4519 from the database layer: organizations, organizational units,
  LDAP groups and memberships have no tables, repositories, Hasura models or
  bindings (`exclude_paths` on the RFC module). Migration
  `20261026071039_drop_rfc4519_directory` drops their schemas.
- `organizational_unit` everywhere it was left: the read model's column, and
  with it `filter`, `order_by` and `facets` on it. The RFC module's
  `Resource` no longer has the field either.
- The read model's `organization` column. Migration
  `20261026062717_top_level_engine` drops it and the site columns of holds,
  allocations, reservations and webhooks;
  `20261026070000_top_level_engine_names` renames existing holds, allocations
  and reservations (and the names rows keep of them) to the top-level form,
  refusing to run if two sites used one id.

### Added

- CORS for the HTTP port, so browser clients (the TypeScript SDK in fallback
  mode) can call the REST gateway from other origins: `[server.cors]` with
  `enabled` (default false), `allowed_origins` (exact origins or `*`),
  `allowed_methods`, `allowed_headers` (defaulting to what a GAPIC browser
  client sends), `exposed_headers`, `allow_credentials` (refused with `*`) and
  `max_age_seconds`; `FREEBUSY_SERVER__CORS__*` take comma-separated lists.
  Preflights are answered before any handler runs, and every response varies
  on `Origin`. Served by runtime-go's `grpc.WithCORS`; the MCP port is not
  covered. Helm `cors.*` values and Compose `FREEBUSY_CORS_*` variables wire
  it. See "Calling freebusy from a browser" in the README.
- A release pushes the API's buf module to the BSR, labelled with the tag
  (secret `BUF_TOKEN`; skipped until it exists). The SDKs are regenerated
  from that label by hand.

## [1.0.0-rc.1] - 2026-10-24

### Added

- The service layer is generated (protoc-gen-service 0.1.0, `just gen
  service`, into `internal/service/generated`): the RFC directory and
  inventory services (Organizations, OrganizationalUnits, Resources,
  Calendars, Events, Availabilities) and WebhookService run on generated
  AIP-131..135/164 servers, with freebusy's rules as hooks (a site belongs to
  a live organization, nothing is deleted out from under what it holds, a
  webhook's endpoint and secret) and the custom methods (QueryFreeBusy,
  ExpandEvent, BatchGetEvents, the THISANDFUTURE split, Pause/Resume/Rotate,
  RetryDelivery) beside them. Every service is registered on gRPC, REST and
  MCP through the generated `RegisterAll`. HoldService and
  ReservationService stay hand-written (`(service.v1.service) = {skip:
  true}`): every method there runs the engine or the reservation state
  machine.
- Error reasons are declared in proto: an `ErrorReason` enum per package
  (`freebusy.dev`, `(service.v1.error_domain)`/`(service.v1.error_reason)`)
  with the same reason strings as before. The generated translator renders
  every service's errors, and docs/errors.md's tables are the generated
  fragments (`go run ./tools/errors -fix`; `just lint` fails on a stale
  copy). `CANCELLED` and `DEADLINE_EXCEEDED` are now documented.
- Change-feed events declared in proto: `(service.v1.event)` on Hold and
  Reservation and `(service.v1.state_event)` on every state, with the
  event types unchanged (`freebusy.hold.*`, `freebusy.reservation.*`,
  `freebusy.reservation.updated`); `(service.v1.adapter)` on Webhook,
  Delivery, FeedCursor and IdempotencyKey.
- API docs with diagrams (protoc-gen-openapi-solo 1.2.0): flow files under
  `docs/flows` (book, reschedule, cancel, hold expiry, webhook delivery; the
  hold, reservation, webhook and delivery lifecycles; events and errors
  filled in from the service.v1 options; `just gen flows`), the per-package
  READMEs and index rendered from them (`just gen docs`), and the OpenAPI
  spec carrying the tags' descriptions and every ER, flow and state diagram
  under `x-mermaid`.

- Search stage 2, the availability read model: `allocation.resource_days`
  (`freebusy.allocation.v1.ResourceDay`), one row per resource per UTC day
  of a rolling horizon with its standing free time (offered less events and
  converted holds, as `tstzmultirange` under GiST), site, organization,
  `resource_types`, `display_name` and `attributes` (its STRUCTURED-DATA JSON,
  merged). Generated by protoc-gen-store's projection support and written in
  the same transaction as every write to a resource, site, calendar,
  availability, event or allocation (on Hasura: a Batch's own request; a
  single write's follow-up request). `SearchAvailability`,
  `CheckAvailability` and `CreateHold` read their candidates from it and the
  engine evaluates only those; checks and holds fall back to the whole scope
  when candidates fall short, so they never depend on it being current.
  Every B-tree index on it ends in `id`, and a page index `(organization,
  day, organizational_unit, resource, id)` serves `SearchAvailability`'s
  default order with an ordered index scan. Measured in process on 2,000
  resources: organization-wide search with sparse availability (98% booked)
  171 ms to 8 ms; where everything is free it costs a few ms more (README,
  Search), and a page of 201 in the default order takes 2.4 ms. Migrations
  `resource_day_projection` and `store_v162_indexes`; the server backfills
  it as it starts when it is empty (`[search] rebuild_on_start`).
- `filter` (AIP-160 over `attributes.<path>`, `resource_types`,
  `display_name`, `organizational_unit`; terms ANDed by default) on
  `SearchAvailability`, `ComputeAvailability` and `CheckAvailability`, and
  `Hold.candidate_filter` on `CreateHold` (stored with the hold).
  `SearchAvailability` also takes `order_by` (`resource`,
  `organizational_unit`, `display_name`, `free_fraction`, and
  `attributes.max_occupancy`, sorted as a number through a `json_keys`
  declaration on `ResourceDay.attributes`) and `facets`, returned as
  `SearchAvailabilityResponse.facets`; `AvailabilityMatch.display_name` is
  new. New reasons `INVALID_FILTER`, `INVALID_ORDER_BY`, `INVALID_FACET`,
  `INVALID_PAGE_TOKEN`, `WINDOW_BEYOND_HORIZON`, and `FILTER_UNSUPPORTED` /
  `ORDER_BY_UNSUPPORTED` for what the search backend cannot evaluate (on a
  Hasura-only deployment, `resource_types:` in a filter -- DDN has no array
  comparison; the request's `resource_types` field still works -- and
  ordering by an attribute path). `SearchAvailability` page tokens changed
  format: a token from before the read model is refused with
  `INVALID_PAGE_TOKEN`.
- `[search]`: `horizon_days` (365; extended daily by the server),
  `provider` (`postgres` or `opensearch`) and `rebuild_on_start` (`empty`,
  the default: rebuild the read model in the background when it has no rows;
  `always`: on every start, the repair after writes around the API; `never`).
  `[search.opensearch]` `url`, `username`, `password`, `index_queue`,
  `timeout_ms` and `reindex_on_start` (`empty`, the default: reload the index
  when it has no documents but the table has rows; `always`; `never`)
  (`FREEBUSY_SEARCH__*`). With `opensearch` the server ensures
  the generated index `freebusy_resource_days`, feeds it every committed
  read-model change, and serves `SearchAvailability` from it with a fallback
  to Postgres on errors and timeouts (`freebusy.search.fallbacks{reason}`);
  a reindex at startup reloads it behind an alias. Compose profile
  `search` (OpenSearch 3.9.0, single node, security off for local use);
  Helm `search.provider`, `search.opensearch.url` and Secret keys
  `opensearch-username` / `opensearch-password`.
- "This and future" edits of a recurring series: a CalDAV PUT carrying
  `RECURRENCE-ID;RANGE=THISANDFUTURE`, or `CreateEvent`/`UpdateEvent` with
  `original_start_range`, splits the series at that instance in one write.
  The original ends before it (`COUNT` cut, else `UNTIL` set to its last
  instance before the split); the rest becomes a new series carrying the
  edit, shifted by the edit's delta, with UID `<uid>_R<instance in UTC>` and
  the original UID in `REFIDS`. Later overrides move with it; a cancelled
  edit ends the series there. A split PUT answers without an ETag (RFC 4791
  section 5.3.4). New reasons `NOT_AN_INSTANCE`, `SERIES_NOT_FOUND`,
  `RANGE_NOT_SUPPORTED` (a rule the shift would change the meaning of). On
  Hasura DDN the split is not atomic: DDN runs each root field of a mutation
  in a transaction of its own. Migration `original_start_range`
  (`event.resource.original_start_range`).
- MCP: one endpoint, `http://localhost:8081/mcp`, serves every service's
  tools as one MCP server, so an agent connects once (`[server.mcp] path`,
  `FREEBUSY_SERVER__MCP__PATH`). Each service still answers at its own path
  on the same port; those paths will be retired in a later release. MCP is
  stateless by default (`[server.mcp] stateless`, `true`;
  `FREEBUSY_SERVER__MCP__STATELESS`): streamable HTTP without sessions, so
  any replica answers any request without sticky routing. It gives up
  server-to-client requests (elicitation, sampling, roots) and the
  standalone GET stream and DELETE (405); set `false` for sessions. Tool
  descriptions for searching, holding and reserving say what to send, in
  what format, and which tool comes next, so an agent can book with nothing
  else to read.
- Admission control for the availability engine: `[engine]
  max_evaluations` (0, the default, is the number of CPUs) bounds the
  `SearchAvailability`, `ComputeAvailability`, `BatchComputeAvailability`,
  `CheckAvailability` and `CreateHold` evaluations running at once; one that
  finds no turn within `[engine] admission_wait_ms` (250) is refused with
  `RESOURCE_EXHAUSTED` / `ENGINE_BUSY` and counted in
  `freebusy_engine_rejected_total{method}`. `FREEBUSY_ENGINE__MAX_EVALUATIONS`,
  `FREEBUSY_ENGINE__ADMISSION_WAIT_MS`.
- `OUTSIDE_AVAILABILITY` (`FAILED_PRECONDITION`): `CreateHold` for a window no
  resource asked about is offered in -- outside opening hours -- says so
  instead of `CAPACITY_EXHAUSTED`, which stays for a window that is offered
  but taken.
- `ReservationService.UpdateReservation` (`PATCH /v1/{reservation.name}`,
  AIP-134): changes a confirmed reservation's `customer` or `attributes`.
  `update_mask` is required and may name only those two; `reservation.etag`
  guards it (`ABORTED` when stale). Cancelled, failed or moved reservations
  are refused (`RESERVATION_NOT_CONFIRMED`, `RESERVATION_MOVED`). Webhooks
  get a new event type, `freebusy.reservation.updated`, and agents a new MCP
  tool, `reservation_service-update_reservation_v1`. A reservation's new
  output-only `last_change` (`CHANGE_STATE`, `CHANGE_DETAILS`) says what its
  last write changed; `freebusy.reservation.updated` is sent exactly when it
  is `CHANGE_DETAILS`. Migration `reservation_last_change`.
- Extending a booking in place: `Hold.replaces` names the hold of the
  reservation a new hold is taken to replace. That booking's own time does not
  count against the new hold, so a window overlapping it keeps the same
  resources; `RescheduleReservation` hands the shared time over in the write
  that ends the old hold. Such a hold backs only that move
  (`HOLD_FOR_RESCHEDULE`); `replaces` must name a live booking
  (`REPLACED_HOLD_NOT_BOOKED`). Migration `hold_replaces`.
- Foreign keys between freebusy's tables and the RFC tables, generated from
  the protos' resource references (store discovers the RFC module's tables
  from an image `just gen store` builds at the pinned commit). Migration
  `fk_constraints` adds them `NOT VALID`; `validate_foreign_keys` then
  validates the seven external keys, and fails on a database still holding
  rows that break one: clean those first. `just db validate` writes such a
  migration for every key the models add `NOT VALID`. Violations are
  `FAILED_PRECONDITION` with `FOREIGN_KEY_VIOLATION`, `REFERENCE_NOT_FOUND`
  or `RESOURCE_REFERENCED` (domain `store.the-protobuf-project.org`).
- References to other services: `[[reference_services]]` registers the service keeping a type freebusy names
  but does not store, such as a reservation's `customer` (`users/{user}`,
  `on_delete: ALLOW`): writes check the name with its AIP Get, and
  `store.v1.ReferenceService` tells that service which records name one of its
  resources.
- `freebusy.purge.kept{kind}`: soft-deleted records the purger keeps because
  something still refers to them. The purger uses the generated
  `PurgeExpired`.
- Docs: `docs/getting-started.md` (empty server to a confirmed booking with
  curl) and `docs/errors.md`, the error contract: every reason, its code and
  what to do. `tools/errors` (in `just lint`) fails when the code returns a
  reason the page does not list, or the page lists one nothing returns. The
  README's Performance section has load-test numbers from a single laptop
  (kind, two replicas, in-cluster k6).
- The OpenAPI file covers the whole REST API: the RFC services freebusy serves
  (organizations, sites, resources, calendars, events, availability) beside its
  own. It is linted in CI and attached to each GitHub release.
- Deployment: a Compose stack (`deploy/compose`) for one host, with Postgres,
  migrations, scheduled backups, and the Redis cache, Hasura backend and
  observability stack as profiles; and a Helm chart (`deploy/helm/freebusy`)
  with `helm test` (a Pod that checks `/health` and a read API through the
  Service; `tests.enabled`, `tests.image`). The `freebusy-migrations` image
  (atlas and `migrations/`) applies the migrations to the database it is
  given: Compose runs it before the server, the chart in an init container.
  CI job `kind` runs the chart on a kind cluster over throwaway Postgres and
  Redis (`deploy/kind/`), `helm test`, and a rolling restart under traffic
  that must fail no call (`deploy/kind/rollout-check.sh`).
- Alerts (`deploy/observability/alerts.yaml`) on server errors, latency, pool
  saturation, failed webhook deliveries, a failing hold sweeper and a silent
  deployment; runbooks for each in the README.
- List-cache hit rate: `cache_gets_total{strategy="volatile",result=hit|miss}`,
  with *List Cache Hit Rate* and *List Cache Gets* on the service dashboard.
- Metrics: `freebusy_hold_sweeps_total`, `freebusy_hold_expired_total`,
  `freebusy_webhook_attempts_total` and `freebusy_webhook_delivery_lag_seconds`,
  with a "Background work" row on the service dashboard.
- Telemetry carries `service.instance.id` (`FREEBUSY_TELEMETRY_INSTANCE_ID`,
  else the host name) and `host.name`, and honours `OTEL_RESOURCE_ATTRIBUTES`;
  the Helm chart sets the instance id to the pod's name and adds
  `k8s.pod.name`, `k8s.namespace.name`, `k8s.node.name` and
  `k8s.deployment.name` from the downward API, so replicas' metrics do not
  overwrite each other. `FREEBUSY_OTLP_SECURE` for a collector that serves
  TLS.
- `freebusy --version`, and the commit on the startup line and banner.
  Release images are stamped with the tag and commit (`bazel ...
  --config=release`, `tools/bazel/status.sh`); unstamped builds say `dev`.
- Release workflow: images, chart and GitHub release from a version tag.
- Versioned migrations (`just db diff`, `just db apply`, `just db check`).
- Recurrence: BYSETPOS, numbered BYDAY, BYHOUR, BYMINUTE and WKST.
- Soft-deleted records are purged after `[retention] deleted_days`.
- Webhook signing secrets are sealed at rest under `[webhooks] secret_key`.

### Changed

- Webhook delivery is runtime-go's `events` dispatcher over the generated
  feeds and `EventStore` (metrics `freebusy.webhook.*`, cursors, delivery
  names, signatures, sealing and the lifecycle rules unchanged, cursors
  resumed where they stopped); errors are runtime-go's `grpc/problem`
  through the generated translator; error hints are `grpc/hints`;
  `request_id` is `grpc/idempotency` over the generated `IdempotencyStore`;
  CalDAV is runtime-go's `rfc/caldav` over a store adapter (same `/dav` tree
  and `/.well-known/caldav`). The hand-written equivalents are gone.
- Versioned migrations are written and applied by the store's `migrate`
  library (`tools/db/migrations` is now three lines over it; atlas v1.3.0
  enforced; the license header written by the library). `tools/db/schema`
  is gone.
- Repository layout: `migrations/` is `deploy/migrations/`, `image/`
  `deploy/image/` (Bazel `//deploy/image:load`, `:push`,
  `:load_migrations`), `docker/` `deploy/docker/`, `.grafana/`
  `deploy/grafana/`, `hasura/` `deploy/hasura/`; `config/` is
  `internal/config/` (the dev overlay is read from
  `internal/config/freebusy.dev.toml`), `shared/` `internal/telemetry/`, the
  server, assembly and site check `internal/server/`, `rfcsvc`
  `internal/service/directory`, `webhooksvc` `internal/service/webhook`, the
  error translator and paging `internal/service/rpc`; the e2e and live-test
  gate are `test/e2e` and `test/live`. The binary stays `//:freebusy`; `licenses/` is gone (its Apache-2.0 text is
  `protobuf/LICENSE`), and the CLA is `.github/CLA.md`.
- Pins: protoc-gen-store and the store module 1.6.5, protoc-gen-service
  0.1.0 (BSR `buf.build/the-protobuf-project/service`), protoc-gen-mcp
  1.7.5, protoc-gen-openapi-solo 1.2.0, runtime-go at main (26d67bc, `events`
  added).
- `just gen docs` renders with protoc-gen-openapi-solo; `tools/protobuf/docs`
  (protodoc) is gone. The OpenAPI type documents are per package, not per
  file, so the four `enums.proto` no longer overwrite each other.

- Dependencies: the RFC module is buf.build/the-protobuf-project/rfc commit
  `cc82b3b1` (`Event.original_start_range`), with its Go SDK and
  runtime-go/rfc to match; every runtime-go module is at main (`e4dfbe2`);
  generated code is protoc-gen-store 1.6.3 (store module v1.6.3) and
  protoc-gen-mcp 1.7.5, with MCP annotations from
  `github.com/the-protobuf-project/mcp` (`mcp/v1/annotations.proto`,
  `(mcp.v1.service)`, `(mcp.v1.tool)`, the renamed grpc-mcp-gateway; tool and
  server names are unchanged). Store 1.6.3 selects only the `ReadMask`
  columns in a projection search and writes the range test as
  `coalesce(free @> r, false)` when an equality on an index-leading column is
  present, so the planner reads a page index instead of sorting every match.
  An OpenSearch search no longer opens a point in time: pages follow by
  `search_after` alone, so `search.max_open_pit_context` no longer limits it.
- Shutdown: the HTTP gateway drains before gRPC stops, both within
  `[server] shutdown_timeout_seconds` (default 20). The Helm chart sets it to
  `terminationGracePeriodSeconds` less `preStopSleepSeconds` less 5 s.
- Availability is much cheaper to evaluate. The engine's static inputs --
  resources, sites, calendars and availability, converted -- are cached in
  process per site, under a version that moves on every write to them (the
  list caches' change signal, shared through the cache when `[cache]` is on);
  holds, claims and events are still read on every call, and a hold checks the
  static inputs of what it takes against the database before writing.
  `SearchAvailability` evaluates sites in page order and stops once the page
  is full; batched reads are split by key and read side by side; a
  `resource_types` filter is evaluated in SQL on Postgres. Measured on 10,001
  resources: an organization-wide search 1.9 s to 9 ms, a 1,000-resource
  `ComputeAvailability` 176 ms to 5 ms, `CreateHold` there 725 ms to 44 ms
  under load, server memory under search 2.2 GiB to 250 MiB. On Hasura the
  type filter is still applied after loading (no array containment in the
  GraphQL schema).
- A CalDAV `PUT` of a recurring series and its overrides, and a `DELETE` of an
  object or a calendar with everything in it, is one write: a failure part way
  leaves the object as it was, and a stale etag fails the whole write with
  `412`. (On Hasura DDN each root field of the write still runs in a
  transaction of its own.)
- Webhook delivery reads each change feed as far as it needs to tell what
  happened -- name, etag, the times, a hold's or reservation's state -- and
  reads whole records only for the changes some webhook is sent.
- Nothing is configured at import any more: the server loads its
  configuration (`config.Load`) and builds its telemetry pipeline
  (`shared.Init`) explicitly at startup, in the order package initialisation
  used to. The server's behaviour is unchanged; the migration tools no longer
  start a telemetry pipeline they did not use.
- The gRPC server and the e2e harness share one interceptor
  chain (`internal/assembly`); the e2e harness now runs `request_id`
  idempotency too, as the server always did.

### Fixed

- Log lines given key/value pairs kept only the first key: the telemetry
  logger takes one data value, so "Read model not updated after a write"
  logged `data=projection` without the projection or the error. The pairs
  are now passed as one map.
- A `filter` using `OR` or `NOT` matched nothing, and parentheses or a
  leading `-` failed as an unknown field: the generated AIP-160 parser read
  only ANDed terms, taking `OR` and `NOT` for free-text words searched in
  `display_name`. The parser regenerated from protoc-gen-store v1.6.3 reads
  `AND`, `OR`, `NOT`, `-` and parentheses on every search backend.
- After five runs on one pooled connection, Postgres switched the filtered,
  faceted search to a generic plan: 25 ms became 115 ms (on 10,001
  resources; 211 ms under load). Every connection the server opens now sets
  `plan_cache_mode = force_custom_plan` as a startup parameter, configurable
  as `[database.postgres] plan_cache_mode` (`auto`, `force_generic_plan`, or
  `database` to leave it to `ALTER DATABASE`/`ROLE`, e.g. behind PgBouncer).
- A `SearchAvailability` that fell back from OpenSearch to Postgres was
  counted but never logged. It is now logged at warn level with
  OpenSearch's error, at most once every 10 s, with the number of fallbacks
  in between.
- Concurrent holds on one resource deadlocked under the exclusion
  constraint: a burst of 16 callers racing for a window took up to 86 s on
  Postgres, and on Hasura failed with Internal and could leave a free window
  booked by nobody. A generated trigger now has writers of one resource take
  turns (`pg_advisory_xact_lock` before the insert), and a generated batch
  takes all its locks up front, in order. Contention p99 is now 44 ms on
  Postgres and 115 ms on Hasura, every window won exactly once. Migration
  `no_overlap_locks_routines` adds the trigger.
- Concurrent `CreateHold` calls for the same window no longer pile onto one
  resource: best fit still decides, but among equally good resources each
  caller draws at random. 60 drivers for 50 identical bays now get 50 holds
  and 10 `CAPACITY_EXHAUSTED`; before, 22 were answered `ABORTED` with 12
  bays still free.
- `SearchAvailability` across an organization, with no sites named, found
  nothing: it matched sites by the organization's id while sites store its
  full name.
- Availability published with a start and no end offered every hour before
  that start; time before it that no other availability covers is no longer
  offered.
- YEARLY rules with BYDAY or BYMONTHDAY and no BYMONTH now span the year.
- `freebusy.reservation.updated` was inferred from timestamps: an
  `UpdateReservation` within a second of the confirmation was sent as
  `confirmed`, and a reschedule that fell through as `updated`. It now follows
  `last_change`.
- A soft delete now sets `expire_time` in the same write (AIP-164), and
  Undelete clears it. The purger used to stamp it afterwards, which re-sent a
  `deleted` webhook for every record it stamped -- thousands at once after a
  bulk delete. Webhooks no longer resend a deletion for a later write to a
  deleted record, either.
- On Hasura, deletes and restores never reached webhooks: they did not move a
  record's `update_time`, which the change feed reads by.
- A change made just after a webhook was created could be lost to it: a
  delivery round read the webhooks first and the changes after, and a change
  newer than its list of webhooks was recorded for nobody. A round now takes
  only the changes up to its read of the webhooks; the next round takes the
  rest. Slow rounds (Hasura) lost changes this way often. Delivery also
  rereads the last 30 seconds of changes each round, not the last 2 minutes,
  so a burst of changes delays deliveries less.
- A release build (`FREEBUSY_PROFILE=release`, or no development profile in
  the working directory) reported its telemetry environment, banner and log
  lines as "development"; it now reports "production" unless
  `FREEBUSY_TELEMETRY_ENVIRONMENT` or `FREEBUSY_SERVER__ENVIRONMENT` says
  otherwise.
- `FREEBUSY_OTLP_ENDPOINT` with a host name (`telemetry:6009`, a cluster
  Service) exported nothing: TLS was forced on for every host name. It is now
  plaintext unless `FREEBUSY_OTLP_SECURE=true`.
- Expected outcomes -- a lost booking race, a taken name, a missing row -- are
  no longer logged by GORM at ERROR.
- The OpenAPI file was not valid OpenAPI 3.0 (`info.summary`), listed hosted
  servers that do not exist, and declared a bearer-token scheme nothing checks.
- `just hasura regen` no longer generates models, commands and a freebusyql
  package (`atlasql`) for atlas's `atlas_schema_revisions` schema: the
  connector's introspection excludes it.
