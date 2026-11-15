# 0003: Server-rendered dashboard with HTMX

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The browser surface is administrative: setup, users, invitations, clients, catalog,
library, releases, tasks and settings. It is used by one or two people on a home
server. Players use the desktop client.

## Decision

- The dashboard is server-rendered HTML from Gin.
- Templates use `html/template`.
- HTMX is vendored locally and used for partial updates.
- Tailwind CSS is compiled ahead of time with the standalone CLI.
- Session cookie `gamedepot_session`, CSRF on every form, POST plus redirect.
- No browser player experience, no public store, no community pages.

## Consequences

- No client-side build or router. A page is a handler and a template.
- No JSON admin API to design, document or version.
- Tailwind is the only frontend build step.
- Templates are not type checked, so handlers must render pages in tests.
- Interaction is coarser than a SPA: fragments and polling instead of client state.

## Alternatives considered

- **A SPA for administration.** Reuses component libraries but adds a build, a router
  and a JSON API surface for no benefit.
- **Admin screens inside the desktop client.** Administration then requires the desktop
  app, and the API grows to support it.
- **CLI and API only.** Smallest server, but setup, pairing and catalog management
  become shell workflows.
