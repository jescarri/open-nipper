package models

import "time"

// SessionStatus enumerates the lifecycle states of a session.
type SessionStatus string

const (
	SessionStatusActive   SessionStatus = "active"
	SessionStatusArchived SessionStatus = "archived"
	SessionStatusExpired  SessionStatus = "expired"
)

// Session is the top-level descriptor for a user conversation session.
type Session struct {
	Key         string          `json:"key"`
	ID          string          `json:"id"`
	UserID      string          `json:"userId"`
	ChannelType ChannelType     `json:"channelType"`
	Status      SessionStatus   `json:"status"`
	Metadata    SessionMetadata `json:"metadata"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// SessionMetadata holds mutable runtime state about a session.
type SessionMetadata struct {
	Model            string       `json:"model"`
	CompactionLevel  string       `json:"compactionLevel,omitempty"`
	CompactionCount  int          `json:"compactionCount"`
	MessageCount     int          `json:"messageCount"`
	ContextUsage     ContextUsage `json:"contextUsage"`
	LastActivityAt   time.Time    `json:"lastActivityAt"`
	DeliveryContext  DeliveryContext `json:"deliveryContext"`
}

// ContextUsage tracks token consumption for a session or single run.
type ContextUsage struct {
	InputTokens     int `json:"inputTokens"`
	OutputTokens    int `json:"outputTokens"`
	CacheReadTokens int `json:"cacheReadTokens,omitempty"`
	ContextWindow   int `json:"contextWindow,omitempty"`
	UsagePercent    float64 `json:"usagePercent,omitempty"`
}

// TranscriptLine is a single append-only entry in the JSONL session transcript.
type TranscriptLine struct {
	Role      string    `json:"role"`       // "user" | "assistant" | "system"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	RunID     string    `json:"runId,omitempty"`
	TokenCount int      `json:"tokenCount,omitempty"`
}
