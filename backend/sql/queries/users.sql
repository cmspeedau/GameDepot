-- name: CreateUser :one
INSERT INTO users (id, username, display_name, password_hash, admin)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, username, display_name, admin, disabled, created_at, updated_at;

-- name: GetUserByUsername :one
SELECT id, username, display_name, password_hash, admin, disabled, created_at, updated_at
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, display_name, password_hash, admin, disabled, created_at, updated_at
FROM users
WHERE id = $1;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: ListUsers :many
SELECT id, username, display_name, admin, disabled, created_at, updated_at
FROM users
ORDER BY created_at ASC;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: SetUserDisabled :exec
UPDATE users SET disabled = $2, updated_at = now() WHERE id = $1;
