package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/open-nipper/open-nipper/internal/models"
)

// GetUserPolicy returns the policy data for a user and policy type.
// Returns nil, nil if no policy is set (caller applies defaults).
func (s *Store) GetUserPolicy(ctx context.Context, userID, policyType string) (*models.PolicyData, error) {
	var dataJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT policy_data FROM user_policies WHERE user_id = ? AND policy_type = ?`,
		userID, policyType,
	).Scan(&dataJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user policy: %w", err)
	}
	var data models.PolicyData
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return nil, fmt.Errorf("unmarshal policy data: %w", err)
	}
	return &data, nil
}

// SetUserPolicy inserts or replaces a policy for the user.
func (s *Store) SetUserPolicy(ctx context.Context, userID, policyType string, data *models.PolicyData) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal policy data: %w", err)
	}
	ts := now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_policies (user_id, policy_type, policy_data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, policy_type) DO UPDATE SET policy_data = excluded.policy_data, updated_at = excluded.updated_at`,
		userID, policyType, string(dataJSON), ts, ts,
	)
	if err != nil {
		return fmt.Errorf("set user policy: %w", err)
	}
	return nil
}

// DeleteUserPolicy removes a policy entry.
func (s *Store) DeleteUserPolicy(ctx context.Context, userID, policyType string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM user_policies WHERE user_id = ? AND policy_type = ?`,
		userID, policyType,
	)
	if err != nil {
		return fmt.Errorf("delete user policy: %w", err)
	}
	return nil
}

// ListUserPolicies returns all policies for a user.
func (s *Store) ListUserPolicies(ctx context.Context, userID string) ([]*models.UserPolicy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, policy_type, policy_data, created_at, updated_at
		 FROM user_policies WHERE user_id = ? ORDER BY policy_type`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list user policies: %w", err)
	}
	defer rows.Close()

	var policies []*models.UserPolicy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

func scanPolicy(s scanner) (*models.UserPolicy, error) {
	var p models.UserPolicy
	var dataJSON, createdAt, updatedAt string
	if err := s.Scan(&p.ID, &p.UserID, &p.PolicyType, &dataJSON, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan policy: %w", err)
	}
	if err := json.Unmarshal([]byte(dataJSON), &p.Data); err != nil {
		return nil, fmt.Errorf("unmarshal policy: %w", err)
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p, nil
}
