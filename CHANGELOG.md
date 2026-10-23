<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Changelog

Notable changes to freebusy, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/): the API under `protobuf/` and the
`FREEBUSY_*` configuration are what a version promises.

To release: move **Unreleased** under a new `## [x.y.z] - YYYY-MM-DD` heading,
set `[app] version` in `config/freebusy.release.toml` to `vx.y.z`, and push the
tag `vx.y.z`. The release workflow refuses a tag that disagrees with either.

## [Unreleased]

### Added

- Docs: `docs/getting-started.md` (empty server to a confirmed booking with
  curl) and `docs/errors.md`, the error contract: every reason, its code and
  what to do. `tools/errors` (in `just lint`) fails when the code returns a
  reason the page does not list, or the page lists one nothing returns.
- The OpenAPI file covers the whole REST API: the RFC services freebusy serves
  (organizations, sites, resources, calendars, events, availability) beside its
  own. It is linted in CI and attached to each GitHub release.
- MCP tool descriptions for searching, holding and reserving say what to send,
  in what format, and which tool comes next, so an agent can book with nothing
  else to read.
- Deployment: a Compose stack (`deploy/compose`) for one host, with Postgres,
  migrations, scheduled backups, and the Redis cache, Hasura backend and
  observability stack as profiles; and a Helm chart (`deploy/helm/freebusy`).
- `freebusy-migrations` image: atlas and `migrations/`, which applies them to
  the database it is given. Compose runs it before the server; the Helm chart
  runs it in an init container.
- Alerts (`deploy/observability/alerts.yaml`) on server errors, latency, pool
  saturation, failed webhook deliveries, a failing hold sweeper and a silent
  deployment; runbooks for each in the README.
- List-cache hit rate: `cache_gets_total{strategy="volatile",result=hit|miss}`,
  with *List Cache Hit Rate* and *List Cache Gets* on the service dashboard.
- Metrics: `freebusy_hold_sweeps_total`, `freebusy_hold_expired_total`,
  `freebusy_webhook_attempts_total` and `freebusy_webhook_delivery_lag_seconds`,
  with a "Background work" row on the service dashboard.
- `FREEBUSY_OTLP_SECURE` for a collector that serves TLS.
- Release workflow: images, chart and GitHub release from a version tag.
- Versioned migrations (`just db diff`, `just db apply`, `just db check`).
- Recurrence: BYSETPOS, numbered BYDAY, BYHOUR, BYMINUTE and WKST.
- Soft-deleted records are purged after `[retention] deleted_days`.
- Webhook signing secrets are sealed at rest under `[webhooks] secret_key`.

### Fixed

- Concurrent holds on one resource deadlocked under the exclusion
  constraint: a burst of 16 callers racing for a window took up to 86 s on
  Postgres, and on Hasura failed with Internal and could leave a free window
  booked by nobody. A generated trigger now has writers of one resource take
  turns (`pg_advisory_xact_lock` before the insert), and a generated batch
  takes all its locks up front, in order. Contention p99 is now 44 ms on
  Postgres and 115 ms on Hasura, every window won exactly once. Migration
  `no_overlap_locks_routines` adds the trigger.
- A soft delete now sets `expire_time` in the same write (AIP-164), and
  Undelete clears it. The purger used to stamp it afterwards, which re-sent a
  `deleted` webhook for every record it stamped -- thousands at once after a
  bulk delete. Webhooks no longer resend a deletion for a later write to a
  deleted record, either.
- On Hasura, deletes and restores never reached webhooks: they did not move a
  record's `update_time`, which the change feed reads by.
- Webhook delivery rereads the last 30 seconds of changes each round, not the
  last 2 minutes, so a burst of changes delays deliveries less.
- A change made just after a webhook was created could be lost to it: a
  delivery round read the webhooks first and the changes after, and a change
  newer than its list of webhooks was recorded for nobody. A round now takes
  only the changes up to its read of the webhooks; the next round takes the
  rest. Slow rounds (Hasura) lost changes this way often.
- `SearchAvailability` across an organization, with no sites named, found
  nothing: it matched sites by the organization's id while sites store its
  full name.
- The OpenAPI file was not valid OpenAPI 3.0 (`info.summary`), listed hosted
  servers that do not exist, and declared a bearer-token scheme nothing checks.
- `FREEBUSY_OTLP_ENDPOINT` with a host name (`telemetry:6009`, a cluster
  Service) exported nothing: TLS was forced on for every host name. It is now
  plaintext unless `FREEBUSY_OTLP_SECURE=true`.
- Expected outcomes -- a lost booking race, a taken name, a missing row -- are
  no longer logged by GORM at ERROR.
- YEARLY rules with BYDAY or BYMONTHDAY and no BYMONTH now span the year.
