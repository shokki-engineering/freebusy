<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Envoy grpc-web proxy

Envoy sits between browser clients (grpc-web / HTTP 1.1) and the freebusy gRPC
backend (HTTP/2), translating grpc-web calls so the frontend can talk to the
services directly.

- **Listener** `:8080` — grpc-web entrypoint (browser → Envoy).
- **Admin UI** `:9901` — http://localhost:9901 (stats, config dump, `/ready`).
- **Upstream** `host.docker.internal:50051` — the freebusy gRPC backend on the host.

## Files

- `launch.yaml` — **generated**, do not edit. One route per gRPC service, all
  pointing at the single `freebusy_backend` cluster. Regenerate with
  `just gen envoy` (also runs as part of `just gen`).
- `docker-compose.yaml` — runs Envoy with `launch.yaml` mounted at
  `/etc/envoy/envoy.yaml`.

## Run it

1. Start the freebusy backend so the gRPC server is listening on `:50051`:

   ```sh
   just run        # or however you start the app locally
   ```

2. Start Envoy:

   ```sh
   just envoy up           # docker compose -f deploy/docker/envoy/docker-compose.yaml up -d
   ```

3. Browser grpc-web clients now hit `http://localhost:8080`. Check Envoy is
   healthy at http://localhost:9901/ready, and stop it with:

   ```sh
   just envoy down
   ```

## Backend host

`launch.yaml` targets `host.docker.internal:50051`, which Docker Desktop (macOS/
Windows) resolves to the host automatically; the compose file adds a
`host-gateway` mapping so it also works on plain Linux.

If you run Envoy **natively** (not in Docker) — e.g. `func-e run -c launch.yaml`
or a local `envoy` binary — regenerate the config pointed at localhost instead:

```sh
go run ./tools/protobuf/envoy -backend-host 127.0.0.1
```

Other knobs: `-backend-port`, `-listen-port`, `-admin-port`, `-cluster`,
`-timeout` (see `go run ./tools/protobuf/envoy -h`).

> **Port note:** the listener defaults to `:8080`, which is also the freebusy
> HTTP gateway's port in local dev. Running Envoy in Docker keeps them separate;
> if you run both natively, regenerate with a different `-listen-port`.
