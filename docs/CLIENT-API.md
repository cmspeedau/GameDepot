# Client API reference

The HTTP contract between the desktop client and the server. It is a compatibility
surface: paired clients already speak it, so field names, status codes and error bodies
are fixed.

Everything here describes the target server. None of it is implemented yet outside the
fork parent.

## Conventions

- Base URL: the origin the user configured in the client, e.g. `https://games.example`.
- All paths below are relative to that origin.
- JSON in, JSON out, unless noted. Requests send `Content-Type: application/json`.
- Timestamps are ISO 8601 with milliseconds and a `Z` suffix, as the Node server emitted
  them.
- Ids are cuid strings for catalog rows, UUIDs for clients, profiles and sessions.
- Authentication is per request, no session cookie is used.

### Error shape

Any non-2xx response carries:

```json
{
  "statusCode": 403,
  "statusMessage": "Invalid client ID",
  "message": "Invalid client ID"
}
```

The desktop reads `statusCode` for control flow and shows `statusMessage` or `message`
to the user. A bare status with no JSON body is also possible for auth failures that
happen before the handler runs. For compatibility, auth failures return 403 rather than
401.

### Profile selector

Routes scoped to a player resolve the profile in this order:

1. `?profile=<uuid>` query parameter.
2. `x-profile-id: <uuid>` header.
3. The account's default profile.

A selector that is not a UUID is a 400. A UUID that belongs to another account is a 404,
never a cross-account read. The identity is always the token's user.

### Capabilities

A client declares capabilities at pairing time. Routes that need one check it and return
403 `Capability not allowed.` when absent.

| Capability | Grants |
| --- | --- |
| `trackPlaytime` | `GET/POST /playtime*` |
| `cloudSaves` | `GET /saves*`, `GET /saves/manifests/{gameId}` |
| `userStatus` | Presence reporting |
| `peerAPI` | Reserved, disabled in the current server |

The stored values are the database enum labels: `trackPlaytime`, `cloudSaves`,
`userStatus`, `peerAPI`.

## Pairing

### 1. Initiate

```http
POST /api/v1/client/auth/initiate
Content-Type: application/json

{
  "name": "Living room deck",
  "platform": "linux",
  "capabilities": { "trackPlaytime": {}, "cloudSaves": {} },
  "mode": "code"
}
```

`mode` is `code` or `callback`, default `callback`. `platform` is parsed to the
`Platform` enum and rejects unknown values with 400. Unknown capability names are 400.

Response for `code`:

```json
"AB12CD3"
```

A seven character uppercase code, valid for ten minutes, single use. Response for
`callback`:

```json
"/client/authorize/<clientId>"
```

The client opens this path in a browser when the user prefers a logged-in approval.

### 2. Listen for approval

```text
GET /api/v1/client/auth/code/ws
Upgrade: websocket
Authorization: AB12CD3
```

The pairing code goes in the `Authorization` header, not a cookie. Server messages:

```json
{ "type": "auth", "value": "<token>" }
{ "type": "error", "value": "Invalid or unknown code." }
```

After `auth`, the socket may close.

### 3. Approve

Done in the dashboard, or over the two web-session routes that remain for the callback
mode. In the new server this is a dashboard form, not a JSON API:

```text
GET  /dashboard/clients           enter a code
POST /dashboard/clients/approve   bind client to the signed-in admin, issue token
```

Old browser-session equivalents kept only if the callback mode is retained:

```text
GET  /api/v1/client/auth/code?code=AB12CD3     -> clientId
POST /api/v1/client/auth/code { "id": ... }    -> sends token over the socket
POST /api/v1/client/auth/callback { "id": ... } -> { redirect, token }
```

The callback response shape is `{ "redirect": "drop://handshake/<clientId>/<token>",
"token": "<clientId>/<token>" }`.

### 4. Handshake

```http
POST /api/v1/client/auth/handshake
Content-Type: application/json

{ "clientId": "<uuid>", "token": "<auth token>" }
```

Response:

```json
{
  "private": "-----BEGIN PRIVATE KEY-----\n...",
  "certificate": "-----BEGIN CERTIFICATE-----\n...",
  "id": "<client uuid>"
}
```

The client stores both PEM blocks and signs a JWT with the private key for each later
request. The certificate is ECDSA P-384. Handshake failures: 400 when the body is
incomplete, 403 for an unknown client or a token mismatch, 400 if the client is not yet
authorized.

### Authenticated requests

```text
Authorization: JWT <clientId> <jwt>
```

The JWT is ES384. Claims carry the nonce and expiry. The server verifies against the
stored certificate for that client id, with 30 seconds of clock tolerance. An unknown
client id, a missing certificate, a bad signature or an expired token all produce 403.

