// Package session provides filesystem-based session management for Open-Nipper agents.
//
// This package is designed to be imported by Go agents:
//
//	import "github.com/open-nipper/open-nipper/pkg/session"
//
// It provides transcript storage, file locking, compaction, and session
// lifecycle operations. The gateway does NOT use this package directly —
// session file management is an agent-only responsibility in the distributed
// architecture. The gateway only derives session keys and tracks delivery
// context in memory.
package session

import "time"

// SessionStatus enumerates the lifecycle states of a session.
type SessionStatus string

const (
	StatusActive   SessionStatus = "active"
	StatusArchived SessionStatus = "archived"
	StatusExpired  SessionStatus = "expired"
)

// Session is the top-level descriptor for a user conversation session.
type Session struct {
	Key         string          `json:"key"`
	ID          string          `json:"id"`
	UserID      string          `json:"userId"`
	ChannelType string          `json:"channelType"`
	Status      SessionStatus   `json:"status"`
	Metadata    SessionMetadata `json:"metadata"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// SessionMetadata holds mutable runtime state about a session.
type SessionMetadata struct {
	Model           string       `json:"model"`
	CompactionLevel string       `json:"compactionLevel,omitempty"`
	CompactionCount int          `json:"compactionCount"`
	MessageCount    int          `json:"messageCount"`
	ContextUsage    ContextUsage `json:"contextUsage"`
	LastActivityAt  time.Time    `json:"lastActivityAt"`
	ChannelMeta     any          `json:"channelMeta,omitempty"`
}

// ContextUsage tracks token consumption for a session or single run.
type ContextUsage struct {
	InputTokens     int     `json:"inputTokens"`
	OutputTokens    int     `json:"outputTokens"`
	CacheReadTokens int     `json:"cacheReadTokens,omitempty"`
	ContextWindow   int     `json:"contextWindow,omitempty"`
	UsagePercent    float64 `json:"usagePercent,omitempty"`
}

// TranscriptLine is a single append-only entry in the JSONL session transcript.
type TranscriptLine struct {
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	Timestamp  time.Time `json:"timestamp"`
	RunID      string    `json:"runId,omitempty"`
	TokenCount int       `json:"tokenCount,omitempty"`
}

// CreateSessionRequest carries the parameters needed to create a new session.
type CreateSessionRequest struct {
	UserID      string
	SessionID   string
	ChannelType string
	Model       string
	ChannelMeta any
}
