-- +goose Up
-- +goose StatementBegin

CREATE TABLE users (
    id uuid PRIMARY KEY,
    username text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    admin boolean NOT NULL DEFAULT false,
    disabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_secret bytea NOT NULL,
    user_agent text NOT NULL DEFAULT '',
    ip inet,
    remember boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE invitations (
    id uuid PRIMARY KEY,
    code_hash bytea NOT NULL UNIQUE,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    used_by uuid REFERENCES users(id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    used_at timestamptz
);

CREATE TABLE settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE clients (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    platform text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    scopes text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX clients_user_id_idx ON clients(user_id);

CREATE TABLE pairing_codes (
    code_hash bytea PRIMARY KEY,
    name text NOT NULL,
    platform text NOT NULL,
    scopes text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    approved_by uuid REFERENCES users(id) ON DELETE SET NULL,
    approved_at timestamptz,
    claimed_at timestamptz,
    client_id uuid REFERENCES clients(id) ON DELETE SET NULL
);

CREATE TABLE tasks (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    state text NOT NULL DEFAULT 'queued',
    progress double precision NOT NULL DEFAULT 0,
    log text NOT NULL DEFAULT '',
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tasks_state_idx ON tasks(state);
CREATE INDEX tasks_created_at_idx ON tasks(created_at);

CREATE TABLE games (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    summary text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    cover_object_id uuid,
    released_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE objects (
    id uuid PRIMARY KEY,
    sha256 bytea NOT NULL UNIQUE,
    size bigint NOT NULL,
    content_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS objects;
DROP TABLE IF EXISTS games;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS pairing_codes;
DROP TABLE IF EXISTS clients;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;

-- +goose StatementEnd
