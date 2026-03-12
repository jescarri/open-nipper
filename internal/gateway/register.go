package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/datastore"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/queue"
	"github.com/jescarri/open-nipper/internal/ratelimit"
)

// RegisterHandler implements POST /agents/register — the agent auto-registration endpoint.
type RegisterHandler struct {
	repo    datastore.Repository
	mgmt    queue.ManagementClient
	limiter *ratelimit.Limiter
	cfg     *config.Config
	logger  *zap.Logger
}

// RegisterHandlerDeps bundles the dependencies for the registration handler.
type RegisterHandlerDeps struct {
	Repo    datastore.Repository
	Mgmt    queue.ManagementClient
	Limiter *ratelimit.Limiter
	Config  *config.Config
	Logger  *zap.Logger
}

// NewRegisterHandler creates a new registration handler.
func NewRegisterHandler(deps RegisterHandlerDeps) *RegisterHandler {
	return &RegisterHandler{
		repo:    deps.Repo,
		mgmt:    deps.Mgmt,
		limiter: deps.Limiter,
		cfg:     deps.Config,
		logger:  deps.Logger,
	}
}

// registrationRequest is the optional JSON body from the agent.
type registrationRequest struct {
	AgentType    string   `json:"agent_type,omitempty"`
	Version      string   `json:"version,omitempty"`
	Hostname     string   `json:"hostname,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// registrationResponse is the full config blob returned to the agent.
type registrationResponse struct {
	OK     bool                    `json:"ok"`
	Result *registrationResultBlob `json:"result,omitempty"`
	Error  string                  `json:"error,omitempty"`
}

type registrationResultBlob struct {
	AgentID  string `json:"agent_id"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`

	RabbitMQ registrationRMQBlob `json:"rabbitmq"`

	User     registrationUserBlob     `json:"user"`
	Policies registrationPoliciesBlob `json:"policies"`
}

type registrationRMQBlob struct {
	URL      string `json:"url"`
	TLSURL   string `json:"tls_url,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
	VHost    string `json:"vhost"`

	Queues      registrationQueuesBlob      `json:"queues"`
	Exchanges   registrationExchangesBlob   `json:"exchanges"`
	RoutingKeys registrationRoutingKeysBlob `json:"routing_keys"`
}

type registrationQueuesBlob struct {
	Agent   string `json:"agent"`
	Control string `json:"control"`
}

type registrationExchangesBlob struct {
	Sessions string `json:"sessions"`
	Events   string `json:"events"`
	Control  string `json:"control"`
	Logs     string `json:"logs"`
}

type registrationRoutingKeysBlob struct {
	EventsPublish string `json:"events_publish"`
	LogsPublish   string `json:"logs_publish"`
}

type registrationUserBlob struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	DefaultModel string         `json:"default_model"`
	Preferences  map[string]any `json:"preferences"`
}

type registrationPoliciesBlob struct {
	Tools *models.PolicyData `json:"tools,omitempty"`
}

