# Architecture

How GameDepot is built: processes, modules, schema, formats and the rules that keep
them coherent. Everything here is defined by this project. There is no external
contract to satisfy.

Status: design target. No code exists yet.

## 1. Principles

1. **One server process.** HTTP API, dashboard and depot run in one Go binary.
   PostgreSQL is the only external service.
2. **Content addressed.** Every release object is named by the SHA-256 of its plaintext.
   Identity and integrity are the same thing.
3. **Verify before use.** A client hashes ciphertext, decrypts, hashes plaintext, and
   only then writes a file.
4. **Server-rendered administration.** The dashboard is HTML, not a second application.
5. **Small client contract.** The desktop calls a narrow API with one credential type.
6. **Fail closed.** Missing keys, bad hashes, unknown paths and expired tokens are
   errors, not warnings.

## 2. Runtime topology

```text
                     ┌───────────────────────────────┐
  Desktop client     │  Go server (Gin)              │
  Tauri + Rust  ─────┤  /api/v1/*          JSON      │
  Bearer token       │  /api/v1/depot/*    objects   │
                     │  /dashboard/*       HTML      │
  Browser        ─────┤  /setup, /signin   HTMX      │
  session cookie     └───────────────┬───────────────┘
                                     │
                         ┌───────────┴───────────┐
                         │  PostgreSQL           │
                         └───────────────────────┘
                         ┌───────────┴───────────┐
                         │  Filesystem           │
                         │  DATA: objects, depot │
                         │  LIBRARY: sources     │
                         └───────────────────────┘
```

TLS terminates at the operator's proxy. The server listens on plain HTTP.

## 3. Repository layout

```text
backend/
  cmd/server/main.go        boot, config, pool, migrations, HTTP, shutdown
  internal/
    api/                    route registration, DTOs, error mapping
      client/               desktop endpoints
      depot/                manifest and object reads
      dashboard/            HTML handlers and fragments
      auth/                 setup, sign-in, sign-out, invitations, pairing
    auth/                   passwords, sessions, tokens, pairing
    acl/                    roles and scopes
    catalog/                games, companies, tags, versions, collections
    objects/                small immutable blobs
    library/                source scan, version detection, release build
    depot/                  chunking, encryption, manifests, serving
    tasks/                  task registry and progress
    config/                 environment parsing and validation
    database/               pgx pool, transactions
    web/                    templates, static assets, embedded Tailwind
  sql/
    migrations/             goose migrations, numbered
    queries/                sqlc inputs
  sqlc.yaml
desktop/
  src/                      Vue 3 application
  src-tauri/                Rust core and Tauri configuration
  index.html
  vite.config.ts
  package.json
deploy/
  Dockerfile
  compose.yaml
docs/
```

## 4. HTTP surface

Three groups, separated by consumer and credential.

### 4.1 Client API (`/api/v1/*`)

JSON, consumed by the desktop client. Authenticated with
`Authorization: Bearer <client token>`. The token is 32 random bytes, issued once at
pairing, stored as a SHA-256 hash, and revocable per client.

The full contract is in [`docs/CLIENT-API.md`](docs/CLIENT-API.md).

```text
POST /api/v1/pairing/initiate
POST /api/v1/pairing/claim
GET  /api/v1/me
GET  /api/v1/library
GET  /api/v1/games/{id}
GET  /api/v1/games/{id}/versions
GET  /api/v1/releases/{versionId}
GET  /api/v1/depot/manifests/{digest}
GET  /api/v1/depot/objects/{sha256}
GET  /api/v1/playtime
POST /api/v1/playtime
GET  /api/v1/collections
POST /api/v1/collections
DELETE /api/v1/collections/{id}
GET  /api/v1/settings
GET  /api/v1/health
```

### 4.2 Dashboard (`/dashboard/*`, `/setup`, `/signin`)

HTML, consumed by a browser. Session cookie `gamedepot_session`, `HttpOnly`,
`SameSite=Lax`, `Secure` when the external URL is HTTPS. Every state-changing form
carries a CSRF token bound to the session and compared in constant time.

```text
GET  /setup, POST /setup                     first run, setup token
GET  /signin, POST /signin
POST /signout
GET  /dashboard                              overview
GET  /dashboard/games, /games/{id}
GET  /dashboard/companies, /tags
GET  /dashboard/library, /library/sources
GET  /dashboard/releases, /releases/{id}
GET  /dashboard/clients
GET  /dashboard/users, /users/{id}, /users/invitations
GET  /dashboard/tasks, /tasks/{id}
GET  /dashboard/settings
```

