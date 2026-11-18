# 0011: Ubisoft Connect ingestion, and the vendored protocol schemas

- Status: Accepted
- Date: 2026-11-18
- Deciders: maintainers

## Context

Ubisoft Connect was the second storefront with a working acquisition path in the
archived predecessor: about 1,400 lines of implementation and 1,000 of tests, covering
session auth, the entitled-library query, install-manifest decoding and slice download.
Three facts from that work decide the shape of the port.

1. **The HTTP acquisition hosts are gone.** The legacy product-config and manifest
   endpoints (`api-uplayv2.ubi.com`, `uplaypc-dns.ubi.com`) were probed dead in Nov 2026;
   DNS no longer resolves. Install content now arrives over the Ubisoft Connect "demux"
   socket: a WebSocket carrying protobuf envelopes, with signed slice URLs minted in
   batches. This is not a fallback any more; it is the route.
2. **Two public app ids, each with a reason.** `3ad0af50-…` is the widely published
   Connect PC client id. `f68a4bb5-…` is the one the v3 sessions API accepts for
   server-side logins: the web-club id sits behind Ubisoft's bot protection, which
   answers a 403 captcha before credentials are even checked, so using it looks like a
   bad password. Both are constants of the service, not secrets.
3. **Owned titles come from the launcher's GraphQL gateway.** The old
   `/v1/profiles/{uid}/ownedtitles` route 404s; the gateway
   `/v1/profiles/me/uplay/graphql` answers for a stored session ticket and includes
   names.

The install manifest itself is protobuf (`mg.protocol.download.Manifest`) behind a
   356-byte
header, gzipped for modern titles and raw zlib for older ones; slices are addressed by
SHA-1 under `slices_v3/<hex-char>/<SHA1>` and each slice's compression is detected by its
magic bytes. Content-addressed slices are the same shape as this project's depot objects,
so the mapping onto `internal/depot` is close to direct.

## Decision

- Port Ubisoft as a Go provider under `internal/store/ubisoft`, following the same
  contract GOG and Epic already implement (`store.Provider`), with demux socket
  acquisition rather than the dead HTTP path retained as anything more than a note.
- **Vendor the four protocol schema files** (`download.proto`, `demux.proto`,
  `download_service.proto`, `ownership.proto`) from `claabs/uplay-install-reverse` and
  `YoobieRE/ubisoft-demux-node`, both MIT, into `backend/proto/ubisoft/`, and generate Go
  types from them with `protoc-gen-go`.
  - Field numbers and names are the wire contract; a hand-written schema would be a guess
    about every field nobody has observed, and a wrong guess decodes silently into an
    empty manifest. Protobuf's own decoder is permissive, which makes silent
    mis-decoding the normal failure mode rather than a rare one.
  - ADR-0001 bars code from another game distribution project. These are protocol
    definitions published by reverse-engineering projects, not a distribution
    implementation, and MIT is an accepted license under `AGENT.md`. The provenance lives
    in `backend/proto/ubisoft/PROVENANCE.md` and `NOTICE`.
  - Upstream files are kept byte-identical: no `go_package` option is added to them.
    Import paths are passed on the command line instead, so `diff` against upstream stays
    meaningful.
- Session tickets and remember-me tickets go through the same sealing as every other
  store credential (`internal/secrets`), and a ticket Ubisoft rejects flags the
  connection instead of retrying it.
- 2FA, where Ubisoft challenges, is a first-class interactive step: the challenge travels
  in a 200 response with the fields the client needs to complete it, because an error
  body's custom fields can be stripped in production.

## Consequences

- One build step is added: `make proto` regenerates `internal/proto/ubi` and needs
  `protoc` plus `protoc-gen-go`. Generated files are committed, so a plain build needs
  neither tool.
- The repo now carries third-party schema text with attribution. If a future field-number
  change is needed, it is a divergence from upstream and must be justified against the
  live service, not against the schema's aesthetics.
- The predecessor's own ARCHITECTURE recorded that live verification against Ubisoft
  services was outstanding; its hermetic suites proved contract behaviour only. So this
  port inherits tests, not proof, and the first real ingest with an operator's Ubisoft
  account is the verification step, exactly as with GOG.
- Acquisition needs a long-lived WebSocket per task, batched URL minting with a 3600s TTL
  and re-mint on 403, and one fetch per slice even when several files share it. Those are
  the parts most likely to drift and the parts the port must make observable in the task
  log.

## Alternatives considered

- **Hand-write our own `.proto` files.** Zero third-party files, but every unobserved
  field becomes a guess, and a guessed field number decodes into nothing rather than into
  an error.
- **Keep the dead HTTP acquisition route.** It cannot work; the hosts do not resolve.
- **Skip Ubisoft and do Epic first.** Weighed first, then overtaken by events: Epic's
  decoder had no sample to validate against while Ubisoft had a complete path. A real
  Epic manifest was secured the same evening, so Epic is unblocked rather than parked,
  and Ubisoft still goes first because its path is the one already proven end to end.

## References

- `backend/proto/ubisoft/PROVENANCE.md`, `NOTICE`.
- ADR-0009 (the archive as specification), ADR-0010 (GOG platform handling).
- `claabs/uplay-install-reverse`, `YoobieRE/ubisoft-demux-node` (MIT schema sources).
