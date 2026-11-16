-- +goose Up
-- +goose StatementBegin

CREATE TABLE runner_recipes (
    id uuid PRIMARY KEY,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    version_id uuid REFERENCES game_versions(id) ON DELETE CASCADE,
    profile jsonb NOT NULL DEFAULT '{}',
    source text NOT NULL DEFAULT 'curated',
    compatibility text NOT NULL DEFAULT 'untested',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runner_recipes_game_id_idx ON runner_recipes(game_id);
CREATE INDEX runner_recipes_version_id_idx ON runner_recipes(version_id);
CREATE UNIQUE INDEX runner_recipes_game_version_uniq
    ON runner_recipes (game_id, COALESCE(version_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS runner_recipes;

-- +goose StatementEnd