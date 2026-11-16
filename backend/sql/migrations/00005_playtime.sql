-- +goose Up
-- +goose StatementBegin
CREATE TABLE playtime (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    seconds bigint NOT NULL DEFAULT 0,
    last_played_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, game_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS playtime;
-- +goose StatementEnd
