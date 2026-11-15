-- +goose Up
-- +goose StatementBegin

CREATE TABLE companies (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE
);

CREATE TABLE game_companies (
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    role text NOT NULL DEFAULT 'developer',
    PRIMARY KEY (game_id, company_id)
);

CREATE TABLE tags (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL
);

CREATE TABLE game_tags (
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    tag_id uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (game_id, tag_id)
);

CREATE TABLE game_versions (
    id uuid PRIMARY KEY,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    name text NOT NULL,
    platform text NOT NULL,
    total_size bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX game_versions_game_id_idx ON game_versions(game_id);

CREATE TABLE launches (
    id uuid PRIMARY KEY,
    version_id uuid NOT NULL REFERENCES game_versions(id) ON DELETE CASCADE,
    platform text NOT NULL,
    name text NOT NULL,
    executable text NOT NULL,
    arguments text[] NOT NULL DEFAULT '{}',
    working_dir text NOT NULL DEFAULT ''
);
CREATE INDEX launches_version_id_idx ON launches(version_id);

CREATE TABLE collections (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX collections_user_id_idx ON collections(user_id);

CREATE TABLE collection_entries (
    collection_id uuid NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    position int NOT NULL DEFAULT 0,
    PRIMARY KEY (collection_id, game_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS collection_entries;
DROP TABLE IF EXISTS collections;
DROP TABLE IF EXISTS launches;
DROP TABLE IF EXISTS game_versions;
DROP TABLE IF EXISTS game_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS game_companies;
DROP TABLE IF EXISTS companies;

-- +goose StatementEnd
