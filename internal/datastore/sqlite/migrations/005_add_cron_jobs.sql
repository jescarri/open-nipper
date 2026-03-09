CREATE TABLE IF NOT EXISTS cron_jobs (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    schedule          TEXT NOT NULL,
    prompt            TEXT NOT NULL,
    notify_channels   TEXT NOT NULL DEFAULT '[]',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_user ON cron_jobs(user_id);
