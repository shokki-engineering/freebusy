# freebusy

A generic availability and booking engine. Resources (RFC 9073) with calendars
(RFC 5545), offered by availability (RFC 7953), held and reserved without
double booking — over gRPC, REST, MCP and CalDAV, on Postgres directly or
through Hasura DDN. Prices, guests and payment live in other services.

```sh
docker compose -f docker/postgres/docker-compose.yaml up -d   # Postgres
just migrate                                                   # schema
just run                                                       # the server
just image-run                                                 # or: the container — the binary and nothing else
```

**[The full guide → docs/README.md](docs/README.md)**: concepts, setup,
configuration, the APIs and booking flow, webhooks, CalDAV, code generation,
testing, CI, and the Bazel build and container image.

---

© 2026 oh-tarnished | Apache 2.0 License
