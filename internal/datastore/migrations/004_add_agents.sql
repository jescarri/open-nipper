CREATE TABLE IF NOT EXISTS agents (
    id                   TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label                TEXT NOT NULL DEFAULT '',
    token_hash           TEXT NOT NULL,
    token_prefix         TEXT NOT NULL,
    rmq_username         TEXT,
    status               TEXT NOT NULL DEFAULT 'provisioned',
    last_registered_at   TEXT,
    last_registered_ip   TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE(user_id, label)
);
CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id);
CREATE INDEX IF NOT EXISTS idx_agents_token_prefix ON agents(token_prefix);

CREATE TABLE IF NOT EXISTS admin_audit (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp      TEXT NOT NULL,
    action         TEXT NOT NULL,
    actor          TEXT NOT NULL,
    target_user_id TEXT,
    details        TEXT NOT NULL,
    ip_address     TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON admin_audit(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_user ON admin_audit(target_user_id);
