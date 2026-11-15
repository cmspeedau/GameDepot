-- name: GetObjectBySHA256 :one
SELECT id, sha256, size, content_type, created_at FROM objects WHERE sha256 = $1;

-- name: GetObjectByID :one
SELECT id, sha256, size, content_type, created_at FROM objects WHERE id = $1;

-- name: CreateObject :one
INSERT INTO objects (id, sha256, size, content_type)
VALUES ($1, $2, $3, $4)
ON CONFLICT (sha256) DO NOTHING
RETURNING id, sha256, size, content_type, created_at;

-- name: GetObjectBySHA256Maybe :one
SELECT id, sha256, size, content_type, created_at FROM objects WHERE sha256 = $1;
