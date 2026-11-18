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
- [x] Install orchestration: `installer::install_version` drives release lookup, manifest
      fetch and digest check, object download with verify-then-cache, file assembly, and
      store bookkeeping. Tested end to end against a fake `ApiClient` backed by the golden
      fixture, including a second install that resumes from the cache without refetching.
- [~] Pairing UI, token storage in the OS keyring, sign-out. A Vue screen drives
      `pair_initiate` / `pair_complete` / `signed_in` / `sign_out`; the token lives in the
      keyring. Logic is tested and the app compiles and type-checks, but a live desktop
      session (and OS keyring) has not exercised it.
- [x] Tauri command and state layer. `AppState` (mutex-guarded SQLite store, settings,
      keyring) is managed and exposed via `settings_get/set`, `pair_*`, `signed_in`,
      `sign_out`, `library_list`, `install_version`, `playtime_report`.
- [~] Library fetch with local install state in SQLite. The install store (`store.rs`)
      and the API client (`api.rs`, `ApiClient` trait over reqwest) are built; the store
      is fully tested, the reqwest transport compiles but is exercised only against a
      live server.
- [~] Resumable download: parallel chunks, verify, decrypt, write, retry with backoff.
      `download::ensure_objects` (bounded concurrency, decrypt-verify before marking
      done, exponential backoff, cache resume) and `assemble_from_cache` are tested with
      fake sources; the real `HttpApiClient` object fetch is not run here.
- [~] Incremental install: reuse local objects by manifest chunk match, verify by hash
      before reuse, download only the objects that differ between versions. This is how
      Steam's depot downloads stay cheap across updates. The planner
      (`missing_objects`), the reuse inheritance (`Store::inherit_object_state`) and the
      install path are tested together end to end; only the network fetch is left.
- [x] Install layout, file modes, conflict handling. Files write atomically with the
      manifest mode and reject traversal paths; an existing file with the correct hash is
      skipped, a differing one is overwritten. Tested.
- [~] Launch: process spawn, environment, working directory, exit status. `launch.rs`
      spawns via execve with an args array (no shell), applies env and cwd, captures exit
      status and output, and detaches for real games. Tested against real processes; wiring
      to a downloaded install and running-game tracking is next.
- [~] Playtime reporting. `playtime.rs` accumulates per-session seconds with an
      injectable clock and reports deltas through `ApiClient::post_playtime`, clamped
      to the server's accepted range; `report_session` tested with a mock client. The
      `playtime_start`/`playtime_stop` commands and AppState bookkeeping are wired
      (compile-checked); live timing in a game session is not yet exercised.
- [x] Settings: server URL, download directory, concurrency, theme.

Exit criteria: pair, download, install and launch a real game, with playtime recorded,
then relaunch after a client restart.

## Phase 6: operations

- [x] Structured logs with request ids. Gin middleware assigns an `X-Request-Id` (new
      UUID or client supplied) into the request context, access lines are structured JSON
      `http_request` records carrying `method`, `path`, `status`, `duration_ms` and
      `request_id`, and the slog default handler adds the id from context. Covered by
      unit tests; verified live.
- [x] Health and readiness endpoints. `GET /api/v1/health` is liveness (`204`, no
      database check); `GET /api/v1/ready` is readiness (database ping, `200`/`503`).
      Verified live.
- [x] Integrity and cleanup tasks on a schedule. `internal/maintenance` re-verifies every
      release manifest against the depot (digest, format, chunk references, object
      existence and decryption) on `GAMEDEPOT_INTEGRITY_INTERVAL` (default 24h) and
      sweeps expired pairing codes and sessions on `GAMEDEPOT_CLEANUP_INTERVAL`
      (default 1h). Jobs never touch object storage; failures are logged per digest and
      summarized. Unit-tested with a fake store, verified live: integrity reported the
      published release and cleanup deleted injected expired rows.
- [x] Upgrade procedure and version reporting. `GET /api/v1/version` reports the build
      (`internal/version`, settable at link time); the boot log carries the same value.
      Upgrade runbook in `docs/OPERATIONS.md` now includes the post-swap version and
      readiness checks.
- [x] Backup and restore runbook tested against a real dump. `pg_dump --format=custom`
      of a migrated database restored cleanly into a fresh database with all six goose
      migrations and the full schema present; runbook steps unchanged.
- [x] Single image build with embedded assets and migrations. `docker build -f
      deploy/Dockerfile .` completes; the binary embeds the dashboard assets via
      `go:embed` and migrations ship in the module, and the image applies them on boot.

Exit criteria: a fresh host runs from compose, survives a restart and restores from
backup.

## Phase 7: breadth

