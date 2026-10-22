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

- Deployment: a Compose stack (`deploy/compose`) for one host, with Postgres,
  migrations, scheduled backups, and the Redis cache, Hasura backend and
  observability stack as profiles; and a Helm chart (`deploy/helm/freebusy`).
- `freebusy-migrations` image: atlas and `migrations/`, which applies them to
  the database it is given. Compose runs it before the server; the Helm chart
  runs it in an init container.
- Alerts (`deploy/observability/alerts.yaml`) on server errors, latency, pool
  saturation, failed webhook deliveries, a failing hold sweeper and a silent
  deployment; runbooks for each in the README.
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

- `FREEBUSY_OTLP_ENDPOINT` with a host name (`telemetry:6009`, a cluster
  Service) exported nothing: TLS was forced on for every host name. It is now
  plaintext unless `FREEBUSY_OTLP_SECURE=true`.
- Expected outcomes -- a lost booking race, a taken name, a missing row -- are
  no longer logged by GORM at ERROR.
- YEARLY rules with BYDAY or BYMONTHDAY and no BYMONTH now span the year.
