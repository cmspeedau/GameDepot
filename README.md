# GameDepot

Self-hosted game distribution for a private library. A server you run, a desktop
client that installs and launches games, and no storefront in between.

GameDepot is an independent project. It defines its own database schema, depot format,
client protocol and desktop client. No source code from another game distribution
project is included or ported.

## Status

**Design and documentation only.** No server or client code exists yet. `ROADMAP.md`
records what is planned and what is not started.

## What it will do

- Catalog games with metadata, tags, companies and cover art.
- Build installable versions from directories on disk.
- Serve releases to paired desktop clients as chunked, checksummed, encrypted objects.
- Install, verify and launch games from the desktop client.
- Track playtime and collections.
- Administer the instance from a browser dashboard on the same host.
- Add cloud saves and store ingestion after the core works.

## Stack

| Layer | Choice |
| --- | --- |
| Server | Go 1.26, Gin, pgx v5, sqlc, goose, `log/slog` |
| Database | PostgreSQL 13 or newer, schema owned by this project |
| Dashboard | Gin `html/template`, HTMX, Tailwind CSS, CSRF-protected session cookie |
| Desktop | Tauri 2, Rust core, Vue 3 + Vite + shadcn-vue |
| Depot | Go: 4 MiB content-addressed chunks, AES-256-GCM, canonical JSON manifests |
| Packaging | One container image, one binary, two volumes |

Node is a build-time dependency of the desktop UI only. The server image has no Node.

## Design principles

1. **Independent by construction.** Every format and table is defined here. There is no
   compatibility layer to maintain and no upstream contract to satisfy.
2. **One server process.** The API, the dashboard and the depot run in one Go binary.
   PostgreSQL is the only external service.
3. **Content addressed.** Release objects are named by the SHA-256 of their plaintext.
   A cached object cannot be stale.
4. **Verify before use.** Clients hash every chunk, decrypt it, hash the plaintext, and
   only then write a file.
5. **Small surface.** The desktop calls a focused API. Administration is server-rendered
   HTML, not a second application.

## Repository layout

```text
GameDepot/
  backend/          Go module: API, dashboard, depot, tasks
    cmd/server/
    internal/
    sql/            goose migrations, sqlc queries
    web/            templates, static assets, compiled Tailwind
  desktop/          Tauri 2 client
    src/            Vue 3 + Vite UI
    src-tauri/      Rust core
  deploy/           Dockerfile, compose, image notes
  docs/             client API, operations, decision records
```

`backend/`, `desktop/` and `deploy/` do not exist yet. They are created in phase 1.

## Requirements

To run the server, once it exists:

- PostgreSQL 13 or newer.
- Two writable volumes: one for the library, one for data (objects, depot, keys).
- A reverse proxy that terminates TLS if the instance is reachable from an untrusted
  network.

To build:

- Go 1.26 or newer.
- Node 22 and pnpm for the desktop UI.
- Rust stable with the Tauri 2 prerequisites for your platform.
- Tailwind CSS standalone CLI v4, or the desktop toolchain.

## Documentation

| Document | Contents |
| --- | --- |
| [`ROADMAP.md`](ROADMAP.md) | Phases, exit criteria, current state |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | Runtime, modules, schema, depot format |
| [`docs/CLIENT-API.md`](docs/CLIENT-API.md) | HTTP contract for the desktop client |
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | Deployment, configuration, backups, upgrades |
| [`docs/adr/`](docs/adr/README.md) | Decision records and the reasoning behind them |
| [`AGENT.md`](AGENT.md) | Rules for AI agents and humans changing this repo |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Branching, commits, review |
| [`SECURITY.md`](SECURITY.md) | Threat model and reporting |

## License

AGPL-3.0-only. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).

The license is a deliberate choice for this project, not an inherited obligation. If
you run a modified GameDepot as a network service, the AGPL requires you to offer that
modified source to the people using it.