// Handle processes the POST /agents/register request.
func (h *RegisterHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Extract and validate Bearer token.
	token := extractBearerToken(r)
	if token == "" {
		h.logger.Warn("agent registration: missing or malformed authorization header",
			zap.String("remoteAddr", r.RemoteAddr),
		)
		writeRegisterError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// 2. Hash the token and look up the agent.
	tokenHash := hashToken(token)
	agent, err := h.repo.GetAgentByTokenHash(ctx, tokenHash)
	if err != nil {
		h.logger.Warn("agent registration: token not found",
			zap.String("remoteAddr", r.RemoteAddr),
		)
		h.auditRegistrationFailure(ctx, r, "", "invalid_token")
		writeRegisterError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// 3. Check agent status.
	if agent.Status == models.AgentStatusRevoked {
		h.logger.Warn("agent registration: agent revoked",
			zap.String("agentId", agent.ID),
			zap.String("remoteAddr", r.RemoteAddr),
		)
		h.auditRegistrationFailure(ctx, r, agent.UserID, "agent_revoked")
		writeRegisterError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// 4. Load user and check enabled.
	user, err := h.repo.GetUser(ctx, agent.UserID)
	if err != nil {
		h.logger.Warn("agent registration: user not found",
			zap.String("agentId", agent.ID),
			zap.String("userId", agent.UserID),
		)
		h.auditRegistrationFailure(ctx, r, agent.UserID, "user_not_found")
		writeRegisterError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !user.Enabled {
		h.logger.Warn("agent registration: user disabled",
			zap.String("agentId", agent.ID),
			zap.String("userId", agent.UserID),
		)
		h.auditRegistrationFailure(ctx, r, agent.UserID, "user_disabled")
		writeRegisterError(w, http.StatusForbidden, "user_disabled")
		return
	}

	// 5. Rate limiting — per token prefix.
	if h.limiter != nil {
		rateLimitKey := agent.TokenPrefix
		if rateLimitKey == "" {
			rateLimitKey = agent.ID
		}
		allowed, retryAfter := h.limiter.Allow(rateLimitKey)
		if !allowed {
			h.logger.Warn("agent registration: rate limited",
				zap.String("agentId", agent.ID),
				zap.Duration("retryAfter", retryAfter),
			)
			writeRegisterRateLimited(w, retryAfter)
			return
		}
	}

	// 6. Parse optional request body for metadata.
	var reqBody registrationRequest
	if r.Body != nil {
		// Errors ignored intentionally — body is optional metadata.
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}

	// 7. Ensure Management API is available.
	if h.mgmt == nil {
		h.logger.Error("agent registration: management API client not configured")
		writeRegisterError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}

	// 8. Generate RabbitMQ credentials.
	rmqUsername := fmt.Sprintf("agent-%s", agent.ID)
	rmqPassword, err := generateRMQPassword()
	if err != nil {
		h.logger.Error("agent registration: failed to generate RMQ password", zap.Error(err))
		writeRegisterError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	// 9. Create/update RabbitMQ user.
	if err := h.mgmt.CreateUser(ctx, rmqUsername, rmqPassword); err != nil {
		h.logger.Error("agent registration: failed to create RMQ user",
			zap.String("agentId", agent.ID),
			zap.String("rmqUsername", rmqUsername),
			zap.Error(err),
		)
		writeRegisterError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}

	// 10. Set vhost permissions.
	vhost := "/nipper"
	if h.cfg != nil && h.cfg.Queue.RabbitMQ.VHost != "" {
		vhost = h.cfg.Queue.RabbitMQ.VHost
	}

	perms := queue.VhostPermissions{
		Configure: fmt.Sprintf("^(nipper-agent-%s|nipper-control-%s)$", agent.UserID, agent.UserID),
		Write:     `^(nipper\.events|nipper\.logs)$`,
		Read:      fmt.Sprintf("^(nipper-agent-%s|nipper-control-%s)$", agent.UserID, agent.UserID),
	}
	if err := h.mgmt.SetVhostPermissions(ctx, vhost, rmqUsername, perms); err != nil {
		h.logger.Error("agent registration: failed to set vhost permissions",
			zap.String("agentId", agent.ID),
			zap.String("rmqUsername", rmqUsername),
			zap.Error(err),
		)
		writeRegisterError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}

	// 10b. Best-effort cleanup of stale agent connections.
	// Password rotation does not terminate existing RabbitMQ connections, so old
	// agent processes may keep consuming from the same queue until disconnected.
	type staleConnCloser interface {
		CloseUserConnections(ctx context.Context, username string) error
	}
	if closer, ok := h.mgmt.(staleConnCloser); ok {
		if err := closer.CloseUserConnections(ctx, rmqUsername); err != nil {
			h.logger.Warn("agent registration: failed to close stale RMQ connections",
				zap.String("agentId", agent.ID),
				zap.String("rmqUsername", rmqUsername),
				zap.Error(err),
			)
		} else {
			h.logger.Info("agent registration: stale RMQ connections closed",
				zap.String("agentId", agent.ID),
				zap.String("rmqUsername", rmqUsername),
			)
		}
	}

	// 11. Update datastore.
	regMeta := &models.AgentRegistrationMeta{
		IP:        clientIP(r),
		AgentType: reqBody.AgentType,
		Version:   reqBody.Version,
	}
	if err := h.repo.UpdateAgentStatus(ctx, agent.ID, models.AgentStatusRegistered, regMeta); err != nil {
		h.logger.Error("agent registration: failed to update agent status",
			zap.String("agentId", agent.ID),
			zap.Error(err),
		)
		writeRegisterError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	if err := h.repo.SetAgentRMQUsername(ctx, agent.ID, rmqUsername); err != nil {
		h.logger.Error("agent registration: failed to set RMQ username",
			zap.String("agentId", agent.ID),
			zap.Error(err),
		)
		// Non-fatal — registration already succeeded in RMQ.
	}

	// 12. Log audit entry.
	h.auditRegistrationSuccess(ctx, r, agent, reqBody)

	// 13. Load user policies for the response.
	// Prefer per-user policy; fall back to the gateway-level default only when
	// the default actually has rules. An empty allow+deny list means "no policy"
	// (all tools permitted), so we intentionally leave toolsPolicy nil in that
	// case — sending a non-nil empty policy would cause isAllowed() to behave
	// identically to nil, but an old bug had the gateway config's allow list
	// inadvertently blocking built-in agent tools like web_fetch.
	var toolsPolicy *models.PolicyData
	if pd, err := h.repo.GetUserPolicy(ctx, agent.UserID, "tools"); err == nil && pd != nil {
		toolsPolicy = pd
	}
	if toolsPolicy == nil && h.cfg != nil {
		gp := h.cfg.Security.Tools.Policy
		if len(gp.Allow) > 0 || len(gp.Deny) > 0 {
			toolsPolicy = &models.PolicyData{
				Allow: gp.Allow,
				Deny:  gp.Deny,
			}
		}
	}

	// 14. Build and return the response.
	rmqURL := ""
	if h.cfg != nil {
		rmqURL = h.cfg.Queue.RabbitMQ.URL
	}

	prefs := user.Preferences
	if prefs == nil {
		prefs = map[string]any{}
	}

	resp := registrationResponse{
		OK: true,
		Result: &registrationResultBlob{
			AgentID:  agent.ID,
			UserID:   agent.UserID,
			UserName: user.Name,

			RabbitMQ: registrationRMQBlob{
				URL:      rmqURL,
				Username: rmqUsername,
				Password: rmqPassword,
				VHost:    vhost,
				Queues: registrationQueuesBlob{
					Agent:   queue.UserAgentQueue(agent.UserID),
					Control: queue.UserControlQueue(agent.UserID),
				},
				Exchanges: registrationExchangesBlob{
					Sessions: queue.ExchangeSessions,
					Events:   queue.ExchangeEvents,
					Control:  queue.ExchangeControl,
					Logs:     "nipper.logs",
				},
				RoutingKeys: registrationRoutingKeysBlob{
					EventsPublish: fmt.Sprintf("nipper.events.%s.{sessionId}", agent.UserID),
					LogsPublish:   fmt.Sprintf("nipper.logs.%s.{eventType}", agent.UserID),
				},
			},

			User: registrationUserBlob{
				ID:           user.ID,
				Name:         user.Name,
				DefaultModel: user.DefaultModel,
				Preferences:  prefs,
			},

			Policies: registrationPoliciesBlob{
				Tools: toolsPolicy,
			},
		},
	}

	h.logger.Info("agent registered",
		zap.String("agentId", agent.ID),
		zap.String("userId", agent.UserID),
		zap.String("rmqUsername", rmqUsername),
		zap.String("remoteAddr", clientIP(r)),
		zap.String("agentType", reqBody.AgentType),
		zap.String("agentVersion", reqBody.Version),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// extractBearerToken pulls the token from "Authorization: Bearer <token>".
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	return token
}

// hashToken computes the SHA-256 hex digest of a token string.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

// generateRMQPassword creates a 32-byte cryptographically random password,
// encoded as URL-safe base64 (no padding).
func generateRMQPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// clientIP extracts the best-guess client IP from the request.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host
}

// writeRegisterError writes a standard error JSON response.
func writeRegisterError(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":    false,
		"error": errMsg,
	})
}

// writeRegisterRateLimited writes a 429 with retry_after.
func writeRegisterRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	retrySeconds := int(retryAfter.Seconds())
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          false,
		"error":       "rate_limited",
		"retry_after": retrySeconds,
	})
}

func (h *RegisterHandler) auditRegistrationFailure(ctx context.Context, r *http.Request, userID, reason string) {
	entry := models.AdminAuditEntry{
		Timestamp:    time.Now().UTC(),
		Action:       "agent.register_failed",
		Actor:        "system",
		TargetUserID: userID,
		Details:      fmt.Sprintf(`{"reason":%q,"ip":%q}`, reason, clientIP(r)),
		IPAddress:    clientIP(r),
	}
	if err := h.repo.LogAdminAction(ctx, entry); err != nil {
		h.logger.Error("failed to write audit entry", zap.Error(err))
	}
}

func (h *RegisterHandler) auditRegistrationSuccess(ctx context.Context, r *http.Request, agent *models.Agent, reqBody registrationRequest) {
	entry := models.AdminAuditEntry{
		Timestamp:    time.Now().UTC(),
		Action:       "agent.registered",
		Actor:        "system",
		TargetUserID: agent.UserID,
		Details:      fmt.Sprintf(`{"agent_id":%q,"agent_type":%q,"version":%q,"ip":%q}`, agent.ID, reqBody.AgentType, reqBody.Version, clientIP(r)),
		IPAddress:    clientIP(r),
	}
	if err := h.repo.LogAdminAction(ctx, entry); err != nil {
		h.logger.Error("failed to write audit entry", zap.Error(err))
	}
}
