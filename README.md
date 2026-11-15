# GameDepot

Self-hosted, DRM-free game distribution. A server you run, a desktop client that
installs and launches your library, and no storefront in between.

GameDepot is a hard fork of [Drop OSS](https://github.com/Drop-OSS/drop). The fork
replaces the Node/Nuxt server with a Go service, moves the data plane into that
service, and rebuilds the desktop UI on Vue 3 and Vite. The Rust desktop core, the
database schema, the depot on-disk formats and the client HTTP contract are kept, so
existing installs and paired clients keep working.

## Status

**Design and documentation only.** No server code has been written in this repository
yet. `ROADMAP.md` marks what exists and what does not; treat every other document as
a target, not a description of running software.

The upstream fork parent is a working product. This repository is the rewrite in
progress. Nothing here is ready to deploy.

## What it does

- Catalog games with metadata, tags, companies, age ratings and screenshots.
- Build library versions from a directory on disk, or ingest them from GOG, Epic
  Games Store, Ubisoft Connect and EA.
- Serve the library to paired desktop clients as chunked, checksummed, encrypted
  depot releases.
- Track playtime, profiles and cloud saves.
- Administer everything from a browser dashboard on the same host.

## Stack

| Layer | Choice |
| --- | --- |
| Server | Go 1.26, Gin, pgx v5, sqlc, goose, `log/slog` |
| Database | PostgreSQL, schema frozen from the upstream Prisma models |
| Dashboard | Gin `html/template`, HTMX, Tailwind CSS, CSRF-protected session cookie |
| Desktop | Tauri 2, Rust core, Vue 3 + Vite + shadcn-vue |
| Depot | Go: droplet v2 manifests, AES-128-CTR chunks, SHA-256 CAS, X.509 client certificates |
| Packaging | One container image, one binary, two volumes |

Node is a build-time dependency of the desktop client only. The server image has no
Node, no Nuxt, no Nitro, no nginx and no protobuf sidecar.

## Compatibility

The fork is designed as a binary swap over an existing database, not a migration:

- The PostgreSQL schema and its enum types stay as the upstream Prisma migrations
  left them. No migration runs on boot.
- Dashboard sessions keep the `drop-token` cookie and the `Session` table.
- API tokens keep their `APIToken` rows, ACL string arrays and Bearer handling.
- Paired desktops keep authenticating with `JWT <clientId> <jwt>` signed ES384 by the
  certificate already stored in `Certificate`.
- Depot releases keep the droplet v2 manifest and the `gamedepot-droplet` CAS manifest
  byte for byte, so clients verify and decrypt what they already understand.

## Repository layout

```text
GameDepot/
  backend/        Go module: API, dashboard, depot, tasks
    cmd/server/
    internal/
    sql/          schema baseline, sqlc queries
    web/          templates, static assets, compiled Tailwind
  desktop/        Tauri 2 client (Rust core, Vue 3 + Vite UI)
  libraries/      shared Rust crates, including droplet
  deploy/         Dockerfile, compose, image notes
  docs/           client API reference, ADRs, operations notes
```

`backend/`, `desktop/` and `deploy/` do not exist yet. They are created during phase 1
of the roadmap.

## Requirements

For running the server, once it exists:

- PostgreSQL 13 or newer.
- Two writable volumes: one for the library, one for data (objects, depot, certificates).
- A reverse proxy that terminates TLS, if the instance is not on a private network.
  The server does not terminate TLS itself.

For building:

- Go 1.26 or newer.
- Node 22 and pnpm, desktop client only.
- Rust stable with the Tauri 2 prerequisites for your platform.
- Tailwind CSS standalone CLI v4, or run it from the desktop toolchain.

## Documentation

| Document | Contents |
| --- | --- |
| [`ROADMAP.md`](ROADMAP.md) | Phases, exit criteria, current state |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Runtime topology, modules, data model, formats |
| [`docs/CLIENT-API.md`](docs/CLIENT-API.md) | HTTP contract used by the desktop client |
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | Deployment, configuration, backups, upgrades |
| [`docs/adr/`](docs/adr/README.md) | Decision records and the reasoning behind them |
| [`AGENT.md`](AGENT.md) | Rules for AI agents and humans changing this repo |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Branching, commits, review, fork boundary |
| [`SECURITY.md`](SECURITY.md) | Threat model and reporting |

## License

AGPL-3.0-only. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).

GameDepot is derived from Drop OSS, which is AGPL-3.0. The fork keeps that license,
keeps the upstream copyright notices, and publishes its own source. If you run a
modified GameDepot as a network service, the AGPL requires you to offer that modified
source to the people using it.
