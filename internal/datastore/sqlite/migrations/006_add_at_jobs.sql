CREATE TABLE at_jobs (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    run_at            TEXT NOT NULL,
    prompt            TEXT NOT NULL,
    notify_channels   TEXT NOT NULL DEFAULT '[]',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX idx_at_jobs_user ON at_jobs(user_id);
