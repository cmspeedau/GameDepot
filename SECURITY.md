# Security policy

## Reporting a vulnerability

Do not open a public issue for a security problem.

Report privately through GitHub's private vulnerability reporting on the repository
(Security tab, "Report a vulnerability"), or by email to the maintainer listed in the
repository profile. Include the affected commit, the component, reproduction steps and
the impact you believe it has.

Expect an acknowledgement within a few days. There is no bounty program. Credit is
given in the advisory unless you ask otherwise.

## Supported versions

The project has no release yet. Once releases exist, the latest minor version receives
security fixes. Older tags do not.

## Threat model

GameDepot runs on a home server or a small VPS, reachable by its owner and a handful of
users. Assets worth protecting:

- User passwords and session cookies.
- Client tokens held by paired desktops.
- The library, which may include unreleased or personal content.
- The host, since the server reads and writes large trees on disk.
- Store credentials, once ingestion exists.

Assumed adversary capabilities:

- A remote unauthenticated attacker who can reach the HTTP port.
- A signed-in user with no admin rights, trying to read another user's data or escalate.
- A paired desktop client that has been tampered with.
- Malicious or compromised metadata and CDN responses, once ingestion exists.
- A stolen database backup without the filesystem volumes.

Out of scope:

- An attacker with root on the host.
- Physical access to the server.
- Traffic analysis when TLS is not enabled.
- Denial of service by saturating the network link.

## Controls

### Dashboard

- Session cookie `gamedepot_session` is `HttpOnly`, `SameSite=Lax`, and `Secure` when the
  external URL is HTTPS.
- The database stores a SHA-256 hash of the session token, never the token.
- Every state-changing form carries a CSRF token bound to the session, compared in
  constant time, and rotated on sign-in.
- Sign-in responses do not reveal whether a username exists. Password verification uses
  Argon2id.
- Failed sign-ins, setup attempts and pairing claims are rate limited per IP and per
  account.
- Destructive actions require a recent re-authentication.
- Responses set `Content-Security-Policy` without inline script, plus
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `X-Frame-Options: DENY`.

### Client API

- Client tokens are 32 random bytes, shown once at pairing, stored as a SHA-256 hash.
- A token is bound to one client row and one user. Revoking the client takes effect on
  the next request.
- Pairing codes are eight characters, single use, valid for ten minutes, and bound to one
  pending device. Approval requires an authenticated admin session.
- The setup token exists only until setup completes, is printed once, and is deleted on
  use. While setup is incomplete, all non-setup routes return 503.
- Scope checks happen before any data access. A missing scope is 403, not an empty list.
- Client tokens cannot reach dashboard or admin routes.

### Depot and files

- Manifest paths are normalized and rejected if they contain `..`, a leading slash, a
  backslash, NUL, an empty segment or a duplicate.
- Library scans do not follow symlinks outside the source root.
- Chunk references are range-checked against the object size before any read.
- Object identity is the SHA-256 of the plaintext. A client verifies ciphertext hash,
  decrypts, and verifies plaintext hash before writing.
- AES-256-GCM uses a 32-byte key per release and a 12-byte nonce per object. The nonce is
  never reused with different plaintext, because object identity deduplicates plaintext
  and each release has its own key.
- Objects and manifests are written to a temp file, fsynced and renamed. Readers never
  see a partial file.
- The release key travels in the manifest, which is only served to clients with access to
  the game. Encryption protects data at rest and against a leaked object URL, not
  against an authorized client.

### Store adapters (phase 7)

- Adapter responses are untrusted. Sizes, paths and hashes are validated before a write.
- Store credentials are sealed with a 32-byte key from `GAMEDEPOT_CREDENTIAL_KEY`. A
  missing key fails closed.
- Redirects during a CDN download are restricted to expected hosts, and the final host is
  checked before streaming.
- Provider build id, manifest id and content digest stay distinct, so a manipulated
  manifest cannot make one build masquerade as another.
- Adapters authenticate only to the endpoints their own acquisition flow needs. A
  stored provider credential is never offered to a provider's save-sync service:
  GameDepot does not read or write Ubisoft cloud saves, so a stolen or abused
  credential cannot silently touch a player's saved games on the provider side.

### Logging

- Credentials, tokens, cookies, signed URLs and release keys are never logged.
- Request logs include method, path, status, duration and a request id. Body content is
  not logged.
- Errors reaching a client are generic. Detail stays in the server log under the request
  id.

## Deployment guidance

- Put the server behind a TLS-terminating proxy. Do not expose it over plain HTTP to an
  untrusted network.
- Set `EXTERNAL_URL` to the public HTTPS origin. The dashboard uses it for `Secure`
  cookies and absolute links.
- Back up PostgreSQL and the `DATA` volume together. Manifests reference objects by hash,
  so a partial restore produces broken releases.
- Run as a non-root user that owns the library and data directories.
- Restrict the dashboard to trusted networks if the instance is only administered from
  home.

## Known limits

- Single instance only. Tasks and progress are process-local.
- No range requests in v1. A client resumes at object granularity.
- GameDepot's cloud saves coordinate only between GameDepot clients. A title whose own
  storefront also syncs saves (Ubisoft Connect, EA app, and others) has no knowledge of
  this feature, and neither does the reverse: running both writers on the same save
  directory is unsupported by design, not merely untested. Turn the provider's own cloud
  saves off for any title using ours, per game.

## Disclosure

Fixes ship in a patch release with an advisory describing the affected component, the
impact and the upgrade step. Reporters are credited unless they prefer not to be.
