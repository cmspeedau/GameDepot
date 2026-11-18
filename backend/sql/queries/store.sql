-- name: CreateStoreConnection :one
INSERT INTO store_connections (id, provider, account_label, account_id, access_token, refresh_token, expires_at, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, provider, account_label, account_id, access_token, refresh_token, expires_at, status, created_at, updated_at;

-- name: GetStoreConnection :one
SELECT id, provider, account_label, account_id, access_token, refresh_token, expires_at, status, created_at, updated_at
FROM store_connections WHERE id = $1;

-- name: ListStoreConnections :many
SELECT id, provider, account_label, account_id, access_token, refresh_token, expires_at, status, created_at, updated_at
FROM store_connections
ORDER BY provider ASC, updated_at DESC;

-- name: NewestActiveStoreConnection :one
SELECT id, provider, account_label, account_id, access_token, refresh_token, expires_at, status, created_at, updated_at
FROM store_connections
WHERE provider = $1 AND status = 'active'
ORDER BY updated_at DESC
LIMIT 1;

-- name: UpdateStoreConnectionToken :exec
-- Providers rotate refresh tokens on use, so every refresh must write both values back.
UPDATE store_connections
SET access_token = $2, refresh_token = $3, expires_at = $4, updated_at = now()
WHERE id = $1;

-- name: SetStoreConnectionStatus :exec
UPDATE store_connections
SET status = $2, updated_at = now()
WHERE id = $1;

-- name: DeleteStoreConnection :exec
DELETE FROM store_connections WHERE id = $1;

-- name: UpsertStoreProduct :one
INSERT INTO store_products (id, provider, product_id, game_id, title, platform, last_build, last_release_digest)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (provider, product_id, platform) DO UPDATE SET
    game_id = EXCLUDED.game_id,
    title = EXCLUDED.title,
    last_build = COALESCE(EXCLUDED.last_build, store_products.last_build),
    last_release_digest = COALESCE(EXCLUDED.last_release_digest, store_products.last_release_digest),
    updated_at = now()
RETURNING id, provider, product_id, game_id, title, platform, last_build, last_release_digest, created_at, updated_at;

-- name: GetStoreProduct :one
SELECT id, provider, product_id, game_id, title, platform, last_build, last_release_digest, created_at, updated_at
FROM store_products WHERE provider = $1 AND product_id = $2 AND platform = $3;

-- name: GetStoreProductByGame :many
SELECT id, provider, product_id, game_id, title, platform, last_build, last_release_digest, created_at, updated_at
FROM store_products WHERE game_id = $1;

-- name: ListStoreProducts :many
SELECT id, provider, product_id, game_id, title, platform, last_build, last_release_digest, created_at, updated_at
FROM store_products WHERE provider = $1
ORDER BY title ASC;

-- name: ClearStoreProductBuild :exec
-- Forget the last-ingested build after a release is deleted, so a re-ingest is allowed.
UPDATE store_products SET last_build = NULL, last_release_digest = NULL, updated_at = now()
WHERE provider = $1 AND product_id = $2 AND platform = $3;

-- name: GetStoreProductSiblings :many
-- Every mapping for a provider product across platforms, newest first. A title
-- ingested for Windows and again for macOS is still one game, so the platform-specific
-- row cannot be the only place the game link is kept.
SELECT id, provider, product_id, game_id, title, platform, last_build, last_release_digest, created_at, updated_at
FROM store_products WHERE provider = $1 AND product_id = $2
ORDER BY updated_at DESC;
