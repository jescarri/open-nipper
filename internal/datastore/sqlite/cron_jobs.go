package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/open-nipper/open-nipper/internal/config"
)

// ListCronJobs returns all cron jobs (for gateway startup load).
func (s *Store) ListCronJobs(ctx context.Context) ([]config.CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, schedule, prompt, notify_channels
		 FROM cron_jobs ORDER BY user_id, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []config.CronJob
	for rows.Next() {
		var j config.CronJob
		var notifyRaw string
		if err := rows.Scan(&j.ID, &j.UserID, &j.Schedule, &j.Prompt, &notifyRaw); err != nil {
			return nil, fmt.Errorf("scan cron job: %w", err)
		}
		if notifyRaw != "" && notifyRaw != "[]" {
			_ = json.Unmarshal([]byte(notifyRaw), &j.NotifyChannels)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ListCronJobsByUser returns cron jobs for a single user (for agent API).
func (s *Store) ListCronJobsByUser(ctx context.Context, userID string) ([]config.CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, schedule, prompt, notify_channels
		 FROM cron_jobs WHERE user_id = ? ORDER BY id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list cron jobs by user: %w", err)
	}
	defer rows.Close()

	var jobs []config.CronJob
	for rows.Next() {
		var j config.CronJob
		var notifyRaw string
		if err := rows.Scan(&j.ID, &j.UserID, &j.Schedule, &j.Prompt, &notifyRaw); err != nil {
			return nil, fmt.Errorf("scan cron job: %w", err)
		}
		if notifyRaw != "" && notifyRaw != "[]" {
			_ = json.Unmarshal([]byte(notifyRaw), &j.NotifyChannels)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// AddCronJob inserts a cron job. Id must be unique per user (table PK is id only; id is globally unique).
func (s *Store) AddCronJob(ctx context.Context, job config.CronJob) error {
	notifyRaw := "[]"
	if len(job.NotifyChannels) > 0 {
		b, _ := json.Marshal(job.NotifyChannels)
		notifyRaw = string(b)
	}
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_jobs (id, user_id, schedule, prompt, notify_channels, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.UserID, job.Schedule, job.Prompt, notifyRaw, ts, ts,
	)
	if err != nil {
		return fmt.Errorf("add cron job: %w", err)
	}
	return nil
}

// RemoveCronJob deletes a cron job. Only the owning user can remove (userID is checked).
func (s *Store) RemoveCronJob(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM cron_jobs WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("remove cron job: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("cron job not found or not owned by user")
	}
	return nil
}
