# 0009: The archived GameDepot is our own history, not a third party

- Status: Accepted
- Date: 2026-11-18
- Deciders: maintainers

## Context

Store ingestion (GOG first, then Epic) needs provider behaviour that is not written
down anywhere official: which OAuth client GOG accepts, the one redirect URI registered
for it, how Epic's device-code exchange works, which endpoints are zlib-compressed
without saying so, how a chunk URL is signed.

The predecessor of this repository, developed for months as `cmspeedau/GameDepot` and
now archived as `cmspeedau/GameDepot-legacy`, has that knowledge in working code: about
1,550 lines of GOG and 1,120 of Epic, plus roughly 3,700 lines of tests that record
real provider responses. The archive is AGPL-3.0-or-later and the author is the same,
so it is not a third party.

ADR-0001 says no source code from another game distribution project is copied or
translated. Read literally, that rule is about projects this one does not own; this
record makes the boundary explicit so review does not have to re-argue it.

The old code is TypeScript on Nitro and its output was the old data plane
(`droplet` manifests, torrential's chunk store). This project is Go and builds
`gamedepot-release` v1 through `internal/depot`. Only the provider edge is portable;
the manifest and chunk-format layers are not.

## Decision

Reuse the archive as a specification and a fixture source, and implement the result in
Go.

- The provider-facing packages under `internal/store/` are written in Go. Their
  behaviour is derived from the legacy TypeScript, read endpoint by endpoint.
- Where the legacy tests carry recorded provider payloads, the payload shapes move
  across as test data for the Go tests. Constants that encode a provider fact, such as
  GOG's public OAuth client pair and its registered redirect URI, move across with the
  comment that says how they were verified.
- The legacy manifest, chunking and streamer layers are not ported. Acquisition ends at
  a staging directory of real files, which `depot.BuildRelease` already accepts.
- The license needs no change: AGPL-3.0-or-later code from the same author lands in an
  AGPL-3.0 project. `NOTICE` records the lineage in one line so it is not a mystery in
  five years.
- ADR-0001 keeps its force for everyone else's code. Copying or translating a foreign
  project remains out of bounds; this record covers this project's own history only.

## Consequences

- Days of endpoint probing are avoided, and a class of silent breakage goes away: the
  comments carry the live verification date and what the wrong choices look like
  (GOG answers an unregistered redirect with an "invalid client" page, some
  `getFilteredProducts` query params return HTTP 500, retired content paths 404).
- The Go code is new, so it must be re-verified rather than trusted on the strength of
  the old tests. The provider moves without notice and a mistranslation shows up as a
  failed ingest, not a compile error.
- Two behaviours are deliberately inherited as requirements, not accidents: refresh
  tokens rotate on use, so every refresh must write the new value back; and a rejected
  credential marks the connection for re-auth instead of retrying.
- Nothing links against the archive and it is not a git submodule. It is a reference a
  contributor can consult at `/home/google/backups/GameDepot-archive.git` or on GitHub.

## Alternatives considered

- **Treat the archive as off-limits and re-derive everything from public docs.**
  Legally moot and practically slow; the official docs cover the account endpoints but
  not the content-system quirks that took live accounts to learn.
- **Run the old TypeScript as a sidecar ingestion worker.** Preserves the most code and
  was the fastest path to a green ingest, but it puts Node back into a deployment whose
  contract is a single Go image, and it means two credential stores and two schemas for
  one feature.
- **Vendor the legacy files verbatim into `internal/store/`.** Rejected: the language
  and the surrounding architecture are different, and the result would be
  TypeScript-shaped code written in Go.

## References

- ADR-0001 (independence and no code reuse), ADR-0005 (depot format).
- `cmspeedau/GameDepot-legacy` (archived), paths `server/server/utils/gog*`,
  `server/server/utils/epic*`, `server/server/utils/depot/credential-crypto.ts`.
- Local mirror: `/home/google/backups/GameDepot-archive.git`, branch `develop`.
- `docs/STEAM_INTEGRATION_PROPOSAL.md` in the archive, for the provider-adapter shape.
