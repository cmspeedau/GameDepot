# Provenance

GameDepot is a derivative work of [Drop OSS](https://github.com/Drop-OSS/drop),
licensed AGPL-3.0. This document records where the code came from and where the
pre-rewrite history lives.

## Upstream

- Project: Drop OSS
- Repository: https://github.com/Drop-OSS/drop
- License: AGPL-3.0
- Role: original project. The database schema, desktop client, droplet formats and
  client HTTP contract all originate here.

## Pre-rewrite fork

The fork that this rewrite replaces was developed in a separate repository. It is kept
as the reference implementation for the port.

- Repository: https://github.com/cmspeedau/GameDepot-legacy
- State: archived, read-only
- Default branch: `develop`
- Last commit at archival: `1d7a7523392419462e5cee11debe1c4dcf8d75e7`
- Archived on: 2026-11-15

That repository holds the working Node/Nuxt server, the Tauri desktop client, the
`droplet` crate, the 131 Prisma migrations, the store adapters, the tests and the full
commit graph. At archival it had 15 branches, 16 tags and 13 pull requests.

A local mirror and a verified git bundle were taken before the archive. They are not
part of this repository; they exist as backup of the legacy history.

## This repository

This repository starts a new history. It does not carry the legacy commit graph, because
the server and the desktop UI are being replaced rather than patched, and carrying the
old history would make the port harder to review.

What is carried over conceptually, and must stay compatible, is listed in
`docs/adr/0006-compatibility-contract.md`.

When a module is ported, its behavior comes from the archived implementation, not from
memory. Ported code is new Go or TypeScript in this repository, released under
AGPL-3.0, with the upstream attribution in `NOTICE`.

## License chain

- Drop OSS: AGPL-3.0.
- `libraries/droplet`: AGPL-3.0-only.
- `torrential`: AGPL-3.0, not carried over; its role moves into the Go server.
- This repository: AGPL-3.0-only, see `LICENSE`.

If you distribute a build or run a modified version as a network service, the AGPL
requires you to offer the corresponding source. Point users at the tag or commit named
in the build metadata.
