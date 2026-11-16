-- name: CreateRunnerRecipe :one
INSERT INTO runner_recipes (id, game_id, version_id, profile, source, compatibility)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, game_id, version_id, profile, source, compatibility, created_at, updated_at;

-- name: GetRunnerRecipeForGame :one
SELECT id, game_id, version_id, profile, source, compatibility, created_at, updated_at
FROM runner_recipes
WHERE game_id = $1 AND version_id IS NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: GetRunnerRecipeForVersion :one
SELECT id, game_id, version_id, profile, source, compatibility, created_at, updated_at
FROM runner_recipes
WHERE game_id = $1 AND version_id = $2
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListRunnerRecipesForGame :many
SELECT id, game_id, version_id, profile, source, compatibility, created_at, updated_at
FROM runner_recipes
WHERE game_id = $1
ORDER BY updated_at DESC;

-- name: UpdateRunnerRecipe :one
UPDATE runner_recipes
SET profile = $2,
    source = $3,
    compatibility = $4,
    updated_at = now()
WHERE id = $1
RETURNING id, game_id, version_id, profile, source, compatibility, created_at, updated_at;

-- name: DeleteRunnerRecipe :exec
DELETE FROM runner_recipes WHERE id = $1;

-- name: ListRunnerRecipes :many
SELECT id, game_id, version_id, profile, source, compatibility, created_at, updated_at
FROM runner_recipes
ORDER BY updated_at DESC
LIMIT $1 OFFSET $2;