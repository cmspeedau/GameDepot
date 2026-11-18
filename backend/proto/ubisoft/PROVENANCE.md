# Ubisoft protocol schema files

These `.proto` files describe Ubisoft Connect's wire formats. They are protocol
definitions, not a game distribution project's implementation, which is why they are in
scope under ADR-0001; see ADR-0011 for the reasoning and the license record in `NOTICE`.

| File | Origin | License |
| --- | --- | --- |
| `download.proto` | claabs/uplay-install-reverse (install-manifest schema) | MIT |
| `demux.proto` | YoobieRE/ubisoft-demux-node (demux socket protocol) | MIT |
| `download_service.proto` | YoobieRE/ubisoft-demux-node | MIT |
| `ownership.proto` | YoobieRE/ubisoft-demux-node | MIT |

Upstream layout and field numbers are preserved: field numbers are the wire contract, so
renaming or renumbering a field would silently break decoding against the live service.

The files carry no `go_package` option on purpose. Adding one would edit vendored text and
make upstream comparison awkward, so the Go import paths are supplied on the command line
instead, one package per file because the upstream proto packages reuse message names
(`Req`, `Rsp`, `Upstream`, `Downstream`, `InitializeReq`) that would collide in a single
generated Go package.

Regenerate with `make proto` (needs `protoc` and `protoc-gen-go`); output lands in
`internal/proto/{downloadpb,demuxpb,downloadservicepb,ownershippb}`.
