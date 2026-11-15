# Roadmap

This roadmap is the plan for replacing the upstream Node/Nuxt server with a Go
service, and for rebuilding the desktop UI on Vue 3 and Vite. It is ordered so that
each phase produces something testable against a real client.

Status legend: `[x]` done and merged, `[ ]` not started, `[~]` partially done.

Nothing in phases 1 to 7 has code behind it. Phase 0 is documentation, which is what
this repository currently contains.

## Phase 0: foundations

Documentation, license and repository control.

- [x] Write `README.md`, `ARCHITECTURE.md` and this roadmap.
- [x] Record the architectural decisions in `docs/adr/`.
- [x] Document the desktop client HTTP contract in `docs/CLIENT-API.md`.
- [x] Add `AGENT.md` and `CONTRIBUTING.md` with fork boundary and branch rules.
- [x] Add `SECURITY.md` with the threat model.
- [x] Add AGPL-3.0 `LICENSE` and a `NOTICE` with upstream attribution.
- [x] Initialise git with `main` as the default branch, `.gitignore`, `.gitattributes`
      and `.editorconfig`.

Exit criteria: a new contributor can read the docs and know what is being built, why,
and in which order.

## Phase 1: scaffold

A Go service that boots, connects to the database, serves the dashboard shell, and a
desktop client that builds without Nuxt.

- [ ] `go.mod` for Go 1.26 at the repository root target `backend/`.
- [ ] `cmd/server` that loads config, opens a pgx pool, starts the HTTP server, and
      shuts down cleanly on signal.
- [ ] Config package with parity for every environment variable in
      `docs/adr/0004-configuration-parity.md`.
- [ ] `sql/schema.sql` generated once from the frozen Prisma migrations, plus a goose
      baseline that records the version without altering an existing database.
- [ ] sqlc configuration and a first query set for `User`, `Session`, `APIToken`.
- [ ] Dashboard shell: layout, navigation, Tailwind pipeline, HTMX vendored locally,
      flash messages, error pages.
- [ ] Setup and sign-in pages rendering, wired to stub handlers.
- [ ] Desktop: Vue 3 + Vite project replacing Nuxt, `unplugin-vue-router`,
      auto-imports, `NuxtLink` shim, Tailwind v4 palette, `vite build` output consumed
      by `tauri.conf.json`. The existing screens must render.

Exit criteria: `go build ./...` and `pnpm build` in `desktop/main` both succeed; the
dashboard renders an empty shell; the desktop app opens its sign-in screen from the
Vite build.

## Phase 2: identity and dashboard

Everything needed to sign in, create the first account, and approve a client.

- [ ] Argon2id password hashing with bcrypt verification fallback for existing rows.
- [ ] `drop-token` session cookie, `Session` rows, sign-in, sign-out, remember me.
- [ ] ACL manager: global ACLs, user ACLs, admin flag, 10 minute superlevel window.
- [ ] Setup token printed to logs on first run, consumed by `POST /api/v1/setup`.
- [ ] Invitation creation and redemption, including signup gating.
- [ ] User list, edit, disable, password reset, ACL assignment.
- [ ] Session list and revocation.
- [ ] Client pairing: code display, approval page, `Client` row, certificate issuance.
- [ ] CSRF tokens on every dashboard form, same-origin checks on WebSocket upgrades.
- [ ] Dashboard security headers and cache policy.

Exit criteria: a fresh database reaches a signed-in admin through the browser, and the
desktop pairing code can be approved in the dashboard.

## Phase 3: objects and catalog

The content an admin manages before any game can be downloaded.

- [ ] Filesystem object store with permissions, ETag, `If-None-Match`, and streaming
      reads at `/api/v1/object/:id`.
- [ ] Image upload and resize for logos, avatars, covers and screenshots.
- [ ] Games: create, edit, delete, platforms, release date, description.
- [ ] Companies, tags, genres, age ratings and rating organizations.
- [ ] Game versions: list, edit, delete, launch configurations and setups.
- [ ] Collections and collection entries.
- [ ] Store listing fields needed by the desktop client, if store mode is enabled.
- [ ] Admin list views with pagination, search and filters, rendered as HTMX fragments.

Exit criteria: an admin can create a game with a cover and a version row in the
dashboard, and the desktop client can list it through `/api/v1/client/game`.

## Phase 4: library and depot in Go

The data plane, which is the part the Rust `torrential` service used to own.

