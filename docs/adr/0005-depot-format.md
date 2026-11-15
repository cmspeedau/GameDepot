# 0005: New depot format, `gamedepot-release` v1

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

Releases must be chunked, encrypted and verifiable, and served from the same server
process. There is no existing format to match.

## Decision

- Fixed 4 MiB plaintext chunks, SHA-256 over plaintext.
- One random 32-byte AES-256 key per release.
- One random 12-byte nonce per object.
- AES-256-GCM, with the object hash as additional authenticated data.
- Canonical JSON manifest, digest over the manifest without its digest field.
- Objects at `objects/<xx>/<sha256>`, manifests at `manifests/<digest>.json`.
- Atomic writes, immutable objects, reference-counted deletion.

## Consequences

- A cached object cannot be stale, because its name is the plaintext hash.
- The release key is in the manifest, so manifest access implies content access. This
  is accepted and stated in the threat model.
- No range requests in v1. Resume happens at object granularity.
- The canonical encoder is ours and must be tested against its own fixtures.

## Alternatives considered

- **Stream cipher with per-file keys.** More granular, but harder to verify and more
  state to manage.
- **Unencrypted objects behind the API.** Simpler, but loses at-rest protection and
  makes a leaked object URL a data breach.
- **A separate depot service.** Reintroduces the process split this project avoids.
