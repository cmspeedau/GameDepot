# 0002: Go and Gin for the server

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The server must be small to deploy, fast to start, and single-process. It serves JSON
to the desktop client, HTML to a browser, and large binary objects over the same port.

## Decision

- The server is Go 1.26 with Gin.
- One binary serves the client API, the dashboard and the depot.
- PostgreSQL access uses pgx v5 and sqlc.
- Logging uses `log/slog`.
- Nuxt, Nitro, Node and nginx are not used.

## Consequences

- No Node in the server image. The server image is a static binary plus assets.
- Routes are registered explicitly in code, one group per domain.
- Every endpoint is implemented and tested here.
- Go has no runtime validation library comparable to ArkType. Request validation is
  written per handler with explicit DTOs.

## Alternatives considered

- **Node with Fastify.** Keeps a familiar ecosystem but keeps Node in the image and
  does not remove the UI/API coupling.
- **Rust with Axum.** One language for server and desktop, but a slower iteration cycle
  for a web surface. The Rust investment stays in the desktop core.
