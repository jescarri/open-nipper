package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/datastore"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/telemetry"
)

// AgentHealthHandler implements POST /agents/health — agents POST their status for metrics.
// Heartbeats are stored in memory only (never written to DB).
type AgentHealthHandler struct {
	repo     datastore.Repository
	metrics  *telemetry.Metrics
	heartbeat *AgentHeartbeatStore
	logger   *zap.Logger
}

// AgentHealthHandlerDeps bundles dependencies for the health handler.
type AgentHealthHandlerDeps struct {
	Repo      datastore.Repository
	Metrics   *telemetry.Metrics
	Heartbeat *AgentHeartbeatStore
	Logger    *zap.Logger
}

// NewAgentHealthHandler creates a new AgentHealthHandler.
func NewAgentHealthHandler(deps AgentHealthHandlerDeps) *AgentHealthHandler {
	return &AgentHealthHandler{
		repo:      deps.Repo,
		metrics:   deps.Metrics,
		heartbeat: deps.Heartbeat,
		logger:    deps.Logger,
	}
}

// healthRequest is the JSON body for POST /agents/health.
type healthRequest struct {
	Status  string `json:"status"`  // "healthy", "degraded", "unhealthy"
	Message string `json:"message"` // optional detail
}

// healthResponse is the JSON response.
type healthResponse struct {
	OK   bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Handle processes POST /agents/health. Requires Bearer token (same as /agents/register).
// Records nipper_agent_health_reports_total with agent_id and status.
func (h *AgentHealthHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token := extractBearerToken(r)
	if token == "" {
		h.logger.Debug("agent health: missing or malformed authorization header")
		writeHealthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	agent, err := h.repo.GetAgentByTokenHash(ctx, hashToken(token))
	if err != nil {
		h.logger.Debug("agent health: token not found")
		writeHealthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if agent.Status == models.AgentStatusRevoked {
		h.logger.Debug("agent health: agent revoked", zap.String("agentId", agent.ID))
		writeHealthError(w, http.StatusForbidden, "agent revoked")
		return
	}

	var body healthRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	status := normalizeHealthStatus(body.Status)
	telemetry.RecordAgentHealthReport(ctx, h.metrics, agent.ID, status)
	if h.heartbeat != nil {
		h.heartbeat.Record(agent.ID, agent.UserID, status)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{OK: true})
}

func writeHealthError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(healthResponse{OK: false, Error: msg})
}

func normalizeHealthStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "healthy":
		return "healthy"
	case "degraded":
		return "degraded"
	case "unhealthy":
		return "unhealthy"
	default:
		return "unknown"
	}
}
