package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jescarri/open-nipper/internal/models"
)

// CreateUser inserts a new user record with an auto-generated ID and returns the created user.
func (s *Store) CreateUser(ctx context.Context, req models.CreateUserRequest) (*models.User, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("user name is required")
	}
	userID := models.GenerateUserID()
	if req.DefaultModel == "" {
		req.DefaultModel = "claude-sonnet-4-20250514"
	}
	prefsJSON := "{}"
	if req.Preferences != nil {
		b, err := json.Marshal(req.Preferences)
		if err != nil {
			return nil, fmt.Errorf("marshal preferences: %w", err)
		}
		prefsJSON = string(b)
	}
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, name, enabled, default_model, created_at, updated_at, preferences)
		 VALUES (?, ?, 1, ?, ?, ?, ?)`,
		userID, req.Name, req.DefaultModel, ts, ts, prefsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return s.GetUser(ctx, userID)
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(ctx context.Context, userID string) (*models.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, enabled, default_model, created_at, updated_at, preferences
		 FROM users WHERE id = ?`, userID,
	)
	return scanUser(row)
}

// UpdateUser applies partial updates to a user.
func (s *Store) UpdateUser(ctx context.Context, userID string, updates models.UpdateUserRequest) (*models.User, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if updates.Name != nil {
		user.Name = *updates.Name
	}
	if updates.DefaultModel != nil {
		user.DefaultModel = *updates.DefaultModel
	}
	if updates.Enabled != nil {
		user.Enabled = *updates.Enabled
	}
	if updates.Preferences != nil {
		user.Preferences = updates.Preferences
	}
	prefsJSON, err := json.Marshal(user.Preferences)
	if err != nil {
		return nil, fmt.Errorf("marshal preferences: %w", err)
	}
	ts := now()
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET name=?, enabled=?, default_model=?, preferences=?, updated_at=?
		 WHERE id=?`,
		user.Name, user.Enabled, user.DefaultModel, string(prefsJSON), ts, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	user.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ts)
	return user, nil
}

// DeleteUser removes a user (cascades to identities, agents, allowlist via FK constraints).
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found: %s", userID)
	}
	return nil
}

// ListUsers returns all users.
func (s *Store) ListUsers(ctx context.Context) ([]*models.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, enabled, default_model, created_at, updated_at, preferences
		 FROM users ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []*models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// IsUserEnabled returns whether the user exists and is enabled.
func (s *Store) IsUserEnabled(ctx context.Context, userID string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled FROM users WHERE id = ?`, userID,
	).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("is user enabled: %w", err)
	}
	return enabled, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(s scanner) (*models.User, error) {
	var u models.User
	var createdAt, updatedAt, prefsJSON string
	if err := s.Scan(&u.ID, &u.Name, &u.Enabled, &u.DefaultModel, &createdAt, &updatedAt, &prefsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if err := json.Unmarshal([]byte(prefsJSON), &u.Preferences); err != nil {
		u.Preferences = map[string]any{}
	}
	return &u, nil
}
