# 0002: Server-rendered dashboard with HTMX, no SPA

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The Nuxt UI is the only place where several flows exist today:

- First-run setup and account creation.
- Invitation creation and redemption.
- Desktop pairing approval, either by code or by an authorize link.
- Catalog, library and store administration.
- User, session and token management.

Players use the Tauri desktop client. They do not need a browser library, store or
account page. That means the browser surface is almost entirely administrative, used by
one or two people on a home server, often from a laptop or phone on the same network.

An administrative surface changes rarely, has few concurrent users, and benefits more
from being easy to inspect and debug than from client-side interactivity.

## Decision

Build the browser surface as server-rendered HTML from Gin.

- `html/template` for layout and pages.
- HTMX for partial updates, vendored locally.
- Tailwind CSS compiled ahead of time with the standalone CLI.
- Session cookie `drop-token`, CSRF token on every form, POST plus redirect for state
  changes.
- Scope: setup, sign-in, invitations, users, sessions, pairing, catalog, library, depot,
  ingestion, tasks, saves administration, settings.
- No browser player experience, no public store, no news or community pages.

## Consequences

- No client-side build or router. A page is a handler and a template.
- No JSON admin API has to be designed, documented or versioned.
- Tailwind is the only frontend build step, and it can run in CI or be committed as a
  compiled file.
- Interaction is coarser: inline edits reload a fragment, long operations poll a fragment
  instead of pushing over a WebSocket.
- Templates are not type checked. A missing field fails at render time, so handler tests
  must render pages.
- The dashboard and the desktop share design tokens but not components. Consistency comes
  from the token vocabulary, not from a shared library.

## Alternatives considered

- **Keep the Nuxt SPA for admin only.** Reuses 57 existing pages, but keeps Node, Nitro
  and a JSON API surface, which contradicts ADR-0001.
- **Admin screens inside the Tauri client.** One UI, but administration then requires the
  desktop app, and roughly 100 endpoints would need Rust UI work.
- **CLI and API only.** Smallest server, but setup, pairing and catalog management become
  shell workflows that most operators will not use.

## References

- Upstream `server/pages/`, `server/components/`, `server/composables/request.ts`.
- ADR-0001, ADR-0005.
