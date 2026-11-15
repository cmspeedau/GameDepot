# Roadmap

GameDepot is built in phases that each end with something usable. There is no
compatibility target and no upstream to match, so the order below is the whole plan.

Status: `[x]` done, `[ ]` not started, `[~]` in progress.

Only phase 0 is done. Everything else is a target.

## Phase 0: foundations

- [x] Write `README.md`, `ARCHITECTURE.md` and this roadmap.
- [x] Record the core decisions in `docs/adr/`.
- [x] Document the planned desktop HTTP contract.
- [x] Add `AGENT.md` and `CONTRIBUTING.md`.
- [x] Add `SECURITY.md` with the threat model.
- [x] Add AGPL-3.0 `LICENSE` and `NOTICE`.
- [x] Initialise git with `main` as the default branch and repository control files.

Exit criteria: a new contributor can read the docs and know what is being built, why,
and in which order.

## Phase 1: scaffold

- [x] `backend/` Go module, `cmd/server` with config, pgx pool, HTTP server and clean
      shutdown.
- [x] Config parser for the variables in `docs/OPERATIONS.md`, validated at boot.
- [x] goose migrations for the first schema, applied by a `migrate` subcommand and on
      boot in development.
- [x] sqlc setup and the first query set for users, sessions and settings.
- [~] Dashboard shell: layout, navigation, Tailwind pipeline, vendored HTMX, flash
      messages, error pages. Tailwind and HTMX are wired; layout, navigation, flash and
      error pages are still to come.
- [x] Setup and sign-in pages rendering, wired to working handlers.
- [x] `desktop/` Tauri 2 project with Vue 3, Vite and a window that builds. shadcn-vue
      and Tailwind v4 arrive with the UI work.
- [x] `deploy/` Dockerfile and compose file for the server and PostgreSQL. Image builds.

Exit criteria: `go build ./...` and `pnpm build` succeed, the dashboard renders, and
the desktop app builds from the Vite output. All met.

## Phase 2: identity

- [x] Argon2id passwords, sign-in, sign-out, session cookie, CSRF.
- [x] First-run setup token printed in the log and consumed by `/setup`.
- [x] Invitations, signup gating, user list, disable and password reset.
- [x] Session list and revocation.
- [x] Device pairing: code issue, dashboard approval, one-time claim, hashed client
      token.
- [x] Client list and revocation in the dashboard.
- [x] Rate limits on sign-in, setup and pairing.

Exit criteria: a fresh database reaches a signed-in admin, and a desktop device pairs
through the dashboard and holds a working token.

## Phase 3: catalog and objects

- [x] Object store with content type, ETag, cache headers and streaming reads.
- [x] Cover, icon and avatar upload.
- [x] Games: create, edit, delete, description, release date, platform.
- [x] Companies, tags and the game relations.
- [~] Versions and launch configurations. Versions work; launches arrive with the
      desktop phase.
- [x] Collections.
- [~] Admin lists with pagination, search and HTMX fragments. Basic lists work;
      pagination and search are later.

Exit criteria: an admin can create a game with a cover, a version and a launch
configuration, and the desktop can list it through the API.

## Phase 4: releases and depot

- [~] Library sources, directory scan and version detection. Library CRUD works;
      automatic scan and version detection arrive with the desktop phase.
- [x] Release build: 4 MiB chunks, SHA-256 over plaintext, AES-256-GCM per object,
      canonical manifest, manifest digest.
- [x] Atomic publish: temp write, fsync, rename, manifest last.
- [~] Depot routes for manifest and object reads, with range support. Reads work;
      range support is deferred (objects are the resume unit at 4 MiB).
- [x] Integrity check that rehashes every object in a release.
- [x] Task registry with progress, logs and cancellation. Progress and logs work;
      cancellation is a later addition.
- [x] Dashboard publish flow and task view.

Exit criteria: a directory becomes a release, an object serves a valid range, and the
integrity check passes on a fresh publish.

## Phase 5: desktop client

- [x] Depot verification core in Rust: manifest parse, AES-256-GCM decrypt, content-hash
      and file-hash verify, multi-chunk assembly. Proven against a Go-generated golden
      fixture, with tamper-rejection and incremental-download planning tests.
