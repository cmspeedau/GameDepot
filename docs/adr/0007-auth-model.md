# 0007: Auth model: sessions and pairing tokens

- Status: Accepted
- Date: 2026-11-15
- Deciders: maintainers

## Context

There are two consumers: a browser dashboard and a desktop client. They need different
credentials, but both should be simple to revoke and simple to reason about.

## Decision

- Dashboard: `gamedepot_session` cookie, 32 random bytes, stored as a SHA-256 hash, with
  a per-session CSRF secret.
- Pairing: an eight-character code, approved by an admin, claimed once for a 32-byte
  client token, stored hashed.
- Client requests use `Authorization: Bearer <token>`.
- Client tokens carry a scope list. Revocation deletes the client row.
- No X.509 certificates and no JWT. The pairing flow is polled, not pushed over a
  WebSocket.
- Argon2id for passwords.

## Consequences

- One credential type per consumer. No certificate authority to operate.
- Revocation is a row deletion.
- Pairing is stateless on the client side: code, poll, token.
- Tokens are high entropy, so SHA-256 is sufficient for storage. Passwords still use
  Argon2id.

## Alternatives considered

- **X.509 client certificates with ES384 JWT.** Stronger, but adds a CA, a certificate
  store and JWT verification for no gain at this scale.
- **Pushing the token over a WebSocket.** Lower latency, but adds a socket and a
  listener lifecycle. Polling is simpler and good enough for a ten-minute code.
- **Opaque session tokens for the desktop.** Reuses one mechanism, but couples desktop
  revocation to session expiry.
