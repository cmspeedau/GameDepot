# 0001: Replace the Node/Nuxt server with Go and Gin

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The upstream server is a Nuxt application. Nuxt is both the browser UI and the API
runtime: Nitro serves `server/api` route files, Prisma runs inside that process, and the
Nuxt build produces the web client. The package is around 31,700 lines of TypeScript
excluding tests, across roughly 224 route files, four WebSocket handlers, and a service
supervisor that spawns the Rust depot process.

Three properties of that arrangement are costly for this fork:

1. The server cannot start without a Node runtime and a built Nitro bundle.
2. The browser UI and the API share one package, so removing the UI means untangling the
   API from Nitro conventions (`defineEventHandler`, h3 utilities, auto-imported server
   utils).
3. The process supervisor exists to manage a child process and a reverse proxy. Both are
   avoidable.

The fork wants a small deployment: one binary, one database, two volumes.

## Decision

Rewrite the server in Go 1.26 with Gin.

- One binary serves the client API, the depot data plane and the dashboard.
- PostgreSQL access uses pgx v5.
- Structured logging uses `log/slog`.
- Nuxt and Nitro are removed entirely. The browser surface is rebuilt as described in
  ADR-0002.
- The API is registered explicitly in code, one route group per domain, so there is no
  file-routing convention to learn.

## Consequences

- Deployment needs no Node. The server image is a static binary plus assets.
- Startup is fast, memory use is predictable, and one process can be profiled with
  `net/http/pprof`.
- Every endpoint must be reimplemented, with tests. This is the bulk of the work.
- Go's type system does not replace the ArkType validation the old handlers used. Request
  validation is written per handler, and DTOs are explicit.
- The team must keep two toolchains, Go and Rust, and a third one, Node, only for the
  desktop build.
- Existing TypeScript server utilities (metadata providers, store adapters) have to be
  ported rather than reused.

## Alternatives considered

- **Keep Nuxt and port only selected modules.** Least work, but keeps Node in production
  and keeps the UI/API coupling that this fork is trying to remove.
- **Node with Fastify, no Nuxt.** Removes Nitro but keeps Node, and still requires porting
  the UI package structure.
- **Rust with Axum.** One language for server and desktop, but a much slower iteration
  cycle for a large web surface, and a smaller contributor pool for HTTP work. The Rust
  investment stays where it already pays off: the desktop core.

## References

- Upstream `server/nuxt.config.ts`, `server/server/api/`, `server/server/internal/`.
- ADR-0002, ADR-0003, ADR-0004.
