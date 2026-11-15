# AGENT.md

Guidance for AI coding agents and humans changing this repository.

Read `README.md`, `ARCHITECTURE.md` and `ROADMAP.md` before touching code. The roadmap
is the source of truth for what exists. Nothing in the roadmap is checked off until it
is merged and tested.

## What this repository is

GameDepot is a self-hosted, DRM-free game distribution server and desktop client. It is
a hard fork of [Drop OSS](https://github.com/Drop-OSS/drop), which is AGPL-3.0. This
fork rewrites the server in Go, drops the browser UI in favour of a server-rendered
dashboard, moves the depot data plane out of the Rust service, and moves the desktop UI
from Nuxt to Vue 3 and Vite.

The database schema, the client HTTP contract, the depot on-disk formats and the
desktop Rust core are compatibility surfaces. Read those parts of `ARCHITECTURE.md`
before changing anything near them.

## Current state

Documentation and repository scaffolding only. There is no Go module, no dashboard, and
no desktop project in this tree yet. Do not describe planned behaviour as implemented,
and do not add a checked roadmap item for code that is not merged.

## Layout

| Path | Contents |
| --- | --- |
| `backend/` | Go module: API, dashboard, depot, tasks (phase 1) |
| `desktop/` | Tauri client with Vue 3 UI (phase 1) |
| `libraries/` | Shared Rust crates, including `droplet` |
| `deploy/` | Container build and compose files |
| `docs/` | Client API reference and architecture decision records |

## Engineering rules

1. **Read the existing code path first.** Find the upstream implementation before
   writing a Go equivalent. The answer to "how should this behave" is usually in
   `/tmp/GameDepot-ref` or in the git history of the fork parent, not in guesswork.
2. **Match the contract, not the style.** Where a format or response shape exists in
   the old server, reproduce it exactly, including field names, status codes and error
   bodies. Idiomatic Go applies to the code; the wire format belongs to the client.
3. **Do not invent behaviour.** If the old server returned a 403 for a case, return a
   403. If a field was optional, keep it optional.
4. **Small, reviewable changes.** One logical change per commit. No unrelated formatting,
   no opportunistic refactors, no dependency upgrades bundled with a feature.
5. **No Node in the server image.** If a task seems to need a Node build step in
   `backend/`, stop and flag it. The dashboard uses precompiled assets.
6. **Security defaults stay strict.** Path traversal checks, symlink containment, CSRF,
   constant-time token comparison, no credentials in logs, no secrets in fixtures.
7. **Atomic writes.** Objects, chunks and manifests are written to a temp path, fsynced
   and renamed. Never write a final path in place.
8. **Adapters return untrusted data.** Everything from GOG, Epic, Ubisoft or EA is
   validated before it reaches the depot. Keep provider build id, manifest id and content
   digest as separate identities.
9. **Cancellation is part of the job.** Any loop over files, chunks or network reads
   takes a `context.Context` and checks it.
10. **Tests are required for format code.** A codec change without a fixture test is
    not reviewable.

## Verification

Run what you touched before reporting done:

- `go build ./...`, `go vet ./...`, `go test ./...` in `backend/`.
- `pnpm typecheck` and `pnpm build` in `desktop/main`.
- `cargo check` and `cargo test` in `desktop/src-tauri` when Rust changes.
- For format work, run the equivalence fixtures. Do not skip them because they are slow.

Report what passed, what failed, and what you did not run. Distinguish failures you
introduced from failures that were already there.

## Fork boundary

This repository is an independent fork. Its work does not go upstream.

- Open pull requests against **`cmspeedau/GameDepot`**, not `Drop-OSS/drop`.
- `origin` is the only remote. Do not add upstream as a push target. For reference
  code, use a throwaway clone under `/tmp`.
- Do not publish to upstream namespaces: `ghcr.io/drop-oss/*`, the `drop-oss-app*` AUR
  packages, or `droposs.org` assets.
- Keep the AGPL notices intact. `LICENSE` and `NOTICE` are not disposable.

## Branching and commits

- `main` is the default branch and stays deployable.
- `feature/<slug>` for features, `fix/<slug>` for bugs, `chore/<slug>` for tooling and
  docs, `port/<slug>` for a module ported from the old server.
- Conventional commit subjects: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`,
  `build:`, `chore:`.
- The body explains why, not what. The diff already says what.
- Reference the roadmap phase in the body when a change completes an item.

## Writing

Documentation and commit messages follow the same rules:

- No em dashes.
- No marketing language, no "seamless", "effortless", "powerful".
- No invented statistics, benchmarks or user counts.
- State status plainly: implemented, planned, or not started.
- Prefer concrete numbers, file paths and endpoint names over adjectives.

## When unsure

Ask before deleting or rewriting anything you did not create, especially:

- Files under `libraries/` (the desktop depends on them).
- Anything that looks like uncommitted user work.
- The schema baseline and any recorded fixtures.
- The wording of `NOTICE`, `LICENSE` or the fork boundary rules.

If a task requires changing the database schema, the client contract or a depot format,
stop and treat it as an architecture decision: write it down in `docs/adr/` first.
