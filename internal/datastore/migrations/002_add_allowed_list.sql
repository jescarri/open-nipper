CREATE TABLE IF NOT EXISTS user_identities (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type     TEXT NOT NULL,
    channel_identity TEXT NOT NULL,
    verified         BOOLEAN NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,
    UNIQUE(channel_type, channel_identity)
);
CREATE INDEX IF NOT EXISTS idx_identity_lookup ON user_identities(channel_type, channel_identity);

CREATE TABLE IF NOT EXISTS allowed_list (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    UNIQUE(user_id, channel_type)
);
CREATE INDEX IF NOT EXISTS idx_allowed_lookup ON allowed_list(channel_type, user_id);
