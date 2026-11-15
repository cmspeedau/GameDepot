# Architecture

This document describes how GameDepot is meant to be built: the processes, the modules,
the data model, and the formats that must match the existing clients. It also records
what is being removed and why. Where it states a format detail, that detail is a
compatibility requirement, not a preference.

Status: design target. Nothing in phases 1 to 7 of the roadmap exists yet.

## 1. Constraints

Four constraints drive the design.

1. **The desktop client is the product.** It is a Tauri application with a Rust core
   that already handles pairing, downloads, launching, process management, saves and
   mods. It must keep working against a new server.
2. **Existing installs swap binaries.** The PostgreSQL schema is frozen. No boot-time
   migration, no data rewrite, no forced re-login, no re-pairing of clients.
3. **One server process.** No nginx, no protobuf sidecar, no separate depot service, no
   Node runtime. The server is a single Go binary plus PostgreSQL.
4. **No Node in the server image.** The dashboard is server-rendered HTML. The desktop
   client may use Node at build time only.

## 2. Runtime topology

```text
                    ┌──────────────────────────────┐
  Desktop client    │  Go server (Gin)             │
  Tauri + Rust  ────┤  /api/v1/client/*            │
  JWT ES384         │  /api/v1/depot/*             │
                    │  /api/v1/*                   │
                    │                              │
  Browser        ───┤  /dashboard/*   HTML + HTMX  │──────┐
  drop-token cookie │  /auth/*        setup, auth  │      │
                    └──────────────┬───────────────┘      │
                                   │                      │
                        ┌──────────┴──────────┐   ┌───────┴────────┐
                        │  PostgreSQL         │   │  Filesystem    │
                        │  frozen schema      │   │  /library      │
                        └─────────────────────┘   │  /data         │
                                                  └────────────────┘
```

There is no separate depot port. The desktop client discovers the depot through the
`Depot` table, whose single row points at `${EXTERNAL_URL}/api/v1/depot/`. In the old
deployment that URL was served by nginx in front of `torrential` on port 5000. In the
new deployment Gin serves it on the same port as everything else. Clients that already
have the URL stored keep using it.

### Removed from the old topology

| Removed | Replaced by |
| --- | --- |
| Nuxt/Nitro server | Gin `html/template`, HTMX, Tailwind |
| Prisma runtime and migration deploy | Frozen SQL baseline, goose version record only |
| Rust `torrential` | Go depot package in the same binary |
| nginx | Direct listeners; TLS terminates at the operator's proxy |
| Protobuf control socket on port 33148 | In-process function calls |
| Port 5000 | Nothing |
| `libraries/droplet` in the server | Kept only as a desktop download dependency |

## 3. Repository layout

```text
backend/
  cmd/server/main.go          boot, config, pool, scheduler, HTTP, shutdown
  internal/
    api/                      route registration, DTOs, error mapping
      client/                 desktop client endpoints
      depot/                  data plane endpoints
      dashboard/              HTML handlers
      auth/                   setup, sign-in, sign-out, invitations
    acl/                      ACL evaluation, superlevel windows
    auth/                     password hashing, sessions, tokens
    catalog/                  games, companies, tags, versions, collections
    objects/                  filesystem object store
    library/                  providers, manifests, import
    depot/                    droplet v2, CAS, chunk serving
    clients/                  pairing, certificates, JWT verification
    saves/                    save manifests, versions, slots, conflicts
    playtime/                 playtime ingestion and aggregation
    tasks/                    task registry, progress bus
    notify/                   notifications
    metadata/                 IGDB, GiantBomb, Steam, PCGamingWiki
    stores/                   GOG, Epic, Ubisoft, EA adapters
    config/                   environment parsing, validation
    database/                 pgx pool, transaction helpers
    web/                      templates, static assets, Tailwind output
  sql/
    schema.sql                baseline generated from Prisma migrations
    queries/*.sql             sqlc inputs
    migrations/               goose, baseline only
  sqlc.yaml
desktop/
  main/                       Vue 3 + Vite UI
  src-tauri/                  Rust core, unchanged in phase 1
libraries/
  droplet/                    Rust crate used by the desktop
deploy/
  Dockerfile
  compose.yaml
docs/
```

