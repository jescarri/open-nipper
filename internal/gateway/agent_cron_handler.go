package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/datastore"
)

// CronJobMutator adds/removes cron jobs at runtime (implemented by cron.Adapter).
type CronJobMutator interface {
	AddJob(ctx context.Context, job config.CronJob) error
	RemoveJob(ctx context.Context, userID, id string) bool
}

// AgentCronHandler implements GET/POST/DELETE /agents/me/cron/jobs (Bearer auth, scoped to agent's user).
type AgentCronHandler struct {
	repo   datastore.Repository
	cron   CronJobMutator
	logger *zap.Logger
}

// AgentCronHandlerDeps bundles dependencies.
type AgentCronHandlerDeps struct {
	Repo   datastore.Repository
	Cron   CronJobMutator
	Logger *zap.Logger
}

// NewAgentCronHandler creates a new AgentCronHandler.
func NewAgentCronHandler(deps AgentCronHandlerDeps) *AgentCronHandler {
	return &AgentCronHandler{
		repo:   deps.Repo,
		cron:   deps.Cron,
		logger: deps.Logger,
	}
}

// cronListResponse is the response for GET /agents/me/cron/jobs.
// Agent tools expect this shape: { "ok": bool, "result": array, "error": string }.
type cronListResponse struct {
	OK     bool             `json:"ok"`
	Result []config.CronJob `json:"result,omitempty"`
	Error  string           `json:"error,omitempty"`
}

// cronAddRequest is the body for POST /agents/me/cron/jobs.
// NotifyChannels is never accepted from the caller — it is resolved server-side
// from the user's registered identities.
type cronAddRequest struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
}

