# AGENT.md

Guidance for AI coding agents and humans changing this repository.

Read `README.md`, `ARCHITECTURE.md` and `ROADMAP.md` before touching code. The roadmap
is the source of truth for what exists. Nothing is checked off until it is merged and
tested.

## What this repository is

GameDepot is an independent self-hosted game distribution server and desktop client.
It owns its database schema, depot format, client protocol and desktop core. There is no
compatibility target with another project.

Current state: documentation only. There is no Go module, no dashboard and no desktop
project yet. Do not describe planned behaviour as implemented, and do not check a
roadmap item for code that is not merged.

## Independence

This project is independent by construction, and that has to stay true in the code.

- Do not copy source code from another game distribution project, including any
  archived or reference tree on this machine or in the git history.
- Do not port a module by translating its source line by line. Read the behaviour,
  write the design down, then implement it here in this project's own shape.
- Ideas and functional behaviour are not code. A new implementation is allowed to solve
  the same problem differently, and should.
- If a dependency is added, check its license. AGPL-3.0, MIT, BSD and Apache-2.0 are
  acceptable. Record anything unusual in `NOTICE`.
- If code is ever copied from an AGPL project, that code stays AGPL and attribution must
  be added to `NOTICE`. The simplest rule is not to copy.

## Layout

| Path | Contents |
| --- | --- |
| `backend/` | Go module: API, dashboard, depot, tasks (phase 1) |
| `desktop/` | Tauri client with a Vue 3 UI (phase 1) |
| `deploy/` | Container build and compose files |
| `docs/` | Client API, operations, decision records |

## Engineering rules

1. **Read the design first.** The architecture document is the contract inside this
   repository. If code and document disagree, stop and fix one of them on purpose.
2. **Define behaviour before implementing it.** For anything with a wire format, write
   the field list and the error cases first.
3. **Match the documented contract.** Field names, status codes and error shapes in
   `docs/CLIENT-API.md` are fixed once a client ships. Change the document and the client
   in the same pull request.
4. **Small, reviewable changes.** One logical change per commit. No unrelated formatting,
   no opportunistic refactors, no dependency upgrades bundled with a feature.
5. **No Node in the server image.** If a task needs a Node build step in `backend/`, stop
   and flag it. Dashboard assets are compiled ahead of time.
6. **Security defaults stay strict.** Path checks, symlink containment, CSRF,
   constant-time comparison, no credentials in logs, no secrets in fixtures.
7. **Atomic writes.** Objects, manifests and database migrations are written to a temp
   path, fsynced and renamed. Never write a final path in place.
8. **Verify before use.** A chunk is hashed before decryption and hashed again after.
   Never write unverified bytes to an install path.
9. **Cancellation is part of the job.** Any loop over files, chunks or network reads
   takes a `context.Context` and checks it.
10. **Tests are required for format code.** A codec change without a fixture test is not
    reviewable.

## Verification

Run what you touched before reporting done:

- `go build ./...`, `go vet ./...`, `go test ./...` in `backend/`.
- `pnpm typecheck` and `pnpm build` in `desktop/`.
- `cargo check` and `cargo test` in `desktop/src-tauri` when Rust changes.
- Depot fixtures when the manifest, chunking or encryption changes.

Report what passed, what failed, and what you did not run. Distinguish failures you
introduced from failures that were already there.

## Branching and commits

- `main` is the default branch and stays buildable.
- `feature/<slug>`, `fix/<slug>`, `chore/<slug>`.
- Conventional commit subjects: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`,
  `build:`, `chore:`.
- The body explains why, not what. The diff already says what.
- Reference the roadmap phase in the body when a change completes an item.

## Writing

Documentation and commit messages follow the same rules:

- No em dashes.
- No marketing language.
- No invented statistics or benchmarks.
- State status plainly: implemented, planned, or not started.
- Prefer concrete numbers, file paths and endpoint names over adjectives.

## When unsure

Ask before deleting or rewriting anything you did not create, especially:

- Files under `desktop/src-tauri` once the Rust core exists.
- Anything that looks like uncommitted user work.
- Recorded fixtures and test vectors.
- The wording of `NOTICE` or `LICENSE`.

If a task requires changing the schema, the client contract or the depot format, write
it down in `docs/adr/` first.
