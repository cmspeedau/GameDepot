-- name: GetClientByTokenHash :one
SELECT id, user_id, name, platform, token_hash, scopes, created_at, last_seen_at, revoked_at
FROM clients
WHERE token_hash = $1;

-- name: GetClientByID :one
SELECT id, user_id, name, platform, token_hash, scopes, created_at, last_seen_at, revoked_at
FROM clients
WHERE id = $1;

-- name: ListClientsForUser :many
SELECT id, user_id, name, platform, token_hash, scopes, created_at, last_seen_at, revoked_at
FROM clients
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListAllClients :many
SELECT id, user_id, name, platform, token_hash, scopes, created_at, last_seen_at, revoked_at
FROM clients
ORDER BY created_at DESC;

-- name: CreateClient :one
INSERT INTO clients (id, user_id, name, platform, token_hash, scopes)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, user_id, name, platform, scopes, created_at, last_seen_at;

-- name: RevokeClient :exec
UPDATE clients SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: TouchClient :exec
UPDATE clients SET last_seen_at = now() WHERE id = $1;

-- name: CreatePairingCode :exec
INSERT INTO pairing_codes (code_hash, name, platform, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetPairingCode :one
SELECT code_hash, name, platform, scopes, created_at, expires_at, approved_by, approved_at, claimed_at, client_id
FROM pairing_codes
WHERE code_hash = $1;

-- name: ApprovePairingCode :execrows
UPDATE pairing_codes
SET approved_by = $2, approved_at = now()
WHERE code_hash = $1 AND approved_at IS NULL AND expires_at > now();

-- name: ClaimPairingCode :execrows
UPDATE pairing_codes
SET claimed_at = now(), client_id = $2
WHERE code_hash = $1 AND approved_at IS NOT NULL AND claimed_at IS NULL AND expires_at > now();

-- name: ListPendingPairingCodes :many
SELECT code_hash, name, platform, scopes, created_at, expires_at, approved_by, approved_at, claimed_at, client_id
FROM pairing_codes
WHERE claimed_at IS NULL
ORDER BY created_at DESC;

-- name: DeleteExpiredPairingCodes :execrows
DELETE FROM pairing_codes WHERE expires_at <= now() AND claimed_at IS NULL;
