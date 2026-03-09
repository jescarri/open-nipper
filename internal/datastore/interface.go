// Package datastore defines the Repository interface and data types for the Open-Nipper datastore.
package datastore

import (
	"context"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// Repository is the single abstraction over the persistence layer.
// The SQLite implementation satisfies this interface; any other backend (PostgreSQL, etc.) can too.
type Repository interface {
	// Users
	CreateUser(ctx context.Context, req models.CreateUserRequest) (*models.User, error)
	GetUser(ctx context.Context, userID string) (*models.User, error)
	UpdateUser(ctx context.Context, userID string, updates models.UpdateUserRequest) (*models.User, error)
	DeleteUser(ctx context.Context, userID string) error
	ListUsers(ctx context.Context) ([]*models.User, error)
	IsUserEnabled(ctx context.Context, userID string) (bool, error)

	// Identities
	AddIdentity(ctx context.Context, userID, channelType, channelIdentity string) error
	RemoveIdentity(ctx context.Context, id int64) error
	ListIdentities(ctx context.Context, userID string) ([]*models.Identity, error)
	ResolveIdentity(ctx context.Context, channelType, channelIdentity string) (string, error)

	// Allowlist
	IsAllowed(ctx context.Context, userID, channelType string) (bool, error)
	SetAllowed(ctx context.Context, userID, channelType string, enabled bool, createdBy string) error
	RemoveAllowed(ctx context.Context, userID, channelType string) error
	ListAllowed(ctx context.Context, channelType string) ([]*models.AllowlistEntry, error)

	// Policies
	GetUserPolicy(ctx context.Context, userID, policyType string) (*models.PolicyData, error)
	SetUserPolicy(ctx context.Context, userID, policyType string, data *models.PolicyData) error
	DeleteUserPolicy(ctx context.Context, userID, policyType string) error
	ListUserPolicies(ctx context.Context, userID string) ([]*models.UserPolicy, error)

	// Agents
	ProvisionAgent(ctx context.Context, req models.ProvisionAgentRequest) (*models.Agent, error)
	GetAgent(ctx context.Context, agentID string) (*models.Agent, error)
	GetAgentByTokenHash(ctx context.Context, tokenHash string) (*models.Agent, error)
	ListAgents(ctx context.Context, userID string) ([]*models.Agent, error)
	UpdateAgentStatus(ctx context.Context, agentID string, status models.AgentStatus, meta *models.AgentRegistrationMeta) error
	SetAgentRMQUsername(ctx context.Context, agentID, rmqUsername string) error
	RotateAgentToken(ctx context.Context, agentID, newTokenHash, newTokenPrefix string) error
	RevokeAgent(ctx context.Context, agentID string) error
	DeleteAgent(ctx context.Context, agentID string) error

	// Cron jobs (runtime-scheduled prompts; persisted so agents can add/remove via API)
	ListCronJobs(ctx context.Context) ([]config.CronJob, error)
	ListCronJobsByUser(ctx context.Context, userID string) ([]config.CronJob, error)
	AddCronJob(ctx context.Context, job config.CronJob) error
	RemoveCronJob(ctx context.Context, id, userID string) error

	// At jobs (one-shot scheduled prompts; fire once then auto-delete)
	ListAtJobs(ctx context.Context) ([]config.AtJob, error)
	ListAtJobsByUser(ctx context.Context, userID string) ([]config.AtJob, error)
	AddAtJob(ctx context.Context, job config.AtJob) error
	RemoveAtJob(ctx context.Context, id, userID string) error

	// Audit
	LogAdminAction(ctx context.Context, entry models.AdminAuditEntry) error
	QueryAuditLog(ctx context.Context, filters models.AuditQueryFilters) ([]*models.AdminAuditEntry, error)

	// Backup
	Backup(ctx context.Context, destPath string) error

	// Lifecycle
	Close() error
	Ping(ctx context.Context) error
}
