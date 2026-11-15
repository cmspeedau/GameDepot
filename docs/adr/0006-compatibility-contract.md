# 0006: Treat schema, cookies, JWTs and depot formats as contracts

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The rewrite replaces the server while keeping the desktop client and, for existing
installs, the database. A user who swaps the container image should not have to:

- Sign in again in the browser.
- Re-pair their desktop clients.
- Reissue API tokens.
- Re-upload or re-import their library.
- Rebuild depot releases.

Each of those is possible to preserve only if specific details are held fixed, and some
of them are easy to change by accident during a port: a field rename, a different cookie
attribute, a different JWT algorithm, a counter endianness.

## Decision

The following are contracts. They change only through a new ADR that explains the
migration path, and never silently in a refactor.

1. **Database schema.** Tables, columns, enum labels, array columns and indexes as the
   Prisma migrations left them. No boot-time DDL.
2. **Dashboard session.** Cookie name `drop-token`, `Session` row shape, expiry semantics,
   and the sign-in JSON field names.
3. **API tokens.** `APIToken` rows, ACL string arrays, `Bearer` handling, system tokens
   with `setup`, and the `settings:read` ACL used by the client.
4. **Client authentication.** `Authorization: JWT <clientId> <jwt>`, ES384, verified
   against the certificate stored in `Certificate`, with 30 seconds of clock tolerance.
   Certificates issued by the old server stay valid.
5. **Droplet v2 manifests.** Compression of files into chunks, chunk size, file entry
   fields, checksum domain, key and IV sizes, and AES-128-CTR with a 64-bit little-endian
   counter.
6. **CAS manifests.** Chunk size, object path layout, canonical JSON encoding, digest
   domain, and the `gamedepot-droplet` / version `1` identifiers.
7. **Client response shapes.** Field names, envelope choices and status codes documented
   in `docs/CLIENT-API.md`, including the error body with `statusCode` and
   `statusMessage`.
8. **WebSocket text protocol.** `connect`, `connect/<id>`, `unauthenticated`, and JSON
   payload frames.
9. **Depot endpoint.** The `Depot` row id and endpoint value `${EXTERNAL_URL}/api/v1/depot/`,
   so stored client URLs keep working.
10. **Routes the desktop calls.** Every path in `docs/CLIENT-API.md` remains, including
    the unversioned `/api/v1` and `/api/v1/settings`.

## Consequences

- Existing installs and clients survive the port.
- The fork inherits constraints it did not choose. Some are awkward: the AES counter mode
  is non-standard, `appName` stays `Drop` in `/api/v1`, error bodies duplicate messages, and
  the CAS digest needs a custom canonical encoder.
- Every contract needs a test. Format fixtures are checked in. A contract test replays the
  desktop request set against both servers.
- Removing a route from the contract is a breaking change and needs a deprecation plan, not
  a deletion in the same release.
- New features that need schema changes require unfreezing, which is deliberately
  expensive.

## Alternatives considered

- **Allow breaking changes and require a migration.** Simpler code, but forces every
  operator to re-pair clients and rebuild the library, and makes the first release
  unrecoverable for anyone without backups.
- **Version the API and keep both contracts.** Cleaner long term, but doubles the port for
  a client that is under our control and already deployed.

## References

- `ARCHITECTURE.md` sections 5, 6 and 9.
- `docs/CLIENT-API.md`.
- ADR-0003, ADR-0004.
