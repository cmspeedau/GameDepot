# Contributing

GameDepot is a hard fork of [Drop OSS](https://github.com/Drop-OSS/drop). Development
happens here, including with AI-assisted tooling. Upstream does not accept that kind of
contribution, and work from this fork does not go upstream.

Read [`AGENT.md`](AGENT.md) for engineering rules and [`ARCHITECTURE.md`](ARCHITECTURE.md)
for the internals before you start. [`ROADMAP.md`](ROADMAP.md) lists what is planned and
what already exists.

## Before you start

- Check open issues and the roadmap phase your change belongs to.
- For a schema change, a client contract change, or a depot format change, open a
  discussion or an ADR first. These are compatibility surfaces, not implementation
  details.
- For anything larger than a bug fix, say what you plan to do before writing it, so
  nobody duplicates work.

## Setup

The server and desktop are separate toolchains.

```bash
# server, once backend/ exists
cd backend
go build ./...
go test ./...

# desktop UI
cd desktop/main
pnpm install
pnpm dev

# desktop shell
cd desktop/src-tauri
cargo check
```

A development database can be started with the compose file in `deploy/`, once it
exists. Until then, point `DATABASE_URL` at any PostgreSQL 13 or newer instance.

## Branching

- `main` is the default branch and should always build.
- Branch before you change anything: `feature/<slug>`, `fix/<slug>`, `chore/<slug>`,
  or `port/<slug>` for a module ported from the old server.
- Keep slugs short and lowercase.
- Rebase on `main` before opening a pull request. Do not merge `main` into a feature
  branch just to update it.

## Commits

Use conventional commit subjects:

```text
feat: serve droplet chunks from the depot package
fix: correct AES counter endianness in chunk decryption
docs: add client API reference
refactor: split ACL resolution from session lookup
test: add CAS manifest digest fixtures
```

- One logical change per commit.
- The body explains why the change is needed and any compatibility consequence.
- Never commit secrets, tokens, database dumps, or personal data.
- Never commit generated depot objects or test data larger than a few hundred kilobytes.

## Pull requests

- Target `cmspeedau/GameDepot`, base `main`. Never `Drop-OSS/drop`.
- Fill in the pull request template: what changed, why, how it was verified, and any
  compatibility impact.
- Keep the diff focused. Move unrelated cleanup into its own pull request.
- Link the roadmap item or issue it closes.
- A pull request that changes a wire format needs fixture tests in the same pull request.

## Verification

Run the checks for what you changed:

| Change | Run |
| --- | --- |
| Go code | `go build ./... && go vet ./... && go test ./...` |
| SQL or queries | `sqlc generate`, then the handler tests |
| Dashboard templates | build the Tailwind CSS, load the page, check the CSRF path |
| Desktop UI | `pnpm typecheck && pnpm build` |
| Desktop Rust | `cargo check && cargo test` |
| Depot or manifest code | the equivalence fixtures, plus `go test -run Manifest` |

If a check fails for a reason that predates your change, say so in the pull request
rather than silently skipping it.

## Compatibility rules

These are not style preferences. Breaking any of them breaks existing installs or
paired clients.

- The PostgreSQL schema and enum labels stay as they are.
- The `drop-token` cookie name and the `Session` row shape stay as they are.
- `APIToken` rows, ACL strings and Bearer handling stay as they are.
- `Authorization: JWT <clientId> <jwt>` stays ES384 over the stored certificate.
- The client response shapes in [`docs/CLIENT-API.md`](docs/CLIENT-API.md) stay as
  documented.
- Droplet v2 and `gamedepot-droplet` v1 formats stay byte for byte.
- WebSocket text protocol stays `connect/<id>`, `unauthenticated`, and JSON messages.

If a change needs to cross one of these lines, it is an ADR, not a patch.

## Fork boundary

- Open pull requests against this repository only.
- The pre-rewrite code and its history are archived at `cmspeedau/GameDepot-legacy`.
  That repository is read-only. Use it as the reference for porting, never as a push
  target. `docs/PROVENANCE.md` records the last commit and the license chain.
- Do not add an upstream remote for pushing. Fetch reference code from a throwaway
  clone instead.
- Do not publish to upstream namespaces.
- Keep `LICENSE`, `NOTICE` and upstream copyright headers intact.

## Review

- Reviewers check the contract before the style.
- A maintainer merges. Do not merge your own pull request unless you are the sole
  maintainer.
- Squash or rebase to keep `main` linear, unless the pull request is deliberately
  multi-commit with meaningful history.

## License

Contributions are licensed AGPL-3.0-only, matching the project. By opening a pull
request you confirm you have the right to submit the code under that license.