The upstream repository also contains `cli/`, `app/`, `backend/`, `torrential/`,
`packages/` and `ubisoft-demux/`. Of those:

- `cli/` is a small operator manifest tool. Port it to a Go subcommand only if operators
  still need it; otherwise leave it out.
- `app/` is a single stray admin page and is not carried over.
- `backend/` is an unrelated Go scaffold using gorilla/mux and is not carried over.
- `torrential/` and `packages/` are dropped.
- `ubisoft-demux/` supports Ubisoft ingestion and is dropped with the Ubisoft adapter
  rewrite, unless live testing shows the protocol work is not reproducible in Go.

## 4. HTTP surface

Three groups, separated by authentication mechanism and by consumer.

### 4.1 Client API (`/api/v1/client/*`)

JSON, consumed by the Tauri client. Authenticated with `Authorization: JWT <clientId>
<jwt>`. The JWT is ES384, signed by the private key of the client certificate issued
during pairing. Verification uses the certificate stored in the `Certificate` table, so
certificates issued by the old server keep verifying.

The full contract is in [`docs/CLIENT-API.md`](docs/CLIENT-API.md). The endpoint families are:

```text
auth/initiate, auth/code/ws, auth/handshake
user, user/library, user/webtoken (removed, see 4.4)
game, game/manifest
depot/manifest, depots
depot/content/{game}/{version}/{chunk}
depot/import/session
playtime
profiles
collection
saves/manifests/{gameId}
```

### 4.2 Data plane (`/api/v1/depot/*`)

Served by Gin, no session, no bearer token. The chunk URL itself is the capability: it
contains the game, version and chunk identifiers that only a paired client learns from
a manifest. Content is AES-128-CTR encrypted with a per-release key that is only in the
manifest, so knowing a URL is not enough to read bytes usefully.

```text
GET  /api/v1/depot/manifest.json                  depot capability and manifest list
GET  /api/v1/depot/speedtest                      50 MiB of zeros
GET  /api/v1/depot/content/{game}/{version}/{chunk}
POST /api/v1/depot/invalidate                     drops cached manifest context
GET  /api/v1/depot/healthcheck
```

### 4.3 Dashboard (`/dashboard/*`, `/auth/*`)

HTML, consumed by a browser. Session cookie `drop-token`, `HttpOnly`, `SameSite=Lax`,
`Secure` when the external URL is HTTPS. Every state-changing form carries a CSRF token
derived from the session and compared in constant time.

```text
GET  /auth/signin, POST /auth/signin
GET  /auth/signup, POST /auth/signup        invitation-gated
GET  /auth/setup,  POST /auth/setup         setup token, first run only
GET  /auth/signout
GET  /dashboard                             overview
GET  /dashboard/catalog, /catalog/games/{id}, /catalog/companies, /tags
GET  /dashboard/library, /library/sources, /library/import
GET  /dashboard/depot, /depot/connections, /depot/publish
GET  /dashboard/tasks, /tasks/{id}
GET  /dashboard/clients                     pairing approvals
GET  /dashboard/users, /users/{id}, /users/invitations
GET  /dashboard/saves, /dashboard/settings
```

Every list is an HTMX fragment endpoint under `/dashboard/_/*`, so pagination, search
and inline edits return partial HTML rather than JSON.

### 4.4 Public API kept for compatibility

The old server exposed `/api/v1/index.get`, `/api/v1/settings` and `/api/v1/setup`.
`/api/v1/settings` is read by the desktop client. `/api/v1/setup` consumes the setup
token and is kept so a first run does not change shape. Everything else at `/api/v1/*`
that existed only to feed the removed web UI is dropped.

Dropped route families include `/api/v1/store/*`, `/api/v1/news/*`, `/api/v1/screenshots/*`
reads used by the web pages, `/api/v1/user/*` web account routes, `/api/v1/admin/*`
JSON routes superseded by dashboard handlers, and `client/user/webtoken`, which only
minted tokens for the removed web client.

## 5. Authentication and authorization

Four credential types coexist, all backed by the frozen schema.