Lists and inline actions return fragments under `/dashboard/_/*` for HTMX.

### 4.3 Depot (`/api/v1/depot/*`)

Object and manifest reads for authenticated clients. No separate port and no proxy.
The manifest contains the release key, so it is only served to a client with access to
the game. Objects are ciphertext and useless without that key.

## 5. Authentication and authorization

### 5.1 Dashboard sessions

- Sign-in matches `users.username` and verifies an Argon2id hash.
- `sessions` stores a SHA-256 hash of the cookie token, not the token itself, plus a
  CSRF secret, user agent, IP and expiry.
- Cookie: 32 random bytes, base64url. Session lifetime 30 days, 90 with remember me.
- CSRF: 32 random bytes per session, rendered into each form, rotated on sign-in.
- Sign-out deletes the row. A dashboard page lists sessions and revokes them.
- Failed sign-ins are rate limited per IP and per username.

### 5.2 First run

- On boot with no users and no completed setup, the server creates a setup token, stores
  its hash in `settings`, and prints the token once at `info` level.
- `POST /setup` accepts the token, creates the admin, deletes the setting and signs the
  user in.
- While setup is incomplete, every other route except health returns 503 with a link to
  `/setup`.

### 5.3 Invitations

- An admin creates an invitation. The server returns a single-use code once and stores
  its hash.
- Signup requires a valid, unused, unexpired invitation.
- Redemption creates the user and marks the invitation used in one transaction.

### 5.4 Device pairing

1. Desktop calls `POST /api/v1/pairing/initiate` with a device name and platform. It
   receives an eight-character code (Crockford base32, uppercase) valid for ten minutes.
2. The user opens `/dashboard/clients`, enters the code, and approves. Approval binds
   the pending code to the signed-in user.
3. Desktop calls `POST /api/v1/pairing/claim` with the code. On success it receives
   `{clientId, token}` exactly once. The code is then consumed.
4. The token is stored hashed in `clients`. Every client request presents it as a
   Bearer token.

Codes are rate limited, single use, and expire. A claimed token can be revoked from the
dashboard, which takes effect on the next request.

### 5.5 Scopes

Client tokens carry a scope list, fixed at pairing:

| Scope | Grants |
| --- | --- |
| `library:read` | Me, library, games, versions, releases, manifests, objects |
| `playtime:write` | Posting playtime |
| `collections:write` | Creating and deleting collections |
| `saves:read`, `saves:write` | Cloud saves, phase 7 |

A route requires a scope and returns 403 when it is missing. Admin routes are not
reachable with a client token at all.

## 6. Data model

The schema is owned by this project and evolves with goose migrations. No compatibility
with any other installation is promised.

| Group | Tables |
| --- | --- |
| Identity | `users`, `sessions`, `invitations`, `api_tokens` |
| Clients | `clients`, `pairing_codes` |
| Catalog | `games`, `companies`, `game_companies`, `tags`, `game_tags`, `game_versions`, `launches`, `collections`, `collection_entries` |
| Content | `objects`, `releases`, `libraries` |
| Player | `playtime`, `saves`, `save_versions` (phase 7) |
| System | `tasks`, `settings`, `store_connections` (phase 7), `runner_recipes` |

Conventions:

- Primary keys are UUIDv7, generated by the application.
- Timestamps are `timestamptz`, stored in UTC.
- Enums are PostgreSQL enum types or checked text. Small, stable sets use enum types.
- JSON columns are `jsonb` and validated in Go before write.
- Every foreign key has an index. Every list query has an index that covers its sort.

Migrations run from a single `migrations` command and on boot when
`GAMEDEPOT_AUTO_MIGRATE=true`. A failed migration stops the process; there is no
half-applied boot.

### 6.1 Queries

sqlc generates Go from hand-written SQL in `sql/queries`. Handlers never build SQL by
string concatenation. Transactions use a helper that takes a function and commits only
on success.

## 7. Object store

Small immutable blobs: covers, icons, avatars. Stored under `${DATA}/objects` and served
from `/api/v1/objects/{id}`.

- Rows in `objects` carry id, SHA-256, size and content type.
- The file is named by the SHA-256, so identical uploads deduplicate.
- `ETag` is the hash. `If-None-Match` returns 304.
- Reads stream from disk. Writes go to a temp file, fsync, then rename.
- Access is checked against the row that references the object (game cover, user
  avatar), not by a per-object permission list.

Release objects are separate: they live in the depot store described in section 9.

## 8. Library and releases

A library is a directory on disk.

