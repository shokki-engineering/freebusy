# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Hasura DDN metadata regeneration. Invoked as `just hasura <recipe>`.
# Recipes run from the repo root so hasura/ + buf template paths resolve unchanged.
set working-directory := '../..'

# Connector link name + endpoint to poll/verify. Override via env or CLI.
connector := env_var_or_default("HASURA_CONNECTOR", "freebusy_pgsql")
graphql_url := env_var_or_default("GRAPHQL_URL", "http://localhost:3280/graphql")

# List recipes (avoids a bare `just hasura` firing a destructive full regen).
[group('hasura')]
default:
    @just --list hasura

# Regenerate the Hasura DDN metadata after the Postgres schema changes (e.g. a
# `just migrate` that added/renamed tables), then refresh the freebusyql Go
# client so the hasura repository layer can use the new shape.
#
# DDN serves its GraphQL API from metadata (the .hml models), not live from
# Postgres, so new/changed tables are invisible until the connector is
# re-introspected, the models regenerated, the supergraph rebuilt, and the
# engine restarted. This does that end to end.
#
#   domain   Optional. Limits the clean+regen to one domain's models (matched by
#            the graphql-cased metadata prefix, e.g. "reservation"). Use this for
#            an additive change confined to one service. OMIT it for a full clean
#            regen of every generated model/command/type — required when the
#            change is breaking or spans domains (a dropped enum, a shared scalar
#            type change like timestamp -> timestamptz, etc.), because
#            `ddn model add "*"` only adds new models and never updates a changed
#            one. The DataConnectorLink is preserved in both modes.
#
#   just hasura regen             # FULL clean regen (breaking/multi-domain)
#   just hasura regen reservation # only the reservation models (additive)
#
# Env toggles:
#   SKIP_RESTART=1   skip rebuilding/restarting the local engine
#   SKIP_CLIENT=1    skip the final client regeneration (buf generate)

# Re-introspect + regenerate DDN metadata (optionally scoped to a domain).
[group('hasura')]
regen domain="":
    #!/usr/bin/env bash
    set -euo pipefail

    CONNECTOR="{{ connector }}"
    GRAPHQL_URL="{{ graphql_url }}"
    DOMAIN="{{ domain }}"
    REPO_ROOT="$(pwd)"
    HASURA_DIR="${REPO_ROOT}/hasura"
    META_DIR="${HASURA_DIR}/freebusy/metadata"

    log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
    die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

    command -v ddn >/dev/null 2>&1 || die "ddn CLI not found on PATH (https://hasura.io/docs/3.0/cli/installation)"
    [[ -d "$HASURA_DIR" ]] || die "hasura project dir not found: $HASURA_DIR"
    [[ -d "$META_DIR" ]]   || die "metadata dir not found: $META_DIR"

    cd "$HASURA_DIR"

    # 1. Re-introspect Postgres so the connector picks up the new/changed tables.
    log "introspecting connector '$CONNECTOR'"
    ddn connector introspect "$CONNECTOR"

    # 2. Drop stale generated metadata so step 3 regenerates it from the freshly
    #    introspected schema. `ddn model add "*"` only ADDS models that don't exist —
    #    it never updates or removes an existing one — so any table whose columns or
    #    types changed (e.g. an enum dropped, or timestamp -> timestamptz) keeps its
    #    stale model and breaks the build. Deleting first forces a clean regen.
    #
    #    The DataConnectorLink (<connector>.hml) is NEVER deleted — it's the connector
    #    definition, refreshed by the introspect above, not a generated model.
    #
    #    With a domain arg: only that domain's models/commands are dropped (safe for
    #    additive changes). With no arg: a full clean regen of every generated model,
    #    command, and the <connector>-types.hml (required for breaking changes that
    #    span domains, like a shared scalar type change). Everything here is
    #    regenerated below and is git-tracked, so `git checkout hasura/` reverts it.
    LINK_FILE="${CONNECTOR}.hml"
    if [[ -n "$DOMAIN" ]]; then
      cap="$(tr '[:lower:]' '[:upper:]' <<<"${DOMAIN:0:1}")${DOMAIN:1}"
      shopt -s nullglob
      stale=(
        "$META_DIR/$cap"*.hml
        "$META_DIR/Insert$cap"*.hml
        "$META_DIR/Update$cap"*.hml
        "$META_DIR/Delete$cap"*.hml
      )
      shopt -u nullglob
      if (( ${#stale[@]} )); then
        log "dropping ${#stale[@]} stale '$cap' model/command files"
        printf '    %s\n' "${stale[@]##*/}"
        rm -f "${stale[@]}"
      else
        log "no existing '$cap' models to drop"
      fi
    else
      log "full clean regen: removing generated metadata (keeping $LINK_FILE)"
      find "$META_DIR" -maxdepth 1 -name '*.hml' ! -name "$LINK_FILE" -delete
    fi

    # 3. (Re)generate models and commands for every collection/procedure. Existing
    #    models are left untouched; the ones dropped above come back normalized.
    log "adding models + commands for '$CONNECTOR'"
    ddn model add "$CONNECTOR" "*"
    ddn command add "$CONNECTOR" "*"

    if [[ "${SKIP_RESTART:-}" == "1" ]]; then
      log "SKIP_RESTART=1 — skipping supergraph build + engine restart"
      exit 0
    fi

    # 4. Rebuild the supergraph and (re)start the local engine.
    log "building supergraph (local)"
    ddn supergraph build local

    log "restarting local engine"
    ddn run docker-start -- -d

    # 5. Wait for the engine to serve the new schema.
    log "waiting for $GRAPHQL_URL to come up"
    for i in $(seq 1 60); do
      if curl -fsS -X POST "$GRAPHQL_URL" -H 'content-type: application/json' \
           -d '{"query":"{ __typename }"}' >/dev/null 2>&1; then
        log "engine is up"
        break
      fi
      [[ $i -eq 60 ]] && die "engine did not come up at $GRAPHQL_URL after 60s"
      sleep 1
    done

    # 6. Refresh the typed Go client from the now-current schema. protoc-gen-orm's
    #    graphql target introspects the engine and regenerates freebusyql (along
    #    with the other orm outputs) — no separate generateql tool required.
    if [[ "${SKIP_CLIENT:-}" == "1" ]]; then
      log "SKIP_CLIENT=1 — skipping client regeneration"
    else
      log "regenerating freebusyql client (buf generate, target=graphql)"
      ( cd "$REPO_ROOT" && buf generate --template tools/protobuf/buf/store.buf.gen.yaml )
    fi

    # Generated metadata and client carry the copyright header too.
    ( cd "$REPO_ROOT" && go run ./tools/license -fix )
    log "done — verify with:"
    printf '    curl -s -X POST %s -H '\''content-type: application/json'\'' \\\n' "$GRAPHQL_URL"
    printf '      -d '\''{"query":"{ __type(name:\\"AllocationHolds\\"){ name fields { name } } }"}'\''\n'
