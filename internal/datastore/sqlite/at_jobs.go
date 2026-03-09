package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/open-nipper/open-nipper/internal/config"
)

// ListAtJobs returns all at jobs (for gateway startup load).
func (s *Store) ListAtJobs(ctx context.Context) ([]config.AtJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, run_at, prompt, notify_channels
		 FROM at_jobs ORDER BY user_id, run_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list at jobs: %w", err)
	}
	defer rows.Close()

	var jobs []config.AtJob
	for rows.Next() {
		var j config.AtJob
		var notifyRaw string
		if err := rows.Scan(&j.ID, &j.UserID, &j.RunAt, &j.Prompt, &notifyRaw); err != nil {
			return nil, fmt.Errorf("scan at job: %w", err)
		}
		if notifyRaw != "" && notifyRaw != "[]" {
			_ = json.Unmarshal([]byte(notifyRaw), &j.NotifyChannels)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ListAtJobsByUser returns at jobs for a single user.
func (s *Store) ListAtJobsByUser(ctx context.Context, userID string) ([]config.AtJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, run_at, prompt, notify_channels
		 FROM at_jobs WHERE user_id = ? ORDER BY run_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list at jobs by user: %w", err)
	}
	defer rows.Close()

	var jobs []config.AtJob
	for rows.Next() {
		var j config.AtJob
		var notifyRaw string
		if err := rows.Scan(&j.ID, &j.UserID, &j.RunAt, &j.Prompt, &notifyRaw); err != nil {
			return nil, fmt.Errorf("scan at job: %w", err)
		}
		if notifyRaw != "" && notifyRaw != "[]" {
			_ = json.Unmarshal([]byte(notifyRaw), &j.NotifyChannels)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// AddAtJob inserts an at job. ID must be unique per user.
func (s *Store) AddAtJob(ctx context.Context, job config.AtJob) error {
	notifyRaw := "[]"
	if len(job.NotifyChannels) > 0 {
		b, _ := json.Marshal(job.NotifyChannels)
		notifyRaw = string(b)
	}
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO at_jobs (id, user_id, run_at, prompt, notify_channels, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.UserID, job.RunAt, job.Prompt, notifyRaw, ts, ts,
	)
	if err != nil {
		return fmt.Errorf("add at job: %w", err)
	}
	return nil
}

// RemoveAtJob deletes an at job. Only the owning user can remove.
func (s *Store) RemoveAtJob(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM at_jobs WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("remove at job: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("at job not found or not owned by user")
	}
	return nil
}
