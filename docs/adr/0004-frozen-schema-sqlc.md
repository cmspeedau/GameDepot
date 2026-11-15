# 0004: Freeze the Prisma schema, use sqlc with pgx

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

The database has 37 models and 12 enums across 131 Prisma migrations. Existing installs
have real data: users, sessions, API tokens, games, versions, depot manifests, save
history. A migration bug during a server swap would be unrecoverable for an operator
without a database backup.

Prisma is a Node runtime. A Go server cannot use it, and reimplementing its migration
engine is pointless for a schema that is not changing.

The query surface is uneven. Many admin reads are simple lookups. A few paths are heavy:
store listing with tag and platform filters, library scans, save manifest assembly, and
delta version resolution. Those benefit from hand-written SQL with visible plans.

## Decision

- **Freeze the schema.** Go adopts the exact tables, columns, enum labels, array columns
  and indexes that the Prisma migrations produced. No new migration is required to run
  the Go server.
- **Generate a baseline once.** `sql/schema.sql` is produced from the 131 migrations. A
  fresh database applies it. An existing database is detected and left alone.
- **goose records a version only.** The baseline migration checks for an existing schema
  and, when present, marks itself applied without DDL.
- **sqlc generates Go from hand-written SQL.** Queries live in `sql/queries`, one file per
  domain. Types are generated, not hand-mirrored.
- **pgx v5 directly.** No ORM. Transactions use a small helper so handlers do not manage
  `pgx.Tx` lifecycle.

Postgres-only features are not required: the schema uses enums, arrays and JSON columns,
but no full-text search, no `tsvector`, and no extensions beyond what a default install
provides.

## Consequences

- An existing install swaps binaries with no schema step and no re-login.
- The fork cannot add columns without breaking the freeze. Any new feature that needs a
  column requires an explicit decision to unfreeze, recorded in a new ADR.
- sqlc gives compile-time checked queries and no reflection. The cost is that every query
  is SQL that must be maintained, and dynamic filters must be assembled with care.
- JSON and array columns are read as `[]byte` or `pgtype` and decoded in Go, because sqlc
  does not know the Prisma shapes.
- Enum labels are duplicated in Go constants. A test compares them against the database
  enum values so drift is caught.

## Alternatives considered

- **GORM.** Fastest to write, but struct tags would mirror a frozen schema anyway, and the
  heavy listing queries would end up in raw SQL regardless.
- **ent.** Good relation traversal, close to Prisma, but it owns the schema and wants to
  generate migrations, which conflicts with the freeze.
- **Prisma via a sidecar.** Keeps the schema and query layer, but keeps Node in the image,
  which ADR-0001 rejects.

## References

- Upstream `server/prisma/models/*.prisma`, `server/prisma/migrations/`.
- ADR-0001, ADR-0006.