## Account and library

### `GET /api/v1/client/user`

Returns the `User` row for the token, minus credentials.

### `GET /api/v1/client/user/library`

Returns the games the user can install, already filtered by age restrictions:

```json
[ { "id": "clx...", "mName": "Game", "mShortDescription": "...", "mIconObjectId": "..." } ]
```

This is an array of game rows, not an envelope. The desktop calls it `fetch_library`.

### `GET /api/v1`

Server identity:

```json
{
  "appName": "Drop",
  "version": "v0.4.0",
  "gitRef": "#abc1234",
  "external": "https://games.example",
  "serverName": "Home",
  "mLogoObjectId": null
}
```

`appName` stays `Drop` for client compatibility. It is not a branding switch.

### `GET /api/v1/settings`

Application settings needed by the client. Requires the `settings:read` ACL, which a
paired client inherits from its user.

## Catalog

### `GET /api/v1/client/game/{id}`

Full `Game` row. 404 when missing.

### `GET /api/v1/client/game/{id}/versions?previous=<versionId>`

One entry per platform a version can run on:

```json
[
  {
    "gameId": "clx...",
    "versionId": "clx...",
    "displayName": "1.2.0",
    "versionPath": null,
    "runsStandalone": true,
    "platform": "Windows",
    "size": { "downloadSize": 123, "installSize": 456 },
    "requiredContent": []
  }
]
```

`requiredContent` lists emulator or dependency versions with the same size shape. Sizes
honour `previous`, so the download size excludes chunks already installed.

### `GET /api/v1/client/game/{id}/version/{versionId}`

The version row with its launch configurations and setups, expanded enough for the client
to build a launch command.

### `GET /api/v1/client/depots`

```json
[ { "endpoint": "https://games.example/api/v1/depot/" } ]
```

The client joins `manifest.json`, `speedtest` and `content/...` onto this value.

## Download manifests

### `GET /api/v1/client/game/manifest?version=<versionId>&previous=<versionId>&refresh=true`

Returns:

```json
{
  "manifests": { "<versionId>": { "version": "2", "size": 0, "key": [], "chunks": {} } },
  "fileList": { "bin/game": "<versionId>" },
  "installSize": 0,
  "downloadSize": 0
}
```

- `manifests` is keyed by version id and holds droplet v2 manifests. A delta chain
  produces more than one.
- `fileList` maps each logical file path to the version that supplies it.
- `previous` excludes chunks already present in that install and reduces `downloadSize`.
- `refresh=true` bypasses the in-process cache.

The manifest format is described in `ARCHITECTURE.md` section 9.1.

## Depot data plane

### `GET /api/v1/depot/manifest.json`

Depot capability document. Advertises available formats and the health of the store.

### `GET /api/v1/depot/speedtest`

50 MiB of zeros, used to estimate throughput before a download starts.

### `GET /api/v1/depot/content/{game}/{version}/{chunk}`

Raw encrypted chunk. The `chunk` segment is the chunk UUID from the droplet manifest.
The server reads only the requested range when a `Range` header is present, so a client
resuming a download fetches exactly the missing bytes.

### `POST /api/v1/depot/invalidate`

```json
{ "game": "<gameId>", "version": "<versionId>" }
```

Drops cached manifest context for that release. Used after a re-publish.

### `GET /api/v1/depot/healthcheck`

`200` with an empty body when the depot can serve content.

## Import (upload your own files)

Used when a user already has a build on disk and wants to publish it to the depot. Admin
only.

### `POST /api/v1/client/depot/import/session`

```json
{
  "gameTitle": "Game",
  "appVersion": "1.0",
  "gameId": "clx...",
  "device": "deck",
  "runsStandalone": true,
  "files": [
    {
      "path": "bin/game",
      "size": 1234,
      "mode": 493,
      "sha256": "<file digest>",
      "chunks": [ { "size": 4194304, "sha256": "<chunk digest>" } ]
    }
  ]
}
```

Response lists the objects the depot is missing. The client uploads only those.

### `GET /api/v1/client/depot/import/session/{id}`

Session summary plus the still-missing object list, refreshed against the store.

### `POST /api/v1/client/depot/import/session/{id}/objects`

Raw body stream. The chunk digest is declared in `x-content-sha256` and recomputed while
streaming. Nothing is buffered. The client never names a path; content is addressed by
hash.

### `POST /api/v1/client/depot/import/session/{id}/commit`

Verifies every declared object exists, writes the manifest last, pins the new
`GameVersion` to the manifest digest, and records provenance.

### `DELETE /api/v1/client/depot/import/session/{id}`

Abandons the session. Temporary state is dropped; already written objects stay, because
they are content addressed and may be referenced elsewhere.

## Playtime

