# 0001: Independence and no code reuse

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

GameDepot is an independent project. The implementation, schema, formats and client
protocol are defined by this repository. There is no compatibility target with any
other game distribution project.

## Decision

- No source code from another game distribution project is copied or translated into
  this repository.
- Reading another implementation for ideas is allowed. Translating its source is not.
- If a dependency is added, its license is checked and recorded in `NOTICE`.
- If code is ever copied from an AGPL project, it remains AGPL and its attribution is
  added to `NOTICE`. The default rule is not to copy.

## Consequences

- The implementation is slower to build than a port, because every module is written
  here.
- There is no compatibility layer and no upstream contract to satisfy.
- Contributors have a clear rule to apply in review.
- The project keeps its own license choice, AGPL-3.0, as a deliberate decision.

## Alternatives considered

- **Port from an existing implementation.** Faster, but it ties the project to a contract
  it does not control and creates a provenance obligation. Rejected.
- **Clean-room rewrite with a spec team.** Stronger legal position, but impractical
  for a small project. The no-copy rule in this record is the workable version.

## References

- `AGENT.md`, `CONTRIBUTING.md`.
