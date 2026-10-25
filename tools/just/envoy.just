# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Envoy grpc-web proxy runtime. Invoked as `just envoy <recipe>`.
# Fronts the backend on :8080; admin UI on http://localhost:9901.
# Regenerate its config with `just gen envoy`; run the freebusy backend first.
# Recipes run from the repo root so the compose-file path resolves unchanged.
set working-directory := '../..'

# Admin/dashboard port (matches the admin socket in launch.yaml).
admin_port := env_var_or_default("FREEBUSY_ENVOY_ADMIN_PORT", "9901")

# Start the Envoy grpc-web proxy (deploy/docker/envoy/docker-compose.yaml).
[group('proxy')]
up:
    docker compose -f deploy/docker/envoy/docker-compose.yaml up -d

# Stop the Envoy grpc-web proxy.
[group('proxy')]
down:
    docker compose -f deploy/docker/envoy/docker-compose.yaml down

# Open the Envoy admin dashboard (stats, config_dump, clusters, /ready).
[group('proxy')]
dashboard:
    #!/usr/bin/env sh
    url="http://localhost:{{ admin_port }}"
    echo "Opening Envoy admin dashboard: $url"
    if command -v open >/dev/null 2>&1; then open "$url";
    elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$url";
    else echo "No opener found; browse to $url manually."; fi
