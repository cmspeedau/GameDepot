-- name: CreateSession :one
INSERT INTO sessions (id, user_id, token_hash, csrf_secret, user_agent, ip, remember, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, user_id, created_at, expires_at, last_seen_at;

-- name: GetSessionByTokenHash :one
SELECT id, user_id, token_hash, csrf_secret, remember, created_at, expires_at, last_seen_at
FROM sessions
WHERE token_hash = $1 AND expires_at > now();

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteSessionsForUser :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
