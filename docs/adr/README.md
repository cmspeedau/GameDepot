# Architecture decision records

Short records of decisions that shape the codebase. Each one states the context, the
choice, and what it costs. When a decision changes, add a new record that supersedes the
old one instead of rewriting history.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-go-gin-server.md) | Replace the Node/Nuxt server with Go and Gin | Accepted |
| [0002](0002-server-rendered-dashboard.md) | Server-rendered dashboard with HTMX, no SPA | Accepted |
| [0003](0003-depot-in-go.md) | Move the depot data plane into Go, retire torrential | Accepted |
| [0004](0004-frozen-schema-sqlc.md) | Freeze the Prisma schema, use sqlc with pgx | Accepted |
| [0005](0005-desktop-vue-vite.md) | Desktop UI on Vue 3 and Vite, Tauri Rust core kept | Accepted |
| [0006](0006-compatibility-contract.md) | Treat schema, cookies, JWTs and depot formats as contracts | Accepted |

New records start from `template.md`.