- [~] Cloud saves: slots, versions, conflict detection, retention, pinning. Server-side
      is complete and smoke-tested against a live server (push with optimistic
      concurrency, idempotent no-op, stale-base conflict 409, per-version download,
      restore, pin, delete guards, retention window, and the save-path manifest read).
      Desktop is wired end to end and verified live against the Go server: save-root
      template resolution, per-slot sync cursor, and the four-way sync (push, pull,
      noop, conflict with take-server and keep-mine resolutions), plus the Vue saves
      UI. The two-device save round-trip passed. Remaining for this item: a
      Windows/macOS path-template run and the store side below.
      Out of scope by decision (ADR-0011): any integration with a provider's own save
      cloud. GameDepot neither reads nor writes Ubisoft Connect saves, and a title with
      both writers active can lose progress that neither side can explain, so the two
      are mutually exclusive per game rather than reconciled.
- [~] Store ingestion: GOG first, then Epic, with an encrypted connection store. GOG is
      ported to Go from this project's archived predecessor (ADR-0009): the code-paste
      sign-in, the three documented library endpoints with the owned-ids top-up, and
      content-system acquisition (build list, zlib build meta, depot manifests,
      per-product secure links, chunk verify on both declared digests). Credentials are
      sealed with AES-256-GCM under `GAMEDEPOT_CREDENTIAL_KEY`; the store refuses to boot
      without it, refresh-token rotation is written back, and a rejected credential flags
      the connection instead of retrying. `internal/ingest` turns an acquired directory
      into a release through the existing depot builder, skipping unchanged builds and
      de-duplicating concurrent requests per product. Dashboard: connect, disconnect,
      entitled library, ingest with platform and force, task page for progress.
      Verified live against production GOG for the OAuth path (a bogus code reached GOG's
      real `invalid_grant`), plus CSRF gating, token-never-in-page, and the
      undecryptable-credential flag through Postgres.
      Platform handling was wrong in the predecessor and is fixed and measured
      here (ADR-0010): the build API accepts only `windows` and `osx`, so
      `macos` maps to `osx` on the way out and the library's `Mac` flag
      normalises to `macos` on the way in; the acquired build's own `platform`
      label is checked against the request, and the dashboard offers only the
      platforms GOG reports per title. Mac ingestion is proven against
      production for build, meta and depot manifest (576 files with
      `.app/Contents/MacOS/` paths). Native Linux is refused with an explanation
      rather than downgraded: GOG's content system answers `Unsupported OS` for
      `linux`, and its Linux builds live on the installer downlink API and
      chunklist instead.
      Decided against, not deferred (ADR-0010): native Linux acquisition. GOG ships a
      Linux build as one self-extracting installer, 21.2 GB measured on The Witcher 2,
      behind a login-gated downlink, with no cross-version dedupe and an external
      extractor as a host dependency. Linux titles are ingested as their Windows build
      and run through the existing compatibility tool; the picker shows Linux disabled
      and the ingest route rejects it before starting a task.
      Follow-ups: one end-to-end ingest with an operator's own GOG account, which is the
      step secure_link needs a credential for, and confirming Mac launch entries on it.
      Ubisoft is in progress: the install-manifest decode (fixed 356-byte header, framing
      sniffed from magic across zstd, gzip, zlib and raw deflate, and a candidate accepted
      only when it yields chunks that carry files) and the download plan (required chunks
      plus the wanted language, absolute and traversal paths refused, the empirical
      slice-directory derivation pinned by worked example, all-zero offsets read as
      sequential, shared slices fetched once) are done with 25 tests. Remaining: the demux
      socket client, session auth, and the configuration-YAML launch documents.
- [ ] Runner recipes made real (ADR-0012). The table and `GET /api/v1/games/{id}/recipe`
      already exist, and nothing uses them: define the `profile` shape on the server
      instead of serving an unvalidated blob, give admins a create/edit form on the game
      page, have the desktop read the recipe before it launches, and record
      `compatibility` from a real attempt rather than leaving everything `untested`.
- [ ] Metadata providers with a manual fallback.
- [ ] Cache node for LAN and offline use (see below).

Exit criteria: a store title ingests without a local copy, and a save round-trips
between two devices.

## Version 2

Deliberately deferred past the first release. These are scoped for v2, not the v1
phases above.

- [ ] Store-client handoff and compatibility-tool launch helpers (ADR-0012). v1 plays
      `direct` releases: native processes, or a Windows title through umu with a
      discovered Proton build. Starting a `store_client` release needs the rest, and it
      is v2 because each piece is provider research rather than plumbing: installing and
      registering the storefront in the title's prefix, writing install records derived
      from a legitimate install (never a dumped layout), launching through the client's
      own handler, and tracking the real game process so Stop and playtime are attributed
      to it instead of to the storefront. Emulator selection and per-tool configuration
      land here too.
