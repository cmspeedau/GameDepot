-- name: AddSavePath :one
INSERT INTO save_paths (id, game_id, platform, kind, template, recursive, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, game_id, platform, kind, template, recursive, created_by, created_at;

-- name: ListSavePaths :many
SELECT id, game_id, platform, kind, template, recursive, created_by, created_at
FROM save_paths WHERE game_id = $1 ORDER BY platform ASC, id ASC;

-- name: DeleteSavePath :exec
DELETE FROM save_paths WHERE id = $1;