### 5.1 Dashboard sessions

- Sign-in matches `User.username`, looks up `LinkedAuthMec` with `mec = Simple`, and
  verifies the stored password hash.
- Argon2id hashes are the default for new passwords. Existing bcrypt hashes verify and
  are rehashed on the next successful sign-in, so the fallback can be removed later.
- A row in `Session` holds the session token, user, expiry and remember-me flag. The
  cookie value is the session token.
- Sessions expire on a fixed horizon and are revoked by deleting the row. A dashboard
  page lists sessions with user agent and last use.
- ACL evaluation order: explicit user ACLs, group ACLs, admin override, then the
  superlevel window. The window is a short-lived elevation recorded in the session and
  used for destructive operations such as deleting a game or minting a token.

### 5.2 API tokens

`APIToken` rows carry `mode` (`User`, `Client`, `System`) and a string array of ACLs.
Bearer authentication reads the token, compares a hash, applies the ACLs, and records
last use. System tokens with the `setup` ACL are created once at first boot, printed to
the log, and deleted by a successful setup.

### 5.3 Desktop client certificates

Pairing creates a `Client` row and a `Certificate` row. The certificate is ECDSA P-384,
issued by an internal CA whose root PEM lives in the object store under the key `ca`.
The client receives `{private, certificate, id}` from `POST /api/v1/client/auth/handshake`
and signs an ES384 JWT for every request.

The server verifies with the stored certificate, not with the CA, so a client paired
under the old server authenticates without renewal. The CA only signs new certificates.
Root and leaf certificates use the same distinguished name shape and key usage as the
Rust implementation, so bundles are interchangeable.

### 5.4 Pairing

The code flow stays:

1. Desktop calls `POST /api/v1/client/auth/initiate` with its name and capabilities.
   It receives a short code, or a URL to `/client/authorize/<clientId>`.
2. An admin opens `/dashboard/clients`, enters the code, and confirms. That binds the
   pending client to a `User` and issues the certificate.
3. The desktop holds a WebSocket at `/api/v1/client/auth/code/ws`, receives the token
   over it, then calls `POST /api/v1/client/auth/handshake` for the certificate bundle.

The dashboard replaces the two Nuxt pages that used to do step 2 (`pages/client/code`
and `pages/client/authorize`). Existing clients see no change.

## 6. Data model

37 models and 12 enums, frozen from the upstream Prisma schema. Table and column names,
enum labels and array columns are unchanged. The Go code names tables in SQL, not
through an ORM, so nothing depends on Prisma naming at runtime.

| Group | Models |
| --- | --- |
| Identity | `User`, `LinkedAuthMec`, `LinkedMFAMec`, `APIToken`, `Session`, `Invitation`, `UserGroup` |
| Clients | `Client`, `Certificate` |
| Catalog | `Game`, `GameVersion`, `Company`, `GameTag`, `GameAgeRating`, `BannedAgeRating`, `GameRating`, `LaunchConfiguration`, `Collection`, `CollectionEntry`, `Screenshot`, `Article`, `NewsTag` |
| Library | `Library`, `UnimportedGameVersion`, `Depot` |
| Content | `ObjectHash`, `Task` |
| Player | `Playtime`, `Profile`, `SaveManifest`, `SaveSlot`, `SaveVersion` |
| Stores | `StoreConnection`, `StoreManifest` |
| System | `ApplicationSettings`, `SetupConfiguration` |

Enums: `Platform`, `GameType`, `LibraryBackend`, `MetadataSource`, `AuthMec`, `MFAMec`,
`APITokenMode`, `ClientCapabilities`, `StoreProvider`, `SyncStatus`, `SaveVersionKind`,
`AgeRatingOrganization`.

The baseline is generated once from the 131 Prisma migrations into `sql/schema.sql`.
The goose baseline records a version without executing DDL when the tables already
exist. A fresh database gets the same DDL Prisma would have produced; an existing
database is untouched.

### 6.1 Query layer

sqlc generates typed Go from hand-written SQL in `sql/queries`. Listing endpoints, the
store query, save manifest assembly and the library scan are the expensive paths and are
written as explicit SQL. Transactions are opened through a small helper so handlers do
not manage `pgx.Tx` by hand.

