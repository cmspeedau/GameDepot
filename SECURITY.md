# Security policy

## Reporting a vulnerability

Do not open a public issue for a security problem.

Report privately through GitHub's private vulnerability reporting on the repository
(Security tab, "Report a vulnerability"), or by email to the maintainer listed in the
repository profile. Include:

- The affected version or commit.
- The component: server, dashboard, depot, desktop client, or a store adapter.
- Reproduction steps, and the impact you believe it has.
- Whether the issue is already public anywhere.

Expect an acknowledgement within a few days. There is no bounty program. Credit is
given in the advisory unless you ask otherwise.

## Supported versions

The fork has no release yet. Once releases exist, the latest minor version receives
security fixes. Older tags do not.

## Threat model

GameDepot is usually deployed on a home server or a small VPS, reachable by its owner and
a handful of users. The assets worth protecting are:

- User credentials and session tokens.
- Paired client private keys and certificates.
- Store credentials for GOG, Epic, Ubisoft and EA.
- The library itself, which may include unreleased or personal content.
- The host, since the server reads and writes large trees on disk.

Assumed adversary capabilities:

- A remote unauthenticated attacker who can reach the HTTP port.
- A signed-in user with no admin rights, trying to read another user's data or escalate.
- A paired desktop client that has been tampered with.
- Malicious or compromised store metadata and CDN responses.
- A stolen database backup, without the filesystem volumes.

Explicitly out of scope:

- An attacker with root on the host.
- Physical access to the server.
- Traffic analysis of an operator who did not enable TLS.
- Denial of service by someone who can saturate the network link.

## Controls

### Dashboard

- Session cookie `drop-token` is `HttpOnly`, `SameSite=Lax`, and `Secure` when the
  external URL is HTTPS.
- Every state-changing form carries a CSRF token bound to the session, compared in
  constant time.
- Sign-in responses do not reveal whether a username exists.
- Passwords use Argon2id. Legacy bcrypt hashes verify and are upgraded on sign-in.
- Destructive actions require a recent superlevel elevation.
- Dashboard responses set `Content-Security-Policy` without inline script, plus
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and
  `X-Frame-Options: DENY`.

### Client API

- Client requests are authenticated by ES384 JWT over the certificate stored for that
  client id. The client id in the header must match the certificate's client.
- A certificate row and its client row are the revocation unit. Deleting either ends
  access.
- Tokens have a short lifetime and are checked for expiry, not just signature.
- Pairing codes are short-lived, single use, and bound to one pending client. Approval
  requires an authenticated admin session.
- The setup token exists only until the first successful setup, is printed once, and is
  deleted on use.
- API tokens are stored as hashes where the schema allows it, never logged, and scoped
  by ACL string.

### Depot and files

- Manifest paths are normalized and rejected if they contain `..`, a leading slash, a
  backslash, NUL, or an empty segment. Duplicate paths are rejected.
- Library scans do not follow symlinks outside the source directory.
- Chunk reads range-check the requested offset and length against the manifest entry.
  No arithmetic on client input reaches a file offset unchecked.
- All writes go to a temporary file, are fsynced, then renamed. A reader never sees a
  partial object or manifest.
- Depot content is encrypted with a per-release AES-128 key carried only in the
  manifest, so a leaked URL alone does not disclose plaintext.

### Store adapters

- Adapter responses are untrusted input. Sizes, paths and hashes are validated before
  anything is written.
- Store credentials are sealed with a 32-byte key from `GAMEDEPOT_CREDENTIAL_KEY`. A
  missing key fails closed: credential writes are refused rather than stored in clear.
- Redirects are followed only to expected hosts during a CDN download, and the final
  host is checked before streaming.
- Provider build id, manifest id and content digest stay distinct, so a manipulated
  manifest cannot make one build masquerade as another.

### Logging

- Credentials, tokens, cookies, signed URLs and certificate private keys are never
  logged.
- Request logs include method, path, status, duration and a request id. Body content is
  not logged.
- Errors reaching a client are generic. Detail stays in the server log, keyed by request
  id.

## Deployment guidance

- Put the server behind a TLS-terminating proxy. Do not expose it over plain HTTP to an
  untrusted network.
- Set `EXTERNAL_URL` to the public HTTPS origin. The dashboard uses it for `Secure`
  cookies and absolute links.
- Keep `GAMEDEPOT_CREDENTIAL_KEY` out of the repository and out of backups that are not
  encrypted. Rotating it orphans existing sealed rows.
- Back up PostgreSQL and the `DATA` volume together. Depot manifests reference objects
  by hash, so a partial restore produces broken releases.
- Run as a non-root user that owns the library and data directories.
- Restrict the dashboard to trusted networks if the instance is only administered from
  home.

## Known limits

- The task and notification hub is process-local. Running two instances against one
  database is unsupported.
- Store adapters speak to undocumented endpoints. A provider change can break ingestion;
  it does not create a server-side data exposure, because validation happens before
  writes.
- Old password hashes are bcrypt. Support is kept for compatibility and should be
  removed once a migration window has passed.

## Disclosure

Fixes ship in a patch release with an advisory describing the affected component, the
impact, and the upgrade step. Reporters are credited unless they prefer not to be.