1. An admin adds a source with a name and path.
2. A scan lists subdirectories that look like versions: a directory with files, no
   symlinks escaping the source.
3. The admin maps a directory to a game and a version name, selects a platform, and
   starts a build.
4. The build walks the tree, records relative path, size and mode, and chunks each file.
5. The release is published atomically and the version row points at its digest.

Symlinks are not followed outside the source root. Hidden files are included unless
the source opts out. A tree with more than 200,000 files or 4 TiB is refused.

## 9. Depot format (`gamedepot-release` v1)

The only release format. It is defined here and nowhere else.

### 9.1 Chunking

- Fixed 4 MiB plaintext chunks (`4194304` bytes), last chunk shorter.
- Chunk identity is the SHA-256 of the plaintext, lowercase hex.
- Identical chunks deduplicate within a release.

### 9.2 Encryption

- One random 32-byte key per release, base64 in the manifest.
- One random 12-byte nonce per object, base64 in the manifest.
- AES-256-GCM. Ciphertext is `plaintext || 16-byte tag`.
- Additional authenticated data is the ASCII string `gamedepot-release:v1:` followed by
  the object's lowercase hex SHA-256. This binds a ciphertext to its identity.
- Because object identity is the plaintext hash and the release key is unique, a
  (key, nonce) pair is never reused with different plaintext. Reusing the same plaintext
  reuses the same object and the same nonce, which is safe.

### 9.3 Manifest

Canonical JSON: UTF-8, object keys sorted lexicographically, no insignificant
whitespace, no trailing newline in the hashed bytes.

```json
{
  "format": "gamedepot-release",
  "version": 1,
  "chunkSize": 4194304,
  "totalSize": 123456,
  "key": "<base64, 32 bytes>",
  "files": [
    {
      "path": "bin/game",
      "size": 123456,
      "sha256": "<hex>",
      "mode": 493,
      "chunks": [
        { "sha256": "<hex>", "offset": 0, "size": 123456 }
      ]
    }
  ],
  "objects": {
    "<hex>": { "size": 4194304, "nonce": "<base64, 12 bytes>" }
  },
  "digest": "<hex>"
}
```

Rules:

- `path` is relative, uses `/`, is NFC normalized, and must not contain `..`, a leading
  `/`, a backslash, NUL or an empty segment. Duplicates are rejected.
- `mode` is `mode & 0o777`.
- `chunks[].offset` is the offset inside the file, `size` is the plaintext length of
  that slice.
- `objects[].size` is the plaintext size. The stored file is `size + 16` bytes.
- `digest` is the SHA-256 of the canonical JSON of the manifest with `digest` removed.
- A manifest is written once and never edited.

### 9.4 Layout

```text
${DATA}/depot
  objects/<first two hex>/<sha256>     ciphertext, immutable
  manifests/<digest>.json              canonical manifest, immutable
```

Writes use a temp file in the same directory, fsync, then rename. If the object exists,
the write is skipped. A manifest is written last, after every object it references is
present and fsynced.

## 10. Depot serving

- `GET /api/v1/depot/manifests/{digest}` returns the manifest JSON. The caller must have
  access to the game that owns the release.
- `GET /api/v1/depot/objects/{sha256}` returns ciphertext bytes. Access is checked by
  looking up the object's release and the game.
- No range support in v1. Objects are 4 MiB, so resume happens at object granularity,
  which keeps the server simple and the client state small.
- Object reads stream from disk with `io.Copy` and never buffer a whole object in
  memory.
- `GET /api/v1/depot/healthcheck` returns 200 when the depot root is readable.
- Responses set `Cache-Control: public, max-age=31536000, immutable` for objects, since
  the name is the hash. The dashboard is never cached.

Deleting a release removes its manifest and any object no other manifest references.
Reference counting walks the manifests that remain; it does not trust a counter column.

## 11. Tasks

Long operations run in a registry, not in the request goroutine: release build, publish,
integrity check, cleanup, store ingestion.

- A task has an id, kind, state, progress fraction, log buffer and owner.
- Progress is kept in memory and mirrored to `tasks` for the list view.
- The dashboard polls `/dashboard/_/tasks/{id}` every second with HTMX.
- The desktop does not need task progress in v1; its downloads are client-side.
- Cancellation is a context cancel propagated to the worker.
- One task per kind and target at a time; a duplicate request returns the running task.

## 12. Dashboard

- `html/template` with a layout, navigation and page templates.
- HTMX vendored under `backend/web/static/htmx.min.js`. No CDN at runtime.
- Tailwind compiled ahead of time with the standalone CLI into
  `backend/web/static/app.css` and embedded with `go:embed`.