## 7. Object store

Small immutable blobs (images, certificates, icons) live under `${DATA}/objects` and are
served at `/api/v1/object/:id`.

- Content type is detected on upload and stored in `ObjectHash`.
- ETags are the content hash. `If-None-Match` returns 304.
- Permissions are `id:permission` strings such as `anonymous:read` or `<userId>:write`,
  stored in `ObjectHash.permissions`.
- Reads stream from disk. Writes go to a temporary file, fsync, then rename, so a
  partial upload is never visible.
- Replacing an object overwrites the same id. Cover images, avatars and logos are single
  objects per owner.

## 8. Library

A library source is a directory on disk. The provider decides whether a path is a
version:

- **Filesystem provider**: the path is a directory tree. Files are walked, symlinks are
  not followed out of the tree, and permission bits are recorded.
- **Flat provider**: the path is a single archive. Phase 4 lists the files inside an
  archive without extracting it. Zip is available in the Go standard library; 7z, rar
  and iso need a fallback.
- **Archive-backed versions** (zip, 7z, iso) are a later roadmap item. Directory
  versions come first.

Import produces a `GameVersion` row with a droplet manifest, a `fileList`, and
optionally a `negativeFileList` for delta versions. A delta version references the
version below it by `versionIndex`; the download manifest is assembled by applying file
lists in ascending order, newest last.

`createDownloadManifestDetails(versionId, previous, refresh)` keeps its behaviour:
it returns `manifests` keyed by version id, `fileList` mapping each filename to the
version that supplies it, `installSize` and `downloadSize`. When `previous` is given,
chunks already present in the previous install are excluded and counted out of the
download size.

## 9. Depot formats

Two formats coexist. Both are compatibility contracts with the desktop client.

### 9.1 Droplet v2 (library versions)

Generated from a version directory by the server, consumed by `droplet-rs` in the
desktop.

```text
Manifest
  version : string            "2"
  size    : u64              total plaintext bytes
  key     : [16]byte         random AES-128 key for the release
  chunks  : map<uuid, Chunk>

Chunk
  checksum  : string         SHA-256 hex over the plaintext chunk
  iv        : [16]byte       random per chunk
  files     : [FileEntry]

FileEntry
  filename    : string       relative, posix separators
  start       : usize        offset inside the decrypted chunk
  length      : usize        file length inside the chunk
  permissions : u32          mode bits
```

Generation rules:

- Files are sorted by descending size, then packed into chunks of at most 64 MiB.
- A chunk holds at most 512 file entries.
- A file larger than 64 MiB is split across chunks; each slice becomes its own entry.
- The chunk is encrypted as a whole with AES-128-CTR using the release key and the
  chunk IV, with a **64-bit little-endian counter**. This differs from Go's
  `cipher.NewCTR`, which uses a big-endian counter across the whole block. The Go
  implementation is a small counter mode built on `cipher.Block.Encrypt`, and it is
  tested against `libraries/droplet` output.
- The checksum is computed over the plaintext, before encryption.

Because the desktop decrypts with `ctr::Ctr64LE<Aes128>`, a byte-level mismatch would
break every download. Equivalence tests are a phase 4 exit criterion.

### 9.2 CAS (store ingestion, new releases)

The content-addressed format used by ingestion, and the one new releases should
prefer. Files are split into fixed 4 MiB chunks.

```text
Layout under ${DATA}/depot
  objects/<first two hex>/<sha256>          raw chunk, immutable
  manifests/<digest>.json                   canonical manifest, immutable

Manifest (canonical JSON)
  format     : "gamedepot-droplet"
  version    : 1
  files      : [{ path, size, sha256, mode, chunks: [{index, offset, size, sha256, object}] }]
  chunkIndex : { <sha256>: { object, size } }
  digest     : SHA-256 hex of the canonical JSON of everything above
```

Rules:

- `digest` is computed over the canonical form with `digest` removed. Canonical means
  sorted object keys, no insignificant whitespace, and no trailing newline in the
  hashed bytes. Go needs its own canonical encoder, since `json-canonicalize` is a Node
  package. The output is verified against manifests produced by the old server.
