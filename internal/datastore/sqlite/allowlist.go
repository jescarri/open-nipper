package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jescarri/open-nipper/internal/models"
)

// IsAllowed reports whether the user is allowed to send on channelType.
// It also checks for a wildcard "*" entry for the user.
func (s *Store) IsAllowed(ctx context.Context, userID, channelType string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled FROM allowed_list
		 WHERE user_id = ? AND (channel_type = ? OR channel_type = '*')
		 ORDER BY (channel_type = ?) DESC
		 LIMIT 1`,
		userID, channelType, channelType,
	).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is allowed: %w", err)
	}
	return enabled, nil
}

// SetAllowed inserts or updates an allowlist entry.
func (s *Store) SetAllowed(ctx context.Context, userID, channelType string, enabled bool, createdBy string) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO allowed_list (user_id, channel_type, enabled, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, channel_type) DO UPDATE SET enabled = excluded.enabled`,
		userID, channelType, enabledInt, now(), createdBy,
	)
	if err != nil {
		return fmt.Errorf("set allowed: %w", err)
	}
	return nil
}

// RemoveAllowed deletes an allowlist entry.
func (s *Store) RemoveAllowed(ctx context.Context, userID, channelType string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM allowed_list WHERE user_id = ? AND channel_type = ?`,
		userID, channelType,
	)
	if err != nil {
		return fmt.Errorf("remove allowed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("allowlist entry not found: user=%s channel=%s", userID, channelType)
	}
	return nil
}

// ListAllowed returns all allowlist entries, optionally filtered by channelType.
// Pass "*" or "" to return all entries regardless of channel.
func (s *Store) ListAllowed(ctx context.Context, channelType string) ([]*models.AllowlistEntry, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if channelType == "" || channelType == "*" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, channel_type, enabled, created_at, created_by
			 FROM allowed_list ORDER BY created_at`,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, channel_type, enabled, created_at, created_by
			 FROM allowed_list WHERE channel_type = ? ORDER BY created_at`,
			channelType,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list allowed: %w", err)
	}
	defer rows.Close()

	var entries []*models.AllowlistEntry
	for rows.Next() {
		e, err := scanAllowlist(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func scanAllowlist(s scanner) (*models.AllowlistEntry, error) {
	var e models.AllowlistEntry
	var createdAt string
	if err := s.Scan(&e.ID, &e.UserID, &e.ChannelType, &e.Enabled, &createdAt, &e.CreatedBy); err != nil {
		return nil, fmt.Errorf("scan allowlist: %w", err)
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &e, nil
}
