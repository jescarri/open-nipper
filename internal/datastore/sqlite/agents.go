package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/open-nipper/open-nipper/internal/models"
)

// ProvisionAgent creates a new agent record.
func (s *Store) ProvisionAgent(ctx context.Context, req models.ProvisionAgentRequest) (*models.Agent, error) {
	ts := now()
	// Generate a deterministic agent ID based on user + label.
	agentID := fmt.Sprintf("agt-%s-%s", req.UserID, req.Label)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, user_id, label, token_hash, token_prefix, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, req.UserID, req.Label, req.TokenHash, req.TokenPrefix,
		string(models.AgentStatusProvisioned), ts, ts,
	)
	if err != nil {
		return nil, fmt.Errorf("provision agent: %w", err)
	}
	return s.GetAgent(ctx, agentID)
}

// GetAgent retrieves an agent by ID.
func (s *Store) GetAgent(ctx context.Context, agentID string) (*models.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, label, token_hash, token_prefix, rmq_username, status,
		        last_registered_at, last_registered_ip, created_at, updated_at
		 FROM agents WHERE id = ?`, agentID,
	)
	return scanAgent(row)
}

// GetAgentByTokenHash looks up an agent by the SHA-256 hash of its token.
func (s *Store) GetAgentByTokenHash(ctx context.Context, tokenHash string) (*models.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, label, token_hash, token_prefix, rmq_username, status,
		        last_registered_at, last_registered_ip, created_at, updated_at
		 FROM agents WHERE token_hash = ?`, tokenHash,
	)
	return scanAgent(row)
}

// ListAgents returns all agents, optionally filtered by userID (empty = all users).
func (s *Store) ListAgents(ctx context.Context, userID string) ([]*models.Agent, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, label, token_hash, token_prefix, rmq_username, status,
			        last_registered_at, last_registered_ip, created_at, updated_at
			 FROM agents ORDER BY created_at`,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, label, token_hash, token_prefix, rmq_username, status,
			        last_registered_at, last_registered_ip, created_at, updated_at
			 FROM agents WHERE user_id = ? ORDER BY created_at`,
			userID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var agents []*models.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// UpdateAgentStatus updates the agent's status and optionally the registration metadata.
func (s *Store) UpdateAgentStatus(ctx context.Context, agentID string, status models.AgentStatus, meta *models.AgentRegistrationMeta) error {
	ts := now()
	if meta != nil {
		_, err := s.db.ExecContext(ctx,
			`UPDATE agents SET status=?, last_registered_at=?, last_registered_ip=?, updated_at=?
			 WHERE id=?`,
			string(status), ts, meta.IP, ts, agentID,
		)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status=?, updated_at=? WHERE id=?`,
		string(status), ts, agentID,
	)
	return err
}

// SetAgentRMQUsername stores the RabbitMQ username created for this agent.
func (s *Store) SetAgentRMQUsername(ctx context.Context, agentID, rmqUsername string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET rmq_username=?, updated_at=? WHERE id=?`,
		rmqUsername, now(), agentID,
	)
	return err
}

// RotateAgentToken replaces the token hash and prefix stored for an agent.
func (s *Store) RotateAgentToken(ctx context.Context, agentID, newTokenHash, newTokenPrefix string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET token_hash=?, token_prefix=?, updated_at=? WHERE id=?`,
		newTokenHash, newTokenPrefix, now(), agentID,
	)
	return err
}

// RevokeAgent marks an agent as revoked without deleting it.
func (s *Store) RevokeAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status=?, updated_at=? WHERE id=?`,
		string(models.AgentStatusRevoked), now(), agentID,
	)
	return err
}

// DeleteAgent removes an agent record permanently.
func (s *Store) DeleteAgent(ctx context.Context, agentID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, agentID)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent not found: %s", agentID)
	}
	return nil
}

func scanAgent(s scanner) (*models.Agent, error) {
	var a models.Agent
	var rmqUsername sql.NullString
	var lastRegAt, lastRegIP sql.NullString
	var createdAt, updatedAt, status string

	if err := s.Scan(
		&a.ID, &a.UserID, &a.Label, &a.TokenHash, &a.TokenPrefix,
		&rmqUsername, &status, &lastRegAt, &lastRegIP, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent not found")
		}
		return nil, fmt.Errorf("scan agent: %w", err)
	}
	a.Status = models.AgentStatus(status)
	a.RMQUsername = rmqUsername.String
	a.LastRegisteredIP = lastRegIP.String
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if lastRegAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastRegAt.String)
		a.LastRegisteredAt = &t
	}
	return &a, nil
}