- Paths are normalized to NFC, use `/`, and must be relative. Empty segments, `.`,
  `..`, leading `/`, backslashes and NUL are rejected. Duplicate paths are rejected.
- A chunk object is written to a temp file, fsynced, then renamed into place. If the
  object already exists, the rename is skipped. A conflicting chunk with the same hash
  but different size or path is a hard error.
- Manifests are written once and never edited. The file name is the digest.
- A tree with more than 200,000 files is refused rather than run indefinitely.

### 9.3 Serving

Chunk reads are `io.Copy` from an open file or from the chunk slice inside a droplet
chunk file, with `http.ServeContent` for range and `If-Range` where the object is a
plain file. For droplet chunks, the server decrypts only the requested file range: it
seeks to the chunk offset, applies the counter at the right block, and streams the
requested bytes. Range requests are honoured for CAS objects directly.

## 10. Certificate authority

Pure Go `crypto/x509`, no external process.

- Root: self-signed, `CommonName=Drop Root Server`, `Organization=Drop`, is CA with
  unconstrained basic constraints, key usages CRL sign, cert sign and digital signature.
  Stored once under object key `ca` as `{priv, cert}` PEM.
- Leaf: ECDSA P-384, issued per client id, `CommonName` set to the client name.
- The server's own certificate (`server`, "Drop Server") is created at first boot so the
  root always has at least one leaf.
- Verification of a client request uses the leaf certificate in `Certificate`, checking
  the signature over the JWT header and payload. Revocation is deleting the `Certificate`
  row and the `Client` row, which invalidates both the certificate and the JWT path.

## 11. Tasks and notifications

Long operations (manifest generation, ingestion, publish, integrity checks) run in a task
registry, not in the request goroutine.

- A task has an id, a key that deduplicates concurrent duplicates, an owner, ACLs, a
  status, a progress fraction and a log buffer.
- Progress is published to an in-process hub. The desktop gets it over the existing
  `/api/v1/task` WebSocket with the text protocol: `connect/<taskId>`, then JSON
  messages, with `unauthenticated` sent on a missing credential.
- The dashboard gets it by polling an HTML fragment every second with HTMX. No browser
  WebSocket is added.
- The notification feed at `/api/v1/notifications/ws` keeps its payload shape. It is
  used by the desktop for admin announcements.
- Registry state is process-local. Horizontal scaling would need a shared bus; that is
  out of scope and documented as a known limit.

## 12. Dashboard rendering

- `html/template` with a layout, a navigation partial, and page templates.
- HTMX is vendored as a single minified file under `backend/web/static`; no CDN at
  runtime.
- Tailwind is compiled ahead of time with the standalone CLI into
  `backend/web/static/app.css` and embedded with `go:embed`. The server never runs
  Node.
- Design tokens follow shadcn's vocabulary (`background`, `foreground`, `card`,
  `border`, `muted`, `primary`, `destructive`), so the dashboard and the desktop client
  look like one product.
- Forms use POST plus redirect (PRG), with CSRF tokens. Inline actions return fragments.
- Errors render a page with the request id, not a stack trace. Logs carry the same id.

## 13. Desktop client

The Rust core is kept. The UI moves off Nuxt.

- Vue 3, Vite, TypeScript, `vue-router`.
- `unplugin-vue-router` keeps file-based routing over `pages/`.
- `unplugin-auto-import` and `unplugin-vue-components` keep the Nuxt-style auto-imports.
- `NuxtLink` is replaced by a shim that renders `RouterLink`.
- `useState` becomes VueUse `createGlobalState` stores.
- shadcn-vue on Reka UI supplies components; `@heroicons/vue` stays until a later icon
  pass.
- Tailwind v4, with the existing `ink` and `accent` scales mapped to shadcn variables.
  Both themes are kept, since the client ships a theme picker.
- Tauri commands and the Rust modules (`client`, `download_manager`, `games`, `importer`,
  `process`, `remote`, `cloud_saves`, `database`, `tailscale`) are unchanged in phase 1.

