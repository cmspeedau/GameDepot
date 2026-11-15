# 0008: Chunk size, 4 MiB for v1

- Status: Accepted
- Date: 2026-11-16
- Deciders: maintainers
- Refines: [0005](0005-depot-format.md)

## Context

ADR-0005 set fixed-size, content-addressed chunks but left the size as a tunable.
Steam's depot system (seen in DepotDownloader) uses roughly 1 MiB chunks, each identified
by a content hash, and reuses local chunks by matching those hashes when a file changes
between manifest versions. That makes Steam's updates cheap: a small edit downloads only
the changed chunk.

The size trades off against three things:

- **Delta reuse.** With fixed boundaries, a 1-byte edit near the start of a chunk
  invalidates the whole chunk. Smaller chunks re-download less, larger chunks re-download
  more.
- **Object and manifest volume.** A 50 GiB title is about 13,000 objects at 4 MiB and
  about 51,000 at 1 MiB. The manifest lists every chunk, so a smaller chunk multiplies
  manifest size and the number of files on disk.
- **Transfer granularity.** v1 has no HTTP range support, so an object is the smallest
  unit of transfer, verification, and resume. Smaller chunks mean more round trips per
  download and per cache-node fill.

## Decision

- v1 keeps **4 MiB** chunks. The depot already builds, publishes and verifies at this
  size, and the tests pass.
- Cross-version reuse comes from the content hash, not from small chunks: a file that is
  unchanged between versions is unchanged in its chunks, so the desktop reuses those
  objects regardless of size (the phase 5 incremental-install item).
- Chunk size is a constant (`depot.ChunkSize`), not written into the format version, so
  it can change in a later `gamedepot-release` version without touching the manifest
  shape.

## Consequences

- A single large file edited near its start re-downloads up to 4 MiB for that chunk
  rather than 1 MiB. For the expected library (whole games, occasional rebuilds) this is
  a small number of chunks.
- Fewer objects and smaller manifests than a 1 MiB design, which matters for the
  read-through cache node and for listing directories.
- More HTTP requests per title than a range-supported large-chunk design would need. If
  downloads show request overhead dominating, add HTTP range support instead of shrinking
  chunks.

## Alternatives considered

- **1 MiB, matching Steam.** Better worst-case delta reuse, but four times the objects,
  four times the manifest entries, and four times the requests for a server that has no
  range support. Not worth it for v1.
- **Content-defined chunking (CDC).** Boundaries move with content so edits localize
  tightly and reuse is better than any fixed size. Rejected for v1: it makes chunk
  boundaries depend on the chunking function, complicates verification and the cache
  node, and cannot be added later without a format version, whereas fixed size is simple
  and already working.
- **Very large chunks (16+ MiB).** Fewest objects, but poor reuse and coarse resume.

## Revisit trigger

Move to 1 MiB, add HTTP range support, or adopt CDC if, after phase 5, real updates show
that fixed-boundary reuse is leaving too much data on the table. Any change is a
`gamedepot-release` version bump with a migration note for existing objects.

## References

- `docs/adr/0005-depot-format.md`
- `backend/internal/depot/depot.go` (`ChunkSize`)
- DepotDownloader `ContentDownloader.cs`: chunk matching and reuse by hash
