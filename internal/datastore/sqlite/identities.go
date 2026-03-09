package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/open-nipper/open-nipper/internal/models"
)

// AddIdentity links a channel identity to a user.
func (s *Store) AddIdentity(ctx context.Context, userID, channelType, channelIdentity string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_identities (user_id, channel_type, channel_identity, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(channel_type, channel_identity) DO NOTHING`,
		userID, channelType, channelIdentity, now(),
	)
	if err != nil {
		return fmt.Errorf("add identity: %w", err)
	}
	return nil
}

// RemoveIdentity deletes an identity by its row ID.
func (s *Store) RemoveIdentity(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_identities WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove identity: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("identity not found: %d", id)
	}
	return nil
}

// ListIdentities returns all identities for a user.
func (s *Store) ListIdentities(ctx context.Context, userID string) ([]*models.Identity, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, channel_type, channel_identity, verified, created_at
		 FROM user_identities WHERE user_id = ? ORDER BY created_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()

	var identities []*models.Identity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		identities = append(identities, id)
	}
	return identities, rows.Err()
}

// ResolveIdentity returns the userID associated with a channel identity, or "" if not found.
func (s *Store) ResolveIdentity(ctx context.Context, channelType, channelIdentity string) (string, error) {
	var userID string
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM user_identities WHERE channel_type = ? AND channel_identity = ?`,
		channelType, channelIdentity,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve identity: %w", err)
	}
	return userID, nil
}

func scanIdentity(s scanner) (*models.Identity, error) {
	var id models.Identity
	var createdAt string
	if err := s.Scan(&id.ID, &id.UserID, &id.ChannelType, &id.ChannelIdentity, &id.Verified, &createdAt); err != nil {
		return nil, fmt.Errorf("scan identity: %w", err)
	}
	id.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &id, nil
}
