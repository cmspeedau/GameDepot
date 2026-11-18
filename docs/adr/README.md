# Architecture decision records

Short records of decisions that shape the codebase. Each one states the context, the
choice, and what it costs. When a decision changes, add a new record that supersedes the
old one instead of rewriting history.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-independence.md) | Independence and no code reuse | Accepted |
| [0002](0002-go-gin-server.md) | Go and Gin for the server | Accepted |
| [0003](0003-server-rendered-dashboard.md) | Server-rendered dashboard with HTMX | Accepted |
| [0004](0004-schema-and-queries.md) | New schema with goose and sqlc | Accepted |
| [0005](0005-depot-format.md) | New depot format, `gamedepot-release` v1 | Accepted |
| [0006](0006-desktop-client.md) | Desktop client: Tauri with Vue 3 and Vite | Accepted |
| [0007](0007-auth-model.md) | Auth model: sessions and pairing tokens | Accepted |
| [0008](0008-chunk-size.md) | Chunk size, 4 MiB for v1 | Accepted |
| [0009](0009-legacy-archive-as-spec.md) | The archived GameDepot is our own history, not a third party | Accepted |
| [0010](0010-gog-platform-handling.md) | GOG platform handling; Linux builds refused, not downgraded | Accepted |
| [0011](0011-ubisoft-ingestion-and-protos.md) | Ubisoft ingestion, and the vendored protocol schemas | Accepted |
| [0012](0012-launch-posture-and-drm.md) | Launch posture and DRM: no circumvention, two modes | Accepted |

New records start from `template.md`.
