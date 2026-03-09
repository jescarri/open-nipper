// Package allowlist implements the gateway's allowlist enforcement guard.
package allowlist

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/datastore"
	"github.com/open-nipper/open-nipper/internal/models"
)

// RejectionReason classifies why a message was rejected by the guard.
type RejectionReason string

const (
	RejectionUnknownIdentity RejectionReason = "unknown_identity"
	RejectionUserDisabled    RejectionReason = "user_disabled"
	RejectionNotInAllowlist  RejectionReason = "not_in_allowlist"
)

// Guard enforces allowlist policy in the message pipeline.
// All business-rule rejections return (false, nil) so that HTTP callers can
// always return HTTP 200 — preventing retries and avoiding information leakage.
type Guard struct {
	repo   datastore.Repository
	logger *zap.Logger
}

// New creates a Guard backed by the given repository.
func New(repo datastore.Repository, logger *zap.Logger) *Guard {
	return &Guard{repo: repo, logger: logger}
}

// Check returns (true, nil) if the message is allowed to proceed through the
// pipeline. Returns (false, nil) on any business-rule rejection so the caller
// can safely return HTTP 200. Returns (false, err) only for infrastructure
// failures (datastore errors).
func (g *Guard) Check(ctx context.Context, userID, channelType, channelIdentity string) (bool, error) {
	// Step 1: identity must have been resolved to a known user.
	if userID == "" {
		g.writeRejection(ctx, RejectionUnknownIdentity, channelType, channelIdentity, "")
		return false, nil
	}

	// Step 2: the user account must be active.
	enabled, err := g.repo.IsUserEnabled(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("allowlist guard: IsUserEnabled(%q): %w", userID, err)
	}
	if !enabled {
		g.writeRejection(ctx, RejectionUserDisabled, channelType, channelIdentity, userID)
		return false, nil
	}

	// Step 3: there must be an active allowlist entry for this user+channel.
	// First check the channel-specific entry; fall back to the wildcard "*" entry.
	ok, err := g.repo.IsAllowed(ctx, userID, channelType)
	if err != nil {
		return false, fmt.Errorf("allowlist guard: IsAllowed(%q, %q): %w", userID, channelType, err)
	}
	if !ok {
		ok, err = g.repo.IsAllowed(ctx, userID, "*")
		if err != nil {
			return false, fmt.Errorf("allowlist guard: IsAllowed(%q, *): %w", userID, err)
		}
	}
	if !ok {
		g.writeRejection(ctx, RejectionNotInAllowlist, channelType, channelIdentity, userID)
		return false, nil
	}

	return true, nil
}

// writeRejection logs the rejection and writes an audit entry.
// channelIdentity is intentionally not stored — only [REDACTED] appears in logs and audit.
func (g *Guard) writeRejection(ctx context.Context, reason RejectionReason, channelType, _ /* channelIdentity */, userID string) {
	g.logger.Warn("message rejected by allowlist guard",
		zap.String("reason", string(reason)),
		zap.String("channelType", channelType),
		zap.String("userId", userID),
	)

	details, _ := json.Marshal(map[string]string{
		"reason":           string(reason),
		"channel_type":     channelType,
		"channel_identity": "[REDACTED]",
	})

	entry := models.AdminAuditEntry{
		Timestamp:    time.Now().UTC(),
		Action:       "message.rejected",
		Actor:        "system",
		TargetUserID: userID,
		Details:      string(details),
	}

	if err := g.repo.LogAdminAction(ctx, entry); err != nil {
		g.logger.Error("failed to write audit entry for rejected message", zap.Error(err))
	}
}
