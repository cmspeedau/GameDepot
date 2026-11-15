-- +goose Up
-- +goose StatementBegin

CREATE TYPE save_version_kind AS ENUM ('save', 'restore');

CREATE TABLE save_slots (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    slot_index int NOT NULL,
    label text NOT NULL DEFAULT '',
    -- Soft reference to the current head version (avoids a circular FK with
    -- save_versions). Kept in sync by the service in one transaction.
    head_version_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, game_id, slot_index)
);
CREATE INDEX save_slots_user_game_idx ON save_slots(user_id, game_id);

CREATE TABLE save_versions (
    id uuid PRIMARY KEY,
    slot_id uuid NOT NULL REFERENCES save_slots(id) ON DELETE CASCADE,
    version_seq int NOT NULL,
    kind save_version_kind NOT NULL DEFAULT 'save',
    content_hash text NOT NULL,
    archive_sha256 text NOT NULL,
    archive_object_id uuid REFERENCES objects(id) ON DELETE SET NULL,
    size bigint NOT NULL DEFAULT 0,
    file_count int NOT NULL DEFAULT 0,
    pinned boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (slot_id, version_seq)
);
CREATE INDEX save_versions_slot_idx ON save_versions(slot_id);

-- Save-location manifest: user-defined roots per game (the "own manifest"
-- approach). Templates use {p}, {save}, {appdata}, {user}, {steamid} style
-- tokens expanded on the client.
CREATE TABLE save_paths (
    id uuid PRIMARY KEY,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    platform text NOT NULL,          -- windows | linux | macos | *
    kind text NOT NULL,              -- file | registry
    template text NOT NULL,          -- path or registry key template
    recursive boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX save_paths_game_idx ON save_paths(game_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS save_paths;
DROP TABLE IF EXISTS save_versions;
DROP TABLE IF EXISTS save_slots;
DROP TYPE IF EXISTS save_version_kind;

-- +goose StatementEnd
