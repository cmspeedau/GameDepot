# Operations

How to run, back up and upgrade a GameDepot instance. This describes the target
deployment. The server does not exist yet, so treat the commands as the contract the
implementation must satisfy.

## What you need

- PostgreSQL 13 or newer, reachable from the server.
- A directory for the library, mounted at `LIBRARY`.
- A directory for data, mounted at `DATA`. It holds objects, depot chunks, manifests and
  the certificate authority.
- A reverse proxy that terminates TLS. The server speaks plain HTTP and expects
  `EXTERNAL_URL` to be the public HTTPS origin.

Both volumes must be backed up together. Depot manifests reference chunk objects by
hash, so restoring one without the other produces releases that cannot be verified.

## First run

1. Start the server with `DATABASE_URL`, `EXTERNAL_URL`, `DATA` and `LIBRARY` set.
2. The server creates the `setup` system token and prints it once, in the log, at `info`
   level.
3. Open `https://<host>/auth/setup`, enter the token, and create the admin account.
4. The token is deleted on success. Check the log for a warning if setup is still open
   after you finish.

If the setup token is lost before use, restart with `LOG_LEVEL=debug`; it is regenerated
only when no `setup` token exists. If one exists and is unknown, delete the row manually:

```sql
DELETE FROM "APIToken" WHERE mode = 'System' AND 'setup' = ANY(acls);
```

## Configuration

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none | `postgresql://user:pass@host:5432/db` |
| `EXTERNAL_URL` | yes in production | `http://localhost:4000` | Public origin, no trailing slash |
| `DATA` | yes | `./.data/data` | Objects, depot, certificates |
| `LIBRARY` | yes | `./.data/library` | Library sources |
| `PORT` | no | `4000` | Listen port |
| `GAMEDEPOT_CREDENTIAL_KEY` | for store features | none | 32 bytes, hex or base64 |
| `GAMEDEPOT_STORE` | no | `false` | Enables store ingestion |
| `METADATA_TIMEOUT` | no | `5000` | Milliseconds per provider request |
| `CHECK_FOR_UPDATES` | no | `true` in production | Set `false` to disable |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

A wrong `EXTERNAL_URL` breaks pairing links, depot URLs and `Secure` cookie behaviour.
It is validated at boot, so a malformed value fails fast rather than at first download.

## Backups

Take both, at the same time:

```bash
pg_dump --format=custom "$DATABASE_URL" > gamedepot-db.dump
tar -C "$DATA" -czf gamedepot-data.tar.gz .
tar -C "$LIBRARY" -czf gamedepot-library.tar.gz .
```

Restore in the same order: database, data, library. Then start the server and check
`GET /api/v1/depot/healthcheck` and one game's download.

The certificate authority lives under `DATA` in the object store. Losing it does not
invalidate already issued client certificates (verification uses each leaf), but it
prevents issuing new ones without re-pairing every client.

## Upgrading

For a patch or minor release within the compatible line:

1. Back up as above.
2. Stop the old process.
3. Swap the binary or image.
4. Start the new process.

- The server applies pending goose migrations on boot when `GAMEDEPOT_AUTO_MIGRATE=true`,
  which is the default in development. In production, run `gamedepot migrate` before
  swapping the binary, or set the variable true for the first boot.
- A failed migration stops the process. There is no half-applied boot.

## Health and monitoring

- `GET /api/v1/health` is the liveness probe and returns `204 No Content` without
  touching the database. Use it for container liveness and orchestrator liveness gates.
- `GET /api/v1/ready` is the readiness probe and returns `200 OK` when the database
  answers, `503` with a JSON error body when it does not. Point load balancer and
  orchestrator readiness checks here.
- The depot also exposes `GET /api/v1/depot/healthcheck` for a lighter check.
- Every request gets an `X-Request-Id` response header (a new UUID when the client does
  not send one) and the value is placed in the request context; structured logs pick it
  up as `request_id`. Access lines are JSON `http_request` records with `method`, `path`,
  `status`, `duration_ms` and `request_id`.
- Logs are JSON on stdout. Ship them with whatever the host provides; no log file is
  written.
- Watch disk space on `DATA` first. Chunk objects are append-only and deduplicated, but
  they are not removed automatically.

## Common problems

**Clients cannot pair.** Check `EXTERNAL_URL` and the reverse proxy's WebSocket upgrade
for `/api/v1/client/auth/code/ws`. The dashboard approval also needs a signed-in admin.

**Downloads start and fail with a checksum error.** The depot and the manifest disagree.
Run the integrity task from the dashboard. If it reports missing objects, restore `DATA`
from backup rather than re-publishing, unless the release is reproducible.

**Store ingestion authenticates then fails.** Provider endpoints change without notice.
Check the task log for the HTTP status, then the adapter's recorded fixtures. Do not retry
in a loop; each attempt may consume a rate limit.

**Dashboard loads without styles.** The embedded Tailwind file is missing from the build.
This is a packaging bug, not a configuration one. Rebuild with the CSS step.

## Security checklist

- TLS terminates at the proxy, and `EXTERNAL_URL` uses `https`.
- `GAMEDEPOT_CREDENTIAL_KEY` is set before connecting any store account, and is stored
  outside the repository.
- The service runs as a non-root user that owns `DATA` and `LIBRARY`.
- Dashboard access is limited to trusted networks if the instance is not on the public
  internet.
- Backups are encrypted if they contain store credentials in sealed rows.