`desktop/main` currently uses Nuxt APIs in 29 of its 50 files, but only lightly: 9
`definePageMeta`, 20 `NuxtLink`, 12 `useState`, and a handful of router and composable
calls. That is why the migration is a set of shims rather than a rewrite.

## 14. Configuration

Environment only, no config file. Defaults are chosen so a development instance starts
without a `.env`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | required | PostgreSQL connection string |
| `PORT` | `4000` | HTTP listen port |
| `EXTERNAL_URL` | `http://localhost:4000` | Public origin, used for depot URL and links |
| `LIBRARY` | `./.data/library` | Library volume |
| `DATA` | `./.data/data` | Objects, depot, certificates |
| `GAMEDEPOT_CREDENTIAL_KEY` | required for store features | 32 byte key sealing stored credentials |
| `GAMEDEPOT_STORE` | `false` | Enables store ingestion surfaces |
| `METADATA_TIMEOUT` | `5000` | Metadata provider timeout in ms |
| `CHECK_FOR_UPDATES` | `true` in production | Update check |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `OIDC_*` | unset | Only if OIDC returns in phase 7 |

Validation happens once at boot. A missing `DATABASE_URL` or an unwritable `DATA`
directory is fatal with a specific message, not a panic. `EXTERNAL_URL` is normalized
(trailing slash removed, scheme required) so string concatenation for the depot URL is
safe.

## 15. Error model

- JSON handlers return `{"statusCode": n, "statusMessage": "...", "message": "..."}`
  for any 4xx, matching the shape the desktop already parses.
- Validation failures are 400 with a field summary. Missing authentication is 401.
  Failed authorization is 403. Missing rows are 404. Conflicts are 409.
- Internal errors log with the request id and the stack-equivalent fields, and return a
  generic message. No database error text reaches a client.
- Dashboard handlers render an error template with the same status codes, plus a flash
  message for form-level failures.

## 16. Concurrency and limits

- pgx pool sized from `GOMAXPROCS`, with a floor and a ceiling, and a statement timeout.
- Manifest generation uses a worker pool bounded by CPU count and disk throughput. Each
  chunk is hashed and encrypted independently.
- Ingest jobs are serialized per connection and per game through task keys, so a double
  click cannot start two downloads of the same build.
- Uploads stream to disk with a size cap. No request body is fully buffered in memory.
- WebSocket connections have read and write deadlines and a ping interval.
- Every long loop checks `context.Context` for cancellation, including chunking, publish
  and ingestion.

## 17. Testing

- **Unit**: format codecs, canonical JSON, path safety, ACL resolution, config parsing.
- **Format equivalence**: droplet v2 manifests and CAS manifests produced by Go are
  compared byte for byte with fixtures generated by the old implementation. This is the
  highest-value test in the project, because a mismatch breaks clients silently.
- **Handler**: table tests against a seeded PostgreSQL, using a compose service or
  testcontainers.
- **Contract**: replay the desktop request set against an upstream server and the Go
  server, diff the responses. Run in phase 5 and nightly afterwards.
- **End to end**: setup, invite, sign in, pair, import, download, play, save.
- **No network in unit tests.** Provider adapters take an injected HTTP client and are
  tested against recorded responses.

## 18. Deployment

- One image: the Go binary, embedded dashboard assets, the baseline SQL.
- Two volumes: `${LIBRARY}` and `${DATA}`. Both must be backed up together, because
  depot manifests reference objects by hash.
- PostgreSQL is external or a compose service, not embedded.
- The process handles `SIGTERM` by stopping listeners, waiting for in-flight requests
  with a bounded grace period, then closing the pool.
- Health: `GET /api/v1/depot/healthcheck` for liveness, plus a readiness check that
  pings the database.
- Logs are structured JSON on stdout, with a human-readable mode for development.

## 19. Known limits

- Task and notification state is process-local; the server is single instance by design.
- Archive-backed library versions are not in the first milestone.
- Ubisoft and EA adapters depend on undocumented endpoints; they are the least verifiable
  part of the port and are tested with recorded traffic only.
- The CAS canonical encoder is ours. If it ever disagrees with the Node implementation,
  manifest digests change, so the fixtures are checked in and treated as goldens.
