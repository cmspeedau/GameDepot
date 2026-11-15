-- +goose Up
-- +goose StatementBegin

CREATE TABLE libraries (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    path text NOT NULL,
    kind text NOT NULL DEFAULT 'directory',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE releases (
    id uuid PRIMARY KEY,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    version_id uuid NOT NULL REFERENCES game_versions(id) ON DELETE CASCADE,
    digest text NOT NULL UNIQUE,
    manifest jsonb NOT NULL,
    total_size bigint NOT NULL DEFAULT 0,
    published_by uuid REFERENCES users(id) ON DELETE SET NULL,
    published_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX releases_game_id_idx ON releases(game_id);
CREATE INDEX releases_version_id_idx ON releases(version_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS releases;
DROP TABLE IF EXISTS libraries;

-- +goose StatementEnd