All routes require `trackPlaytime`.

### `GET /api/v1/client/playtime`

All playtime rows for the user and selected profile.

### `GET /api/v1/client/playtime/{gameId}`

One row. 404 when the game has no recorded playtime, 400 for an unknown game.

### `POST /api/v1/client/playtime/{gameId}`

```json
{ "seconds": 600 }
```

Integer seconds, between 1 and 31,536,000 (one year). Values outside the range are 400.
The server adds the delta to the existing row for that game, user and profile.

## Profiles

### `GET /api/v1/client/profiles`

Players on the account, default first, then by creation.

### `POST /api/v1/client/profiles`

```json
{ "name": "Player two", "color": "#2563eb" }
```

The first profile created for an account becomes the default.

### `PATCH /api/v1/client/profiles/{id}`

Rename or recolour. At least one of `name` or `color` is required, otherwise 400.
Deletion is deliberately absent: profiles own playtime and save history.

## Collections

### `GET /api/v1/client/collection`

Collections owned by the user.

### `GET /api/v1/client/collection/default`

The default collection resolved for the current profile, including entries. This is what
the desktop shows as the library shelf.

Mutations exist (`POST/DELETE` on entries) for the web client that is being removed. The
desktop only reads collections. The Go server may keep read routes only, as long as the
desktop read paths above stay.

## Saves

All routes require `cloudSaves`. Every save belongs to a user and a profile.

### `GET /api/v1/client/saves`

Slots for the user and profile across games.

### `GET /api/v1/client/saves/{gameId}`

Slots for one game.

### `GET /api/v1/client/saves/{gameId}/{slotIndex}`

One slot. `slotIndex` is an integer.

### `POST /api/v1/client/saves/{gameId}/{slotIndex}/push`

Uploads a new save version. The body is the manifest plus archive or an upload session,
depending on size. Conflict rules apply: a newer remote version than the one the client
based on is 409 with the remote version id.

### `GET /api/v1/client/saves/{gameId}/{slotIndex}/sync`

Current version id and metadata, used to decide whether to pull.

### `GET /api/v1/client/saves/{gameId}/{slotIndex}/versions`

History for the slot.

### `GET .../versions/{versionId}` and `GET .../versions/{versionId}/download`

Version metadata and bytes.

### `POST .../versions/{versionId}/pin`

Pins a version against retention cleanup.

### `POST .../versions/{versionId}/restore`

Makes an old version the current one.

### `DELETE .../versions/{versionId}`

Removes a version, unless pinned.

### `DELETE /api/v1/client/saves/{gameId}/{slotIndex}`

Removes the slot and its versions.

### `GET /api/v1/client/saves/settings`

```json
{
  "slotLimit": 10,
  "sizeLimit": 268435456,
  "history": 5,
  "userStorageLimit": 0,
  "features": {
    "versionedSaves": true,
    "conflictResolution": true,
    "restore": true,
    "pinning": true,
    "storageQuota": false
  },
  "apiVersion": 2
}
```

`userStorageLimit` is in MB across all games and profiles, `0` meaning unlimited. A push
over the quota is refused with 400.

### `GET /api/v1/client/saves/manifests/{gameId}`

The Ludusavi manifest entry the client uses to find local save files and build an archive.
Read-only. Placeholders are resolved locally by the client for its platform and Proton
prefix.

## WebSockets

Three feeds, same text protocol as the old server.

| Path | Auth | Purpose |
| --- | --- | --- |
| `/api/v1/client/auth/code/ws` | pairing code in `Authorization` | Delivers the auth token after approval |
| `/api/v1/task` | session or client credentials | Task progress and logs |
| `/api/v1/notifications/ws` | session or client credentials | Notifications |

Protocol:

- Server sends `connect` on open, or `unauthenticated` when credentials are missing.
- Client sends `connect/<id>` to subscribe to a task.
- Server sends JSON messages, one per frame, with the task or notification payload.
- Non-JSON plain text frames are status lines, not data.

WebSocket upgrades are subject to an origin check. The desktop sends no browser origin;
the dashboard does not use WebSockets at all.

## Removed from the contract

These existed for the removed Nuxt web client and are not part of the Go server:

- `POST /api/v1/client/user/webtoken`
- `POST /api/v1/client/auth/session`
- `GET /api/v1/client/news*`
- Web collection mutation routes, beyond what the dashboard needs
- `/api/v1/client/auth/index.get` (web session based client attach)

Removing them does not affect a paired desktop client, which does not call them.

## Compatibility tests

The port is verified by replaying the desktop request set against the old server and the
Go server with identical fixtures, then diffing status, headers and bodies. Responses
that legitimately differ (timestamps, request ids) are normalized in the harness. This
runs in phase 5 and nightly after that.