- [ ] Move the release chunk size from 4 MiB to 1 MiB (or content-defined chunking),
      per the revisit trigger in
      [`docs/adr/0008-chunk-size.md`](docs/adr/0008-chunk-size.md). This is a
      `gamedepot-release` version bump with a migration note for existing objects.
- [ ] Serve the desktop client from the server (see below).

Exit criteria for v2: a title launches through a chosen emulator or compatibility tool,
one `store_client` title plays from a depot copy with its storefront's own entitlement
check answering, and that session reports Stop and playtime against the game process,
and new releases build at the smaller chunk size with cross-version reuse measurable.

### Desktop client served by the server

The launcher should be downloadable from the instance that runs it, not only from GitHub;
[Drop asked for the same thing](https://github.com/Drop-OSS/drop/issues/196) and never got
an answer. This is v2 because it depends on a release pipeline the first version does not
need.

- A downloads page that picks the installer for the visitor's OS, and the same artifacts
  reachable by direct URL for scripts.
- The Tauri 2 updater points at the server: `latest.json` plus target/arch artifacts under
  a mounted `clients/` volume, with the pubkey pinned in the app. No runtime fetch from
  GitHub, so an air-gapped or LAN-only instance still updates.
- The manifest is signed and the installer verifies it before running anything; the
  SHA-256 in the same document is a corruption check, not authenticity. This is the
  Zorven 4.2 lesson applied before it becomes a finding.
- Version skew is explicit: a server records which client versions it serves, and the
  client refuses an update older than itself.

Not in scope: building the client inside the server image, or serving macOS/Windows
installers from a Linux-only deployment without uploading them first.

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

### Mesh and roaming

Handhelds leave the house, so one server URL is not enough. The plan is Tailscale,
Headscale and NetBird as transport, with the desktop holding a LAN endpoint and a mesh
endpoint and choosing between them. The detail lives in
[issue #9](https://github.com/cmspeedau/GameDepot/issues/9); two constraints belong here
because they shape the cache and save-sync work:

- **Relay state, not the metered flag.** A device can report an unmetered network while
  the mesh forces its traffic through a public relay. Read the path from the daemon
  (`CurAddr`/`Relay` in Tailscale, the relayed flag in NetBird) and treat relayed like
  metered: background chunk transfer pauses, saves and metadata continue. With plain
  WireGuard there is no daemon to ask, so measure RTT and throughput instead, as below.
- **Parallel endpoint probe.** Qualify LAN and remote together with a few hundred
  milliseconds per endpoint and take the first healthy answer, instead of falling back
  serially and waiting out a TCP timeout when a device leaves home Wi-Fi.

Bulk transfer belongs on a direct WireGuard path. An outbound HTTPS tunnel is a valid
access route for the dashboard and small metadata behind CGNAT, but it is one
multiplexed TCP stream capped by the relay host's uplink, not a substitute for a direct
path when moving depot objects.

### WireGuard: don't bundle it (decided)

The audience already runs the network. A homelabber self-hosting GameDepot has WireGuard
somewhere: OPNsense or pfSense, wg-easy, a `wg0` peer on a VPS, or Headscale/Tailscale/
NetBird when CGNAT makes plain WireGuard painful. Shipping a second WireGuard stack would
duplicate keys, compete for ports and routes, and demand a TUN driver on Windows for no
capability the operator does not already have. It is recorded here as considered and
rejected, not deferred.

GameDepot consumes the existing network instead of building one:

- Endpoints are URLs (a LAN IP, a WireGuard address, a mesh DNS name) and the client
  stores several and races them. It never creates a tunnel, holds a peer key, or touches
  routing.
- Keys, peers and NAT traversal stay with the operator's stack.
- The path policy from the mesh section still applies, but plain WireGuard has no daemon
  API to ask: no `Relay` field, no `CurAddr`. Detect empirically instead: RTT and
  first-chunk throughput against the endpoint, plus a per-endpoint "treat as metered"
  switch, because a WireGuard endpoint on a VPS is a relay even when the tunnel itself
  is direct and the OS reports the interface as ordinary Ethernet.
- Documentation should cover wg-easy, OPNsense and a single VPS peer, not a control
  plane.

A counterexample worth watching: Drop, the closest comparable project, embeds Tailscale in
its Tauri app (`desktop/src-tauri/tailscale`) instead of assuming the host already runs a
mesh. That changes what users come to expect, even if it is more software to ship.

If a self-contained direct path ever becomes a hard requirement (no external stack and
no public endpoint), QUIC with mutual TLS is a cheaper answer than embedding WireGuard:
no TUN, no root, and connection IDs survive an address change.
