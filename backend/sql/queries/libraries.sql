-- name: CreateLibrary :one
INSERT INTO libraries (id, name, path, kind) VALUES ($1, $2, $3, $4)
RETURNING id, name, path, kind, created_at;

-- name: ListLibraries :many
SELECT id, name, path, kind, created_at FROM libraries ORDER BY created_at ASC;

-- name: GetLibrary :one
SELECT id, name, path, kind, created_at FROM libraries WHERE id = $1;

-- name: DeleteLibrary :exec
DELETE FROM libraries WHERE id = $1;

-- name: CreateRelease :one
INSERT INTO releases (id, game_id, version_id, digest, manifest, total_size, published_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, game_id, version_id, digest, total_size, published_at;

-- name: GetReleaseByDigest :one
SELECT id, game_id, version_id, digest, manifest, total_size, published_by, published_at
FROM releases WHERE digest = $1;

-- name: GetReleaseByGameVersion :one
SELECT id, game_id, version_id, digest, manifest, total_size, published_by, published_at
FROM releases WHERE game_id = $1 AND version_id = $2;

-- name: ListReleasesForGame :many
SELECT id, game_id, version_id, digest, total_size, published_at
FROM releases WHERE game_id = $1 ORDER BY published_at DESC;

-- name: DeleteRelease :exec
DELETE FROM releases WHERE id = $1;
