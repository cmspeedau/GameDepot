## What changed

<!-- One paragraph. What the change does, in plain terms. -->

## Why

<!-- The problem, the failing case, or the roadmap item this completes. -->

Roadmap phase:

## How it was verified

<!-- Commands run, tests added, and what was checked by hand. -->

- [ ] `go build ./...` and `go vet ./...` (if backend changed)
- [ ] `go test ./...` (if backend changed)
- [ ] `pnpm typecheck` and `pnpm build` (if desktop UI changed)
- [ ] `cargo check` and `cargo test` (if Rust changed)
- [ ] Format fixtures pass (if a depot, manifest or chunk format changed)
- [ ] Dashboard page loaded and a form submitted (if templates changed)

## Compatibility

<!-- Delete what does not apply. -->

- [ ] No schema change
- [ ] No change to the client HTTP contract
- [ ] No change to a depot or manifest format
- [ ] No change to session, token or JWT handling

If any box above cannot be ticked, link the ADR that covers it:

## Screenshots

<!-- Dashboard or desktop changes: before and after. Delete if not visual. -->

## Notes for review

<!-- Anything a reviewer should look at first, or known gaps. -->