// cronAddResponse is the response for POST /agents/me/cron/jobs.
// Agent tools expect this exact shape: { "ok": bool, "result": object|null, "error": string }.
// Do not return a bare number or other format; the cron_add_job tool decodes this struct.
type cronAddResponse struct {
	OK     bool           `json:"ok"`
	Result *config.CronJob `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// HandleList returns GET /agents/me/cron/jobs handler.
func (h *AgentCronHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	jobs, err := h.repo.ListCronJobsByUser(ctx, userID)
	if err != nil {
		h.logger.Error("list cron jobs", zap.String("userId", userID), zap.Error(err))
		writeCronJSON(w, http.StatusInternalServerError, cronListResponse{OK: false, Error: "internal error"})
		return
	}
	writeCronJSON(w, http.StatusOK, cronListResponse{OK: true, Result: jobs})
}

// HandleAdd returns POST /agents/me/cron/jobs handler.
func (h *AgentCronHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	var body cronAddRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeCronJSON(w, http.StatusBadRequest, cronAddResponse{OK: false, Error: "invalid JSON"})
		return
	}
	if body.ID == "" || body.Schedule == "" || body.Prompt == "" {
		writeCronJSON(w, http.StatusBadRequest, cronAddResponse{OK: false, Error: "id, schedule, and prompt are required"})
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeCronJSON(w, http.StatusBadRequest, cronAddResponse{OK: false, Error: "prompt must be non-empty (cron jobs are prompts only)"})
		return
	}
	if h.cron == nil {
		writeCronJSON(w, http.StatusServiceUnavailable, cronAddResponse{OK: false, Error: "cron channel is disabled"})
		return
	}
	// Resolve notify channels server-side from the user's registered identities.
	// The LLM never supplies these — outbound routing is always programmatic.
	notifyChannels, err := resolveNotifyChannels(ctx, h.repo, userID)
	if err != nil {
		h.logger.Error("resolve notify channels", zap.String("userId", userID), zap.Error(err))
		writeCronJSON(w, http.StatusInternalServerError, cronAddResponse{OK: false, Error: "failed to resolve delivery channels"})
		return
	}
	job := config.CronJob{
		ID:             body.ID,
		Schedule:       body.Schedule,
		UserID:         userID,
		Prompt:         body.Prompt,
		NotifyChannels: notifyChannels,
	}
	if err := h.repo.AddCronJob(ctx, job); err != nil {
		h.logger.Debug("add cron job db", zap.String("userId", userID), zap.String("id", body.ID), zap.Error(err))
		writeCronJSON(w, http.StatusConflict, cronAddResponse{OK: false, Error: "job id already exists for this user"})
		return
	}
	if err := h.cron.AddJob(ctx, job); err != nil {
		_ = h.repo.RemoveCronJob(ctx, body.ID, userID) // rollback DB
		h.logger.Debug("add cron job scheduler", zap.String("id", body.ID), zap.Error(err))
		writeCronJSON(w, http.StatusBadRequest, cronAddResponse{OK: false, Error: "invalid schedule or job already registered: " + err.Error()})
		return
	}
	writeCronJSON(w, http.StatusCreated, cronAddResponse{OK: true, Result: &job})
}

// HandleRemove returns DELETE /agents/me/cron/jobs/{id} handler.
func (h *AgentCronHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	id := mux.Vars(r)["id"]
	if id == "" {
		writeCronJSON(w, http.StatusBadRequest, cronListResponse{OK: false, Error: "job id required"})
		return
	}
	if err := h.repo.RemoveCronJob(ctx, id, userID); err != nil {
		writeCronJSON(w, http.StatusNotFound, cronListResponse{OK: false, Error: "job not found or not owned by user"})
		return
	}
	if h.cron != nil {
		h.cron.RemoveJob(ctx, userID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveNotifyChannels builds the notify_channels list from the user's
// registered and allowed identities. Outbound routing is always resolved
// server-side — the LLM never supplies channel targets.
func resolveNotifyChannels(ctx context.Context, repo datastore.Repository, userID string) ([]string, error) {
	identities, err := repo.ListIdentities(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	var channels []string
	for _, id := range identities {
		if id.ChannelType == "cron" {
			continue
		}
		allowed, err := repo.IsAllowed(ctx, userID, id.ChannelType)
		if err != nil || !allowed {
			continue
		}
		channels = append(channels, id.ChannelType+":"+id.ChannelIdentity)
	}
	return channels, nil
}

const cronUserIDHeader = "X-Nipper-User-Id"

// resolveAgentUser validates Bearer token and returns the user ID to use for cron operations.
// When X-Nipper-User-Id is present, it must match the agent's user (session scope); otherwise the agent's user is used.
// Returns false and writes error response if invalid.
func (h *AgentCronHandler) resolveAgentUser(w http.ResponseWriter, r *http.Request) (userID string, ok bool) {
	token := extractBearerToken(r)
	if token == "" {
		writeCronJSON(w, http.StatusUnauthorized, cronListResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	agent, err := h.repo.GetAgentByTokenHash(r.Context(), hashToken(token))
	if err != nil {
		writeCronJSON(w, http.StatusUnauthorized, cronListResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	if agent.Status != "provisioned" && agent.Status != "registered" {
		writeCronJSON(w, http.StatusForbidden, cronListResponse{OK: false, Error: "agent revoked"})
		return "", false
	}
	agentUserID := agent.UserID
	headerUserID := strings.TrimSpace(r.Header.Get(cronUserIDHeader))
	if headerUserID != "" {
		if headerUserID != agentUserID {
			writeCronJSON(w, http.StatusForbidden, cronListResponse{OK: false, Error: "X-Nipper-User-Id does not match agent"})
			return "", false
		}
		return headerUserID, true
	}
	return agentUserID, true
}

// writeCronJSON writes a single JSON value as the entire response body with no trailing newline.
// We marshal and write the bytes explicitly so the body is exactly one JSON value (no extra newline
// or second write that could cause "invalid character after top-level value" on the client).
func writeCronJSON(w http.ResponseWriter, status int, v interface{}) {
	body, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
