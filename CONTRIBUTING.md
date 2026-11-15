# Contributing

GameDepot is an independent project. It owns its schema, format and client contract, and
it does not accept code copied from another game distribution project.

Read [`AGENT.md`](AGENT.md) for engineering rules and [`ARCHITECTURE.md`](ARCHITECTURE.md)
for the internals before you start. [`ROADMAP.md`](ROADMAP.md) lists what is planned and
what already exists.

## Independence

- Write new code for this project. Do not copy or line-by-line translate another
  implementation.
- If a module solves a problem another project also solves, study the behaviour, write
  the design here, then implement it in this project's shape.
- Check the license of every dependency. AGPL-3.0, MIT, BSD and Apache-2.0 are
  acceptable. Record unusual terms in `NOTICE`.
- If any code is copied from an AGPL project, it stays AGPL and attribution is required.
  The simplest rule is not to copy.

## Before you start

- Check the roadmap phase your change belongs to.
- For a schema change, a client contract change or a depot format change, open an issue
  or write an ADR first.
- For anything larger than a bug fix, say what you plan to do before writing it.

## Setup

```bash
# server, once backend/ exists
cd backend
go build ./...
go test ./...

# desktop UI
cd desktop
pnpm install
pnpm dev

# desktop shell
cd desktop/src-tauri
cargo check
```

Start PostgreSQL with the compose file in `deploy/`, once it exists, or point
`DATABASE_URL` at any PostgreSQL 13 or newer instance.

## Branching

- `main` is the default branch and should always build.
- Branch before you change anything: `feature/<slug>`, `fix/<slug>` or `chore/<slug>`.
- Keep slugs short and lowercase.
- Rebase on `main` before opening a pull request.

## Commits

```text
feat: serve depot objects with immutable cache headers
fix: reject duplicate paths in a release manifest
docs: add the client API reference
refactor: split session lookup from CSRF validation
test: add manifest digest fixtures
```

- One logical change per commit.
- The body explains why and any contract consequence.
- Never commit secrets, tokens, database dumps or personal data.
- Never commit generated depot objects or test data larger than a few hundred kilobytes.

## Pull requests

- Target `main`.
- Fill in the template: what changed, why, how it was verified, and any contract impact.
- Keep the diff focused. Move unrelated cleanup into its own pull request.
- Link the roadmap item or issue it closes.
- A pull request that changes a wire format needs fixture tests in the same pull request.

## Verification

| Change | Run |
| --- | --- |
| Go code | `go build ./... && go vet ./... && go test ./...` |
| SQL or queries | `sqlc generate`, then handler tests |
| Dashboard templates | compile Tailwind, load the page, submit a form |
| Desktop UI | `pnpm typecheck && pnpm build` |
| Desktop Rust | `cargo check && cargo test` |
| Depot or manifest code | fixture tests, plus an integrity run |

If a check fails for a reason that predates your change, say so rather than skipping it
silently.

## Contracts

These are documented in this repository and must change deliberately:

- The schema in `backend/sql/migrations`.
- The client API in `docs/CLIENT-API.md`.
- The `gamedepot-release` manifest and object layout in `ARCHITECTURE.md`.
- The session cookie, token storage and pairing flow.

A change to any of them updates the document, the code and the tests in one pull request.

## Review

- Reviewers check the design and the contract before the style.
- A maintainer merges. Do not merge your own pull request unless you are the sole
  maintainer.
- Squash or rebase to keep `main` linear, unless multi-commit history is meaningful.

## License

Contributions are licensed AGPL-3.0-only, matching the project. By opening a pull
request you confirm you have the right to submit the code under that license.
