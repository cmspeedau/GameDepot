-- name: CreateGame :one
INSERT INTO games (id, slug, name, summary, description, cover_object_id, released_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, slug, name, summary, description, cover_object_id, released_at, created_at, updated_at;

-- name: GetGameByID :one
SELECT id, slug, name, summary, description, cover_object_id, released_at, created_at, updated_at
FROM games WHERE id = $1;

-- name: GetGameBySlug :one
SELECT id, slug, name, summary, description, cover_object_id, released_at, created_at, updated_at
FROM games WHERE slug = $1;

-- name: ListGames :many
SELECT id, slug, name, summary, description, cover_object_id, released_at, created_at, updated_at
FROM games ORDER BY name ASC;

-- name: UpdateGame :exec
UPDATE games SET slug=$2, name=$3, summary=$4, description=$5, cover_object_id=$6, released_at=$7, updated_at=now()
WHERE id = $1;

-- name: DeleteGame :exec
DELETE FROM games WHERE id = $1;

-- name: SetGameCover :exec
UPDATE games SET cover_object_id=$2, updated_at=now() WHERE id=$1;
