# 0003: Move the depot data plane into Go, retire torrential

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

Upstream splits the data plane into a Rust service, `torrential`, about 1,100 lines. It
does four things:

1. Serves `manifest.json`, `speedtest`, `content/{game}/{version}/{chunk}`, `invalidate`
   and `healthcheck` on port 5000.
2. Generates droplet v2 manifests from a version directory.
3. Issues the root CA and client certificates.
4. Answers `hasBackend`, `listFiles` and `peekFile` for the library providers.

The server spawns it, supervises it, and talks to it over a protobuf socket on port
33148. nginx proxies `/api/v1/depot/` to port 5000. The client discovers the depot through
a `Depot` row pointing at `${EXTERNAL_URL}/api/v1/depot/`.

The split buys concurrency, which matters for serving many large downloads. It costs
three moving parts (Rust binary, protobuf socket, nginx), a code generation step, and a
supervision layer that has to restart a child process without losing in-flight requests.
The Go server can already stream bytes concurrently, and the expected deployment is one
household or a small group, not a CDN.

## Decision

Implement the data plane in Go, inside the server binary.

- Depot HTTP handlers are registered under `/api/v1/depot/`.
- Manifest generation, CAS chunking and publish run in worker pools inside the server.
- CA and leaf certificate issuance use `crypto/x509` and ECDSA P-384.
- Library backend questions become filesystem calls: `stat`, `WalkDir`, `ReaderAt`.
- The protobuf socket, the `torrential` crate, port 5000 and the nginx config are
  removed.
- The `Depot` row is upserted at boot with the same endpoint value, so clients keep
  using their stored URL.

The formats stay byte for byte:

- Droplet v2: 64 MiB chunks, at most 512 files per chunk, SHA-256 over plaintext, random
  16-byte key, random 16-byte IV per chunk, AES-128-CTR with a **64-bit little-endian**
  counter.
- CAS: 4 MiB fixed chunks, `objects/<xx>/<sha256>`, canonical JSON manifest
  `gamedepot-droplet` v1, digest over the manifest without its `digest` field.

`libraries/droplet` remains in the tree because the desktop client links it.

## Consequences

- One process to build, deploy, monitor and stop.
- No code generation from `.proto` files, no socket protocol, no proxy.
- Serving large downloads and serving HTML share a process. A slow client holds a
  goroutine and a file descriptor; per-request limits and timeouts become important, and
  the server needs a bounded number of concurrent chunk streams.
- The AES counter mode must be written by hand, because Go's `cipher.NewCTR` uses a
  big-endian counter over the whole block. A mismatch silently corrupts every download,
  so equivalence tests against the Rust output are mandatory.
- Canonical JSON must be written by hand, because `json-canonicalize` is a Node package.
  Manifests produced by the old server are checked in as goldens.
- Future horizontal scaling would require splitting the data plane out again. That is
  accepted and recorded as a known limit.

## Alternatives considered

- **Keep torrential.** No format risk, but keeps the supervision layer, the socket, the
  protobuf build step and nginx, and leaves the server unable to publish a release
  without a child process.
- **Rewrite torrential in Go as a separate service.** Same language, same split costs.
- **Serve the depot from a static file server in front of the object store.** Simpler
  reads, but loses delta context, invalidation and the speedtest, and moves the CA work
  elsewhere anyway.

## References

- Upstream `torrential/src/`, `libraries/droplet/src/manifest.rs`,
  `libraries/droplet/src/ssl.rs`, `server/build/nginx.conf`.
- ADR-0006.
