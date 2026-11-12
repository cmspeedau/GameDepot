# GameDepot

[![GitHub License](https://img.shields.io/badge/AGPL--3.0-red?style=for-the-badge)](LICENSE)

GameDepot is a self-hosted, open-source game distribution platform and modern Steam/GameVault alternative. It allows you to preserve, store, and stream your DRM-free game library directly to your devices and friends with high transfer efficiency and zero-config mesh networking.

> [!NOTE]
> **GameDepot is an independent hard fork of [Drop OSS](https://github.com/Drop-OSS/drop).**
> While upstream Drop has entered maintenance mode with a strict policy against AI contributions, GameDepot embraces AI-assisted engineering and rapid experimentation to evolve the platform with automated game ingestion, next-generation compression, and flexible mesh networking.

---

## Key Features & Roadmap

- **Multi-Store Game Ingestion & Updates:**
  - Automated integration with **GOG** and **Epic Games Store** (via OAuth & CLI backends like `gogdl` and `legendary`).
  - Keep game releases, patches, and DLC automatically downloaded and synchronized in your personal depot.

- **Next-Gen Storage & Chunked Delivery:**
  - Fast chunking engine powered by **Zstandard (seekable zstd)** and content-addressed storage.
  - Granular delta patching and deduplication inspired by Steam depots, saving massive bandwidth and disk space.
  - Multi-disc and multi-part installer support (e.g. vintage 2+ CD-ROM games).

- **Pluggable Mesh Networking & Virtual LAN:**
  - Integrated peer-to-peer mesh connectivity for zero-config remote downloads and LAN multiplayer.
  - Pluggable VPN driver architecture supporting both **NetBird** and **Headscale**.

- **Open & Unrestricted Engineering:**
  - AI-assisted development friendly.
  - Rapid release cycle without upstream bureaucracy.

---

## Architecture Overview

GameDepot is structured as a modern monorepo:

- **Desktop Client:** Tauri + Svelte/TypeScript client providing a native desktop experience.
- **Backend Server:** Node.js/TypeScript backend managing depot metadata, authentication, storage providers, and mesh orchestration.
- **Transfer Engine (`droplet`):** High-performance Rust binary handling chunked, concurrent game file streaming and extraction.

---

## Docker Environment Variables

Copy `.env.example` to `.env` and adjust the values. Docker Compose reads the
interpolation variables below from `.env` (or the shell), while the container
runtime variables are set through the `environment` block in
`docker-compose.yml` or with `docker run -e`.

> [!NOTE]
> `.env.example` still lists the legacy `JWT_SECRET`, `AUTH_SECRET`,
> `GAMEDEPOT_PORT`, `LIBRARY_PATH`, and `DATA_PATH` entries. The current Docker
> setup does not read them; use the variable names documented here instead. Its
> `DATABASE_URL` is also ignored, because Compose builds that value from the
> `POSTGRES_*` variables.

### Compose & build variables (`.env`)

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `4000` | Host port published for the web UI and API (`${PORT}:4000`). |
| `POSTGRES_USER` | `gamedepot` | PostgreSQL user created for and used by GameDepot. |
| `POSTGRES_PASSWORD` | `gamedepot_secret` | PostgreSQL password. Change it for any real deployment. |
| `POSTGRES_DB` | `gamedepot` | PostgreSQL database name. |
| `GAMEDEPOT_DATA_PATH` | `./data` | Host path mounted at `/data` (database-adjacent state, nginx prefix, depot CAS). |
| `GAMEDEPOT_LIBRARY_PATH` | `./games` | Host path mounted at `/library` (the game library). |
| `EXTERNAL_URL` | `http://localhost:4000` | Public base URL used for links, WebAuthn, and OAuth/OIDC callbacks. Set this to your real hostname in production. |
| `GAMEDEPOT_CREDENTIAL_KEY` | *(required)* | 32-byte hex key (`openssl rand -hex 32`) that encrypts stored store credentials at rest. Credential writes fail closed without it; rotating it orphans already-sealed rows. |
| `EA_OAUTH_CLIENT_ID` | *(unset)* | Optional EA/Origin OAuth client ID. Commented out in `docker-compose.yml`; only needed for browser sign-in and token renewal for the EA tab. |
| `EA_OAUTH_CLIENT_SECRET` | *(unset)* | Optional EA/Origin OAuth client secret (requires the ID and redirect URI). |
| `EA_OAUTH_REDIRECT_URI` | *(unset)* | Optional EA/Origin OAuth redirect URI registered with your client. |
| `BUILD_DROP_VERSION` | `v0.4.0-local` | Build arg: version string shown in the UI and update checks. Local builds default to a `-local` tag. |
| `BUILD_GIT_REF` | `local` | Build arg: git ref shown in the UI. |

### Container runtime variables

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | built by Compose | PostgreSQL connection string. Compose builds it from `POSTGRES_USER`, `POSTGRES_PASSWORD`, and `POSTGRES_DB`. |
| `EXTERNAL_URL` | `http://localhost:3000` | Public base URL when running outside Compose. Compose sets it from the `.env` value. |
| `GAMEDEPOT_CREDENTIAL_KEY` | *(required)* | Same credential-encryption key as above, passed through to the container. |
| `CHECK_FOR_UPDATES` | `true` in production | Set to `false` to disable update checks, or `true` to force them on outside production. |
| `METADATA_TIMEOUT` | `5000` | Timeout in milliseconds for external metadata provider requests. |
| `DISABLE_SIMPLE_AUTH` | *(unset)* | When set, disables built-in username/password authentication. Use it with OIDC. |
| `WEBAUTHN_DOMAIN` | `EXTERNAL_URL` | Relying-party domain for WebAuthn/passkeys, when it differs from `EXTERNAL_URL`. |
| `OIDC_WELLKNOWN` | *(unset)* | OIDC discovery document URL. When set, the individual endpoint variables below are not needed. |
| `OIDC_AUTHORIZATION` | *(unset)* | OIDC authorization endpoint (alternative to `OIDC_WELLKNOWN`). |
| `OIDC_TOKEN` | *(unset)* | OIDC token endpoint (alternative to `OIDC_WELLKNOWN`). |
| `OIDC_USERINFO` | *(unset)* | OIDC userinfo endpoint (alternative to `OIDC_WELLKNOWN`). |
| `OIDC_JWKS` | *(unset)* | OIDC JWKS endpoint (alternative to `OIDC_WELLKNOWN`). |
| `OIDC_ISSUER` | *(unset)* | OIDC issuer URL. |
| `OIDC_SCOPES` | *(unset)* | Comma-separated OIDC scopes. Required when not using `OIDC_WELLKNOWN`. |
| `OIDC_CLIENT_ID` | *(unset)* | OIDC client ID. |
| `OIDC_CLIENT_SECRET` | *(unset)* | OIDC client secret. |
| `OIDC_PROVIDER_NAME` | *(unset)* | Display name for the OIDC provider on the login page. |
| `OIDC_USER_GROUP` | *(unset)* | Group claim value that grants normal user access. |
| `OIDC_ADMIN_GROUP` | *(unset)* | Group claim value that grants admin access. |
| `OIDC_USERNAME_CLAIM` | `preferred_username` | UserInfo claim used as the username. |
| `OIDC_REQUIRE_HTTPS` | `true` | Set to `false` to allow non-HTTPS OIDC endpoints (not recommended in production). |
| `OIDC_PKCE` | `true` | Set to `false` to disable PKCE for the OIDC flow. |
| `GAMEDEPOT_INGEST_URL` | *(unset)* | External GOG ingestion dispatch URL. When unset, GOG acquisition runs locally as a server task. |
| `GAMEDEPOT_EPIC_INGEST_URL` | `GAMEDEPOT_INGEST_URL` | External Epic ingestion dispatch URL. Epic acquisition is unavailable when neither is set. |
| `GAMEDEPOT_INGEST_TOKEN` | *(unset)* | Bearer token sent to the ingestion dispatch URLs. |
| `IGDB_CLIENT_ID` | *(unset)* | IGDB (Twitch) client ID for metadata lookups. |
| `IGDB_CLIENT_SECRET` | *(unset)* | IGDB (Twitch) client secret for metadata lookups. |
| `PCGAMINGWIKI_BOT_USERNAME` | *(unset)* | PCGamingWiki bot username for metadata lookups. |
| `PCGAMINGWIKI_BOT_PASSWORD` | *(unset)* | PCGamingWiki bot password for metadata lookups. |

---

## Contributing

We welcome all community contributions, feature discussions, bug reports, and pull requests! AI-assisted tooling and contributions are fully permitted and supported.

## License

This project is licensed under the [AGPL-3.0 License](LICENSE), honoring the original licensing of Drop OSS.
