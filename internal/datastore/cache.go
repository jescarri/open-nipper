package datastore

import (
	"context"
	"fmt"
	"time"

	gocache "github.com/patrickmn/go-cache"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

const (
	identityTTL = 60 * time.Second
	allowedTTL  = 60 * time.Second
	userTTL     = 60 * time.Second
	policyTTL   = 300 * time.Second
	agentTTL    = 60 * time.Second
)

// CachedRepository wraps a Repository with an in-memory TTL cache for frequently-read lookups.
// Write operations also invalidate the relevant cache keys.
type CachedRepository struct {
	inner Repository
	cache *gocache.Cache
}

// NewCachedRepository returns a CachedRepository that wraps inner with a TTL cache.
func NewCachedRepository(inner Repository) *CachedRepository {
	return &CachedRepository{
		inner: inner,
		cache: gocache.New(5*time.Minute, 10*time.Minute),
	}
}

// --- Key helpers ---

func identityKey(channelType, channelIdentity string) string {
	return fmt.Sprintf("identity:%s:%s", channelType, channelIdentity)
}

func allowedKey(userID, channelType string) string {
	return fmt.Sprintf("allowed:%s:%s", userID, channelType)
}

func userEnabledKey(userID string) string {
	return fmt.Sprintf("user_enabled:%s", userID)
}

func policyKey(userID, policyType string) string {
	return fmt.Sprintf("policy:%s:%s", userID, policyType)
}

func agentTokenKey(tokenHash string) string {
	return fmt.Sprintf("agent_token:%s", tokenHash)
}

// --- Cached reads ---

// ResolveIdentity returns the cached userID for a channel identity, querying the DB on miss.
func (c *CachedRepository) ResolveIdentity(ctx context.Context, channelType, channelIdentity string) (string, error) {
	key := identityKey(channelType, channelIdentity)
	if v, ok := c.cache.Get(key); ok {
		return v.(string), nil
	}
	userID, err := c.inner.ResolveIdentity(ctx, channelType, channelIdentity)
	if err != nil {
		return "", err
	}
	c.cache.Set(key, userID, identityTTL)
	return userID, nil
}

// IsAllowed returns the cached allowlist result, querying the DB on miss.
func (c *CachedRepository) IsAllowed(ctx context.Context, userID, channelType string) (bool, error) {
	key := allowedKey(userID, channelType)
	if v, ok := c.cache.Get(key); ok {
		return v.(bool), nil
	}
	allowed, err := c.inner.IsAllowed(ctx, userID, channelType)
	if err != nil {
		return false, err
	}
	c.cache.Set(key, allowed, allowedTTL)
	return allowed, nil
}

// IsUserEnabled returns the cached enabled flag, querying the DB on miss.
func (c *CachedRepository) IsUserEnabled(ctx context.Context, userID string) (bool, error) {
	key := userEnabledKey(userID)
	if v, ok := c.cache.Get(key); ok {
		return v.(bool), nil
	}
	enabled, err := c.inner.IsUserEnabled(ctx, userID)
	if err != nil {
		return false, err
	}
	c.cache.Set(key, enabled, userTTL)
	return enabled, nil
}

// GetUserPolicy returns the cached policy, querying the DB on miss.
func (c *CachedRepository) GetUserPolicy(ctx context.Context, userID, policyType string) (*models.PolicyData, error) {
	key := policyKey(userID, policyType)
	if v, ok := c.cache.Get(key); ok {
		if v == nil {
			return nil, nil
		}
		return v.(*models.PolicyData), nil
	}
	data, err := c.inner.GetUserPolicy(ctx, userID, policyType)
	if err != nil {
		return nil, err
	}
	c.cache.Set(key, data, policyTTL)
	return data, nil
}

// GetAgentByTokenHash returns the cached agent, querying the DB on miss.
func (c *CachedRepository) GetAgentByTokenHash(ctx context.Context, tokenHash string) (*models.Agent, error) {
	key := agentTokenKey(tokenHash)
	if v, ok := c.cache.Get(key); ok {
		if v == nil {
			return nil, nil
		}
		return v.(*models.Agent), nil
	}
	agent, err := c.inner.GetAgentByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	c.cache.Set(key, agent, agentTTL)
	return agent, nil
}

// --- Cache-invalidating writes (delegate to inner then bust the cache) ---

func (c *CachedRepository) AddIdentity(ctx context.Context, userID, channelType, channelIdentity string) error {
	if err := c.inner.AddIdentity(ctx, userID, channelType, channelIdentity); err != nil {
		return err
	}
	c.cache.Delete(identityKey(channelType, channelIdentity))
	return nil
}

func (c *CachedRepository) RemoveIdentity(ctx context.Context, id int64) error {
	// We don't cache by ID so flush all identity keys via a range delete is not possible efficiently.
	// The TTL will expire on its own; for correctness we just pass through.
	return c.inner.RemoveIdentity(ctx, id)
}

func (c *CachedRepository) SetAllowed(ctx context.Context, userID, channelType string, enabled bool, createdBy string) error {
	if err := c.inner.SetAllowed(ctx, userID, channelType, enabled, createdBy); err != nil {
		return err
	}
	c.cache.Delete(allowedKey(userID, channelType))
	return nil
}

func (c *CachedRepository) RemoveAllowed(ctx context.Context, userID, channelType string) error {
	if err := c.inner.RemoveAllowed(ctx, userID, channelType); err != nil {
		return err
	}
	c.cache.Delete(allowedKey(userID, channelType))
	return nil
}

func (c *CachedRepository) UpdateUser(ctx context.Context, userID string, updates models.UpdateUserRequest) (*models.User, error) {
	user, err := c.inner.UpdateUser(ctx, userID, updates)
	if err != nil {
		return nil, err
	}
	c.cache.Delete(userEnabledKey(userID))
	return user, nil
}

func (c *CachedRepository) DeleteUser(ctx context.Context, userID string) error {
	if err := c.inner.DeleteUser(ctx, userID); err != nil {
		return err
	}
	c.cache.Delete(userEnabledKey(userID))
	return nil
}

func (c *CachedRepository) SetUserPolicy(ctx context.Context, userID, policyType string, data *models.PolicyData) error {
	if err := c.inner.SetUserPolicy(ctx, userID, policyType, data); err != nil {
		return err
	}
	c.cache.Delete(policyKey(userID, policyType))
	return nil
}

func (c *CachedRepository) DeleteUserPolicy(ctx context.Context, userID, policyType string) error {
	if err := c.inner.DeleteUserPolicy(ctx, userID, policyType); err != nil {
		return err
	}
	c.cache.Delete(policyKey(userID, policyType))
	return nil
}

func (c *CachedRepository) RotateAgentToken(ctx context.Context, agentID, newTokenHash, newTokenPrefix string) error {
	// Bust old token cache — we don't know the old hash, so flush all agent tokens.
	// In practice, the TTL handles the rest within 60s.
	if err := c.inner.RotateAgentToken(ctx, agentID, newTokenHash, newTokenPrefix); err != nil {
		return err
	}
	c.cache.Flush()
	return nil
}

func (c *CachedRepository) UpdateAgentStatus(ctx context.Context, agentID string, status models.AgentStatus, meta *models.AgentRegistrationMeta) error {
	if err := c.inner.UpdateAgentStatus(ctx, agentID, status, meta); err != nil {
		return err
	}
	c.cache.Flush()
	return nil
}

func (c *CachedRepository) DeleteAgent(ctx context.Context, agentID string) error {
	if err := c.inner.DeleteAgent(ctx, agentID); err != nil {
		return err
	}
	c.cache.Flush()
	return nil
}

// --- Pass-throughs (no caching needed) ---

func (c *CachedRepository) CreateUser(ctx context.Context, req models.CreateUserRequest) (*models.User, error) {
	return c.inner.CreateUser(ctx, req)
}

func (c *CachedRepository) GetUser(ctx context.Context, userID string) (*models.User, error) {
	return c.inner.GetUser(ctx, userID)
}

func (c *CachedRepository) ListUsers(ctx context.Context) ([]*models.User, error) {
	return c.inner.ListUsers(ctx)
}

func (c *CachedRepository) ListIdentities(ctx context.Context, userID string) ([]*models.Identity, error) {
	return c.inner.ListIdentities(ctx, userID)
}

func (c *CachedRepository) ListAllowed(ctx context.Context, channelType string) ([]*models.AllowlistEntry, error) {
	return c.inner.ListAllowed(ctx, channelType)
}

func (c *CachedRepository) ListUserPolicies(ctx context.Context, userID string) ([]*models.UserPolicy, error) {
	return c.inner.ListUserPolicies(ctx, userID)
}

func (c *CachedRepository) ProvisionAgent(ctx context.Context, req models.ProvisionAgentRequest) (*models.Agent, error) {
	return c.inner.ProvisionAgent(ctx, req)
}

func (c *CachedRepository) GetAgent(ctx context.Context, agentID string) (*models.Agent, error) {
	return c.inner.GetAgent(ctx, agentID)
}

func (c *CachedRepository) ListAgents(ctx context.Context, userID string) ([]*models.Agent, error) {
	return c.inner.ListAgents(ctx, userID)
}

func (c *CachedRepository) SetAgentRMQUsername(ctx context.Context, agentID, rmqUsername string) error {
	return c.inner.SetAgentRMQUsername(ctx, agentID, rmqUsername)
}

func (c *CachedRepository) RevokeAgent(ctx context.Context, agentID string) error {
	if err := c.inner.RevokeAgent(ctx, agentID); err != nil {
		return err
	}
	c.cache.Flush()
	return nil
}

func (c *CachedRepository) ListCronJobs(ctx context.Context) ([]config.CronJob, error) {
	return c.inner.ListCronJobs(ctx)
}

func (c *CachedRepository) ListCronJobsByUser(ctx context.Context, userID string) ([]config.CronJob, error) {
	return c.inner.ListCronJobsByUser(ctx, userID)
}

func (c *CachedRepository) AddCronJob(ctx context.Context, job config.CronJob) error {
	return c.inner.AddCronJob(ctx, job)
}

func (c *CachedRepository) RemoveCronJob(ctx context.Context, id, userID string) error {
	return c.inner.RemoveCronJob(ctx, id, userID)
}

func (c *CachedRepository) ListAtJobs(ctx context.Context) ([]config.AtJob, error) {
	return c.inner.ListAtJobs(ctx)
}

func (c *CachedRepository) ListAtJobsByUser(ctx context.Context, userID string) ([]config.AtJob, error) {
	return c.inner.ListAtJobsByUser(ctx, userID)
}

func (c *CachedRepository) AddAtJob(ctx context.Context, job config.AtJob) error {
	return c.inner.AddAtJob(ctx, job)
}

func (c *CachedRepository) RemoveAtJob(ctx context.Context, id, userID string) error {
	return c.inner.RemoveAtJob(ctx, id, userID)
}

func (c *CachedRepository) LogAdminAction(ctx context.Context, entry models.AdminAuditEntry) error {
	return c.inner.LogAdminAction(ctx, entry)
}

func (c *CachedRepository) QueryAuditLog(ctx context.Context, filters models.AuditQueryFilters) ([]*models.AdminAuditEntry, error) {
	return c.inner.QueryAuditLog(ctx, filters)
}

func (c *CachedRepository) Backup(ctx context.Context, destPath string) error {
	return c.inner.Backup(ctx, destPath)
}

func (c *CachedRepository) Close() error {
	return c.inner.Close()
}

func (c *CachedRepository) Ping(ctx context.Context) error {
	return c.inner.Ping(ctx)
}

// compile-time interface check
var _ Repository = (*CachedRepository)(nil)