- Design tokens follow shadcn's vocabulary (`background`, `foreground`, `card`,
  `border`, `muted`, `primary`, `destructive`), matching the desktop client.
- Forms use POST plus redirect. Inline actions return fragments.
- Errors render a page with the request id. Logs carry the same id.

## 13. Desktop client

Tauri 2 with a Rust core, written for this project.

- UI: Vue 3, Vite, TypeScript, vue-router, shadcn-vue on Reka UI, Tailwind v4.
- Rust modules:
  - `api`: HTTP client, token injection, error mapping.
  - `auth`: pairing flow, keyring storage, sign-out.
  - `download`: worker pool, per-object fetch, verify, decrypt, temp write, resume.
  - `install`: assemble files, modes, atomic rename, local install state in SQLite.
  - `launch`: spawn, environment, working directory, exit status.
  - `settings`: server URL, download directory, concurrency, theme.
- Token storage uses the OS keyring. The SQLite database holds install state only, never
  credentials.
- Download concurrency defaults to four objects and is configurable.
- Every release declares a launch posture, `direct` or `store_client`, in its runner
  recipe. `store_client` means the provider's own storefront starts the game, so
  entitlement stays the provider's answer; this project never stubs or replaces a
  provider loader (ADR-0012).
- A partial install is never launched: the version is marked ready only after every file
  verifies.

## 14. Configuration

Environment only. Defaults let a development instance start without a `.env`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | required | PostgreSQL connection string |
| `PORT` | `4000` | HTTP listen port |
| `EXTERNAL_URL` | `http://localhost:4000` | Public origin, used for links and cookies |
| `LIBRARY` | `./.data/library` | Library volume |
| `DATA` | `./.data/data` | Objects, depot, manifests |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `GAMEDEPOT_AUTO_MIGRATE` | `true` in development | Apply migrations on boot |
| `GAMEDEPOT_CREDENTIAL_KEY` | unset | 32-byte key for store credentials, phase 7 |

Validation happens once at boot. A missing `DATABASE_URL`, an unwritable `DATA`
directory or a malformed `EXTERNAL_URL` is fatal with a specific message.

## 15. Errors

- JSON errors: `{"error":{"code":"not_found","message":"Game not found"}}`.
- Status codes: 400 malformed, 401 unauthenticated, 403 forbidden, 404 missing,
  409 conflict, 422 validation, 429 rate limited, 500 internal.
- Validation errors include a `fields` object keyed by field name.
- Internal errors log detail with the request id and return a generic message.
- Dashboard errors render a template with the status code and a flash message.

## 16. Concurrency and limits

- pgx pool sized from `GOMAXPROCS`, with a floor, a ceiling and a statement timeout.
- Release builds use a worker pool bounded by CPU count and disk throughput.
- Uploads and object writes stream to disk with a size cap; no request body is fully
  buffered.
- Every long loop takes a `context.Context` and checks it.
- HTTP servers set read, write and idle timeouts.
- Download tokens are validated before any file is opened.

## 17. Testing

- Unit tests for chunking, AES-GCM framing, canonical JSON, path safety, tokens,
  sessions and config.
- Golden fixtures for the manifest encoder and decoder, including a manifest with
  duplicate chunks and an empty file.
- Handler tests against a seeded PostgreSQL in compose or testcontainers.
- End to end: setup, invite, sign in, pair, build a release, download, install, launch,
  playtime.
- No network in unit tests. Provider adapters take an injected HTTP client and use
  recorded responses.

## 18. Deployment

- One image: Go binary, embedded dashboard assets, migrations.
- Two volumes: `LIBRARY` and `DATA`. Back them up together; manifests reference objects
  by hash.
- PostgreSQL external or a compose service.
- `SIGTERM` stops listeners, waits for in-flight requests with a bounded grace period,
  then closes the pool.
- Liveness: `/api/v1/health`. Readiness: health plus a database ping.
- Logs are JSON on stdout by default, human-readable in development.

## 19. Known limits

- Single instance only. Tasks and progress are process-local.
- No range requests in v1; object granularity is the resume unit.
- Launch posture, runner selection and store-client handoff are specified in ADR-0012 and
  mostly not built: the desktop discovers umu and Proton-GE builds and can address a
  prefix, but it does not read the recipe endpoint, no admin can create a recipe without
  SQL, and handoff's process tracking does not exist. Emulator helpers remain v2.
- Store ingestion and cloud saves arrive in phase 7.
- Windows and Linux are the first desktop targets. macOS is not tested.
