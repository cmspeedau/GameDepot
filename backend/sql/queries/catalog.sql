-- name: CreateCompany :one
INSERT INTO companies (id, name) VALUES ($1, $2)
ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
RETURNING id, name;

-- name: GetCompanyByName :one
SELECT id, name FROM companies WHERE name = $1;

-- name: ListCompanies :many
SELECT id, name FROM companies ORDER BY name ASC;

-- name: LinkGameCompany :exec
INSERT INTO game_companies (game_id, company_id, role) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: UnlinkGameCompanies :exec
DELETE FROM game_companies WHERE game_id = $1;

-- name: ListCompaniesForGame :many
SELECT c.id, c.name, gc.role FROM companies c JOIN game_companies gc ON gc.company_id = c.id WHERE gc.game_id = $1;

-- name: CreateTag :one
INSERT INTO tags (id, slug, name) VALUES ($1, $2, $3)
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
RETURNING id, slug, name;

-- name: GetTagBySlug :one
SELECT id, slug, name FROM tags WHERE slug = $1;

-- name: ListTags :many
SELECT id, slug, name FROM tags ORDER BY name ASC;

-- name: LinkGameTag :exec
INSERT INTO game_tags (game_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: UnlinkGameTags :exec
DELETE FROM game_tags WHERE game_id = $1;

-- name: ListTagsForGame :many
SELECT t.id, t.slug, t.name FROM tags t JOIN game_tags gt ON gt.tag_id = t.id WHERE gt.game_id = $1;

-- name: CreateGameVersion :one
INSERT INTO game_versions (id, game_id, name, platform, total_size)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, game_id, name, platform, total_size, created_at;

-- name: ListGameVersions :many
SELECT id, game_id, name, platform, total_size, created_at
FROM game_versions WHERE game_id = $1 ORDER BY created_at DESC;

-- name: GetGameVersion :one
SELECT id, game_id, name, platform, total_size, created_at
FROM game_versions WHERE id = $1;

-- name: CreateLaunch :one
INSERT INTO launches (id, version_id, platform, name, executable, arguments, working_dir)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, version_id, platform, name, executable, arguments, working_dir;

-- name: ListLaunchesForVersion :many
SELECT id, version_id, platform, name, executable, arguments, working_dir
FROM launches WHERE version_id = $1;

-- name: CreateCollection :one
INSERT INTO collections (id, user_id, name) VALUES ($1, $2, $3)
RETURNING id, user_id, name, created_at;

-- name: ListCollectionsForUser :many
SELECT id, user_id, name, created_at FROM collections WHERE user_id = $1 ORDER BY created_at ASC;

-- name: GetCollection :one
SELECT id, user_id, name, created_at FROM collections WHERE id = $1;

-- name: DeleteCollection :exec
DELETE FROM collections WHERE id = $1 AND user_id = $2;

-- name: AddCollectionEntry :exec
INSERT INTO collection_entries (collection_id, game_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveCollectionEntry :exec
DELETE FROM collection_entries WHERE collection_id = $1 AND game_id = $2;

-- name: ListCollectionEntries :many
SELECT game_id FROM collection_entries WHERE collection_id = $1;
