CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT 1,
    default_model   TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    preferences     TEXT NOT NULL DEFAULT '{}'
);