- [ ] Pairing UI, token storage in the OS keyring, sign-out.
- [ ] Library fetch with local install state in SQLite.
- [ ] Resumable download: parallel chunks, verify, decrypt, write, retry with backoff.
- [ ] Incremental install: reuse local objects by manifest chunk match, verify by hash
      before reuse, download only the objects that differ between versions. This is how
      Steam's depot downloads stay cheap across updates.
- [ ] Install layout, file modes, conflict handling.
- [ ] Launch: process spawn, environment, working directory, exit status.
- [ ] Playtime reporting.
- [ ] Settings: server URL, download directory, concurrency, theme.

Exit criteria: pair, download, install and launch a real game, with playtime recorded,
then relaunch after a client restart.

## Phase 6: operations

- [ ] Structured logs with request ids.
- [ ] Health and readiness endpoints.
- [ ] Integrity and cleanup tasks on a schedule.
- [ ] Backup and restore runbook tested against a real dump.
- [ ] Single image build with embedded assets and migrations.
- [ ] Upgrade procedure and version reporting.

Exit criteria: a fresh host runs from compose, survives a restart and restores from
backup.

## Phase 7: breadth

- [ ] Cloud saves: slots, versions, conflict detection, retention, pinning.
- [ ] Store ingestion: GOG first, then Epic, with an encrypted connection store.
- [ ] Metadata providers with a manual fallback.
- [ ] Cache node for LAN and offline use (see below).

Exit criteria: a store title ingests without a local copy, and a save round-trips
between two devices.

## Version 2

Deliberately deferred past the first release. These are scoped for v2, not the v1
phases above.

- [ ] Emulator and compatibility-tool launch helpers. v1 launches native processes
      only; emulator selection, prefixes and per-tool configuration land in v2.
- [ ] Move the release chunk size from 4 MiB to 1 MiB (or content-defined chunking),
      per the revisit trigger in
      [`docs/adr/0008-chunk-size.md`](docs/adr/0008-chunk-size.md). This is a
      `gamedepot-release` version bump with a migration note for existing objects.

Exit criteria for v2: a title launches through a chosen emulator or compatibility tool,
and new releases build at the smaller chunk size with cross-version reuse measurable.

## Future ideas

Recorded so they are not lost. These are not phases and have no exit criteria.

### Cache node

A small node between clients and the main server, so LAN parties and offline setups
pull bytes locally instead of over the WAN.

**Milestone 1: proxy node (plumbing, low risk)**

- A small image, either by extending the repo-root Dockerfile and compose file or by
  running a parallel service.
- Manifest and metadata requests are forwarded live to upstream. They are small, and
  forwarding keeps versions in sync automatically.
- Object requests are forwarded upstream. No caching yet.
- Client tokens are validated against upstream, so the node cannot act as an open
  proxy.
- The client gets a cache URL setting that overrides the server URL.

**Milestone 2: object cache**

- Content-addressed cache on the node's volume. A cached object cannot be stale because
  its name is the hash of its plaintext. Eviction means dropping a file, with no
  invalidation logic.
- LRU eviction against a disk quota.
- Serve on miss, proxying and filling the cache, or cache-only mode, serving only what
  is already resident.

**Milestone 3: admin control of what is cached**

- Dashboard action to pre-cache a game or version to node X, so the cache fills over the
  home connection before the LAN party.
- Pin games so they are never evicted. Everything else stays LRU.
- Node status view: disk usage, what is resident, cache hit and miss statistics.

**Placement:** after the depot and desktop client work, alongside the breadth phase.
The cache is useful only once releases and object reads are stable.

**Design decision to settle first:** cache per chunk or per whole file. Per chunk is
what makes pre-caching and deduplication cheap, and the protocol already streams in
chunks, but it couples the cache to the release manifest. Whole file is simpler and
independent, at the cost of storing and moving more bytes than a client may need.
Current lean: per chunk, since objects are already the unit of transfer. Decide before
the manifest format is frozen in phase 4.

**Precedent:** this is exactly how Steam's community Lancache works, a read-through
per-chunk cache keyed by the content hash. Steam's own tooling (DepotDownloader) reaches
it with a single config switch, the same shape as the client cache URL override above.
The per-chunk lean is confirmed by that model: a cached object keyed by its hash is
never stale and needs no invalidation, so eviction is just dropping a file.