- [ ] Filesystem library provider: detect a version directory, list files, stat files.
- [ ] Flat library provider for single-archive imports.
- [ ] Droplet v2 manifest generation: descending size sort, 64 MiB chunks, at most 512
      files per chunk, random 16-byte key, random 16-byte IV per chunk, SHA-256 over
      plaintext, AES-128-CTR with a 64-bit little-endian counter.
- [ ] Byte-for-byte equivalence tests against `libraries/droplet` vectors.
- [ ] CAS publish: 4 MiB fixed chunks, `objects/<xx>/<sha256>`, canonical JSON
      `gamedepot-droplet` v1 manifest, `manifests/<digest>.json`, atomic rename after
      verify.
- [ ] Depot HTTP surface at `/api/v1/depot/`: `manifest.json`, `speedtest`,
      `content/{game}/{version}/{chunk}`, `POST /invalidate`, `/healthcheck`.
- [ ] `Depot` row upsert pointing at `${EXTERNAL_URL}/api/v1/depot/`.
- [ ] CA and certificate issuance in pure Go, compatible with stored PEM bundles.
- [ ] Import wizard: pick a source, pick a directory, generate the manifest, publish.
- [ ] Task registry with progress, logs and cancellation.
- [ ] Task WebSocket feed with the existing text protocol.

Exit criteria: a release generated by Go is downloaded and verified by an unmodified
desktop client, and a legacy droplet v2 chunk decrypts correctly.

## Phase 5: desktop client contract

The 18 endpoint families the Tauri client calls, plus saves and import sessions.

- [ ] `POST /api/v1/client/auth/initiate`, code WebSocket, `POST /auth/handshake`.
- [ ] `GET /api/v1/client/user`, `GET /user/library`.
- [ ] `GET /api/v1/client/game`, `GET /game/manifest`, `GET /depots`.
- [ ] `POST /api/v1/client/depot/import/session` and its chunk upload path.
- [ ] `GET /api/v1/client/depot/manifest`, `GET /depot/content/...`.
- [ ] `GET /api/v1/settings`, `GET /api/v1`.
- [ ] Playtime reporting and aggregation.
- [ ] Profiles and profile images.
- [ ] Collections read path.
- [ ] Saves: manifests, versions, conflict rules, retention, `saves/manifests/{gameId}`.
- [ ] Auth parity suite: replay every desktop request against both servers and diff.

Exit criteria: pair, list, download, launch and record a play session with the real
Tauri client, then sync a save.

## Phase 6: ingestion and metadata

Breadth. This phase is the most exposed to third-party services.

- [ ] Encrypted store connection store using a 32-byte credential key.
- [ ] GOG: OAuth device flow, depot manifests, CDN streaming.
- [ ] Epic: authorization code, entitlement check, manifest and chunk download.
- [ ] Ubisoft Connect: session API, game streaming.
- [ ] EA: ticket flow, library listing.
- [ ] Ingest queue with task keys, so a duplicate ingest is refused rather than queued.
- [ ] Provider build ID, release manifest ID and content digest kept as separate
      identities.
- [ ] Metadata providers: IGDB, GiantBomb, PCGamingWiki, Steam, manual entry.
- [ ] Metadata matching UI in the dashboard.
- [ ] Update check per connection, queued through the same ingest pipeline.

Exit criteria: a GOG and an Epic title ingest end to end from the dashboard, and a new
build of an already ingested title queues itself.

## Phase 7: cutover

Replace the Node server in production.

- [ ] OIDC, WebAuthn and TOTP only if a concrete need remains after the UI change.
- [ ] Nightly compatibility run against a database dumped from a live upstream install.
- [ ] End-to-end suite: setup, invite, sign in, pair, import, download, play, save.
- [ ] Single image build: Go binary, dashboard assets, migrations, no Node.
- [ ] Restore-from-backup runbook tested against a real backup.
- [ ] Release notes describing what an operator must do to swap binaries.
- [ ] Tag the first release, publish source and image.

Exit criteria: an existing install swaps the image, keeps its sessions, tokens and
paired clients, and needs no manual database work.

## Explicitly out of scope

These were removed from the rewrite and are not planned:

- The Nuxt web UI, the Nitro server, and the browser player experience.
- Prisma at runtime, and the Node migration runner.
- The Rust `torrential` service, its protobuf socket and the nginx split.
- Public store browsing, news, community pages and public user profiles in the browser.
- Web sessions, browser WebAuthn, browser TOTP and browser OIDC flows.
- Web tokens minted for the removed web UI.

Archive-backed library versions (zip, 7z, iso) are a possible later item. Phase 4
ships directory versions first, because the Rust backend treated archives as a separate
backend and the Go port can add it without changing the manifest format.
