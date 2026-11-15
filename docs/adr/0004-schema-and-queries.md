# 0004: New schema with goose and sqlc

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The project needs a relational schema for users, sessions, games, versions, releases,
playtime and the rest. It is a new database, so there is no migration to preserve.

## Decision

- The schema is defined in goose migrations under `backend/sql/migrations`.
- pgx v5 is the driver. No ORM.
- sqlc generates Go types from hand-written SQL in `sql/queries`.
- Migrations run from a `migrate` command and on boot when
  `GAMEDEPOT_AUTO_MIGRATE=true`.

## Consequences

- Adding a column is a normal migration, not a compatibility event.
- sqlc gives compile-time checked queries and no reflection.
- JSON and array columns are decoded in Go, because sqlc does not know the shapes.
- Dynamic filters are assembled with care, since sqlc does not generate them.

## Alternatives considered

- **GORM.** Fastest to write, but heavy queries end up in raw SQL anyway.
- **ent.** Good relation traversal, but it owns the schema and wants to generate
  migrations.
- **Prisma.** Requires Node at runtime, which ADR-0002 rejects.
