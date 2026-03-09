package models

import (
	"time"

	"github.com/google/uuid"
)

// GenerateUserID creates an auto-generated, AMQP-safe user ID using UUIDv7.
// Format: usr_<uuid-v7-hex-no-dashes> (e.g. usr_019539a1b2c3d4e5f6a7b8c9d0e1f2a3).
func GenerateUserID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback to V4 if V7 fails (clock issue).
		id = uuid.New()
	}
	return "usr_" + id.String()
}

// User represents a user registered in the system.
type User struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	DefaultModel string            `json:"defaultModel"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	Preferences  map[string]any    `json:"preferences"`
}

// CreateUserRequest carries fields required to create a new user.
// ID is auto-generated server-side; any client-supplied value is ignored.
type CreateUserRequest struct {
	Name         string         `json:"name"`
	DefaultModel string         `json:"defaultModel,omitempty"`
	Preferences  map[string]any `json:"preferences,omitempty"`
}

// UpdateUserRequest carries optional fields for updating a user.
type UpdateUserRequest struct {
	Name         *string        `json:"name,omitempty"`
	DefaultModel *string        `json:"defaultModel,omitempty"`
	Enabled      *bool          `json:"enabled,omitempty"`
	Preferences  map[string]any `json:"preferences,omitempty"`
}

// Identity links a channel-specific identity to a user.
type Identity struct {
	ID              int64     `json:"id"`
	UserID          string    `json:"userId"`
	ChannelType     string    `json:"channelType"`
	ChannelIdentity string    `json:"channelIdentity"`
	Verified        bool      `json:"verified"`
	CreatedAt       time.Time `json:"createdAt"`
}

// AllowlistEntry grants a user permission to send messages on a channel.
type AllowlistEntry struct {
	ID          int64     `json:"id"`
	UserID      string    `json:"userId"`
	ChannelType string    `json:"channelType"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	CreatedBy   string    `json:"createdBy"`
}

// AgentStatus enumerates the lifecycle states of a provisioned agent.
type AgentStatus string

const (
	AgentStatusProvisioned AgentStatus = "provisioned"
	AgentStatusRegistered  AgentStatus = "registered"
	AgentStatusRevoked     AgentStatus = "revoked"
)

// Agent represents a provisioned agent credential.
type Agent struct {
	ID                 string              `json:"id"`
	UserID             string              `json:"userId"`
	Label              string              `json:"label"`
	TokenHash          string              `json:"-"`
	TokenPrefix        string              `json:"tokenPrefix"`
	RMQUsername        string              `json:"rmqUsername,omitempty"`
	Status             AgentStatus         `json:"status"`
	LastRegisteredAt   *time.Time          `json:"lastRegisteredAt,omitempty"`
	LastRegisteredIP   string              `json:"lastRegisteredIp,omitempty"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
}

// ProvisionAgentRequest carries fields required to provision a new agent.
type ProvisionAgentRequest struct {
	UserID     string `json:"userId"`
	Label      string `json:"label"`
	TokenHash  string `json:"-"`
	TokenPrefix string `json:"-"`
}

// AgentRegistrationMeta carries runtime metadata captured during agent auto-registration.
type AgentRegistrationMeta struct {
	IP        string `json:"ip"`
	AgentType string `json:"agentType,omitempty"`
	Version   string `json:"version,omitempty"`
}

// UserPolicy stores a typed policy blob for a user.
type UserPolicy struct {
	ID         int64     `json:"id"`
	UserID     string    `json:"userId"`
	PolicyType string    `json:"policyType"`
	Data       PolicyData `json:"data"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// PolicyData is the free-form policy payload.
type PolicyData struct {
	Allow       []string       `json:"allow,omitempty"`
	Deny        []string       `json:"deny,omitempty"`
	RateLimit   map[string]any `json:"rateLimit,omitempty"`
	Models      map[string]any `json:"models,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// AdminAuditEntry records a security-relevant admin action.
type AdminAuditEntry struct {
	ID           int64     `json:"id,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	Action       string    `json:"action"`
	Actor        string    `json:"actor"`
	TargetUserID string    `json:"targetUserId,omitempty"`
	Details      string    `json:"details"`
	IPAddress    string    `json:"ipAddress,omitempty"`
}

// AgentHealthInfo carries per-user agent queue health data surfaced by the
// health monitor and consumed by the admin health endpoint and WebSocket API.
type AgentHealthInfo struct {
	UserID        string `json:"user_id"`
	Queue         string `json:"queue"`
	ConsumerCount int    `json:"consumer_count"`
	MessagesReady int    `json:"messages_ready"`
	Status        string `json:"status"` // "processing", "idle", "degraded", "offline"
}

// AgentHeartbeatInfo carries agent health reported via POST /agents/health.
// Stored in memory only (never persisted to DB).
type AgentHeartbeatInfo struct {
	AgentID  string `json:"agent_id"`
	UserID   string `json:"user_id"`
	Status   string `json:"status"` // "healthy", "degraded", "unhealthy", "unknown"
	LastSeen string `json:"last_seen"` // RFC3339
}

// AuditQueryFilters holds optional filters for querying the audit log.
type AuditQueryFilters struct {
	Since        *time.Time `json:"since,omitempty"`
	Until        *time.Time `json:"until,omitempty"`
	Action       string     `json:"action,omitempty"`
	TargetUserID string     `json:"targetUserId,omitempty"`
	Limit        int        `json:"limit,omitempty"`
}
