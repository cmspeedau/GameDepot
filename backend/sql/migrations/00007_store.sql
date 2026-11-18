-- +goose Up
-- +goose StatementBegin

-- Store connections: one row per store account the instance may ingest from.
-- Credentials are sealed at rest (see internal/secrets); the plaintext token never
-- reaches this table. A connection belongs to the instance, not to a user: ingestion
-- is an admin action and the acquired files are shared by everyone in the library.
CREATE TABLE store_connections (
    id uuid PRIMARY KEY,
    provider text NOT NULL,           -- gog | epic | ubisoft | ea
    account_label text,               -- display name from the provider, never a secret
    account_id text,                  -- provider-side stable id, for re-auth prompts
    access_token text NOT NULL,       -- sealed
    refresh_token text,               -- sealed; providers rotate it on every refresh
    expires_at timestamptz,           -- when the access token dies, sealed-value expiry
    status text NOT NULL DEFAULT 'active',  -- active | error (provider rejected the credential)
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT store_connections_provider_check CHECK (provider IN ('gog','epic','ubisoft','ea')),
    CONSTRAINT store_connections_status_check CHECK (status IN ('active','error'))
);

CREATE INDEX store_connections_provider_idx ON store_connections(provider, updated_at DESC);

-- What a store product maps to locally, so an ingest can create the catalog rows on
-- first sight and an update check can tell whether a newer build exists.
CREATE TABLE store_products (
    id uuid PRIMARY KEY,
    provider text NOT NULL,
    product_id text NOT NULL,          -- GOG product id / Epic catalogue item id
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    title text NOT NULL,
    platform text NOT NULL DEFAULT 'windows',
    last_build text,                   -- provider build id or version name last ingested
    last_release_digest text,          -- digest of the release built from it
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT store_products_provider_check CHECK (provider IN ('gog','epic','ubisoft','ea')),
    CONSTRAINT store_products_unique UNIQUE (provider, product_id, platform)
);

CREATE INDEX store_products_game_idx ON store_products(game_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS store_products;
DROP TABLE IF EXISTS store_connections;

-- +goose StatementEnd
