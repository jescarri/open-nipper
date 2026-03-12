package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/datastore"
)

// AtJobMutator adds/removes at jobs at runtime (implemented by cron.Adapter).
type AtJobMutator interface {
	AddAtJob(ctx context.Context, job config.AtJob) error
	RemoveAtJob(ctx context.Context, userID, id string) bool
}

// AgentAtHandler implements GET/POST/DELETE /agents/me/at/jobs (Bearer auth, scoped to agent's user).
type AgentAtHandler struct {
	repo   datastore.Repository
	at     AtJobMutator
	logger *zap.Logger
}

// AgentAtHandlerDeps bundles dependencies.
type AgentAtHandlerDeps struct {
	Repo   datastore.Repository
	At     AtJobMutator
	Logger *zap.Logger
}

// NewAgentAtHandler creates a new AgentAtHandler.
func NewAgentAtHandler(deps AgentAtHandlerDeps) *AgentAtHandler {
	return &AgentAtHandler{
		repo:   deps.Repo,
		at:     deps.At,
		logger: deps.Logger,
	}
}

type atListResponse struct {
	OK     bool            `json:"ok"`
	Result []config.AtJob  `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type atAddRequest struct {
	ID     string `json:"id"`
	RunAt  string `json:"run_at"`
	Prompt string `json:"prompt"`
}

type atAddResponse struct {
	OK     bool           `json:"ok"`
	Result *config.AtJob  `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// HandleList returns GET /agents/me/at/jobs handler.
func (h *AgentAtHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	jobs, err := h.repo.ListAtJobsByUser(ctx, userID)
	if err != nil {
		h.logger.Error("list at jobs", zap.String("userId", userID), zap.Error(err))
		writeAtJSON(w, http.StatusInternalServerError, atListResponse{OK: false, Error: "internal error"})
		return
	}
	writeAtJSON(w, http.StatusOK, atListResponse{OK: true, Result: jobs})
}

// HandleAdd returns POST /agents/me/at/jobs handler.
func (h *AgentAtHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	var body atAddRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: "invalid JSON"})
		return
	}
	if body.ID == "" || body.RunAt == "" || body.Prompt == "" {
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: "id, run_at, and prompt are required"})
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: "prompt must be non-empty"})
		return
	}
	runAt, err := time.Parse(time.RFC3339, body.RunAt)
	if err != nil {
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: "run_at must be a valid RFC3339 timestamp"})
		return
	}
	if runAt.Before(time.Now()) {
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: "run_at is in the past"})
		return
	}
	if h.at == nil {
		writeAtJSON(w, http.StatusServiceUnavailable, atAddResponse{OK: false, Error: "cron channel is disabled (at jobs require cron channel)"})
		return
	}
	// Resolve notify channels server-side from the user's registered identities.
	notifyChannels, err := resolveNotifyChannels(ctx, h.repo, userID)
	if err != nil {
		h.logger.Error("resolve notify channels", zap.String("userId", userID), zap.Error(err))
		writeAtJSON(w, http.StatusInternalServerError, atAddResponse{OK: false, Error: "failed to resolve delivery channels"})
		return
	}
	job := config.AtJob{
		ID:             body.ID,
		RunAt:          body.RunAt,
		UserID:         userID,
		Prompt:         body.Prompt,
		NotifyChannels: notifyChannels,
	}
	if err := h.repo.AddAtJob(ctx, job); err != nil {
		h.logger.Debug("add at job db", zap.String("userId", userID), zap.String("id", body.ID), zap.Error(err))
		writeAtJSON(w, http.StatusConflict, atAddResponse{OK: false, Error: "job id already exists for this user"})
		return
	}
	if err := h.at.AddAtJob(ctx, job); err != nil {
		_ = h.repo.RemoveAtJob(ctx, body.ID, userID) // rollback DB
		h.logger.Debug("add at job scheduler", zap.String("id", body.ID), zap.Error(err))
		writeAtJSON(w, http.StatusBadRequest, atAddResponse{OK: false, Error: err.Error()})
		return
	}
	writeAtJSON(w, http.StatusCreated, atAddResponse{OK: true, Result: &job})
}

// HandleRemove returns DELETE /agents/me/at/jobs/{id} handler.
func (h *AgentAtHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.resolveAgentUser(w, r)
	if !ok {
		return
	}
	id := mux.Vars(r)["id"]
	if id == "" {
		writeAtJSON(w, http.StatusBadRequest, atListResponse{OK: false, Error: "job id required"})
		return
	}
	if err := h.repo.RemoveAtJob(ctx, id, userID); err != nil {
		writeAtJSON(w, http.StatusNotFound, atListResponse{OK: false, Error: "job not found or not owned by user"})
		return
	}
	if h.at != nil {
		h.at.RemoveAtJob(ctx, userID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveAgentUser validates Bearer token and returns the user ID.
func (h *AgentAtHandler) resolveAgentUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := extractBearerToken(r)
	if token == "" {
		writeAtJSON(w, http.StatusUnauthorized, atListResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	agent, err := h.repo.GetAgentByTokenHash(r.Context(), hashToken(token))
	if err != nil {
		writeAtJSON(w, http.StatusUnauthorized, atListResponse{OK: false, Error: "unauthorized"})
		return "", false
	}
	if agent.Status != "provisioned" && agent.Status != "registered" {
		writeAtJSON(w, http.StatusForbidden, atListResponse{OK: false, Error: "agent revoked"})
		return "", false
	}
	agentUserID := agent.UserID
	headerUserID := strings.TrimSpace(r.Header.Get(cronUserIDHeader))
	if headerUserID != "" {
		if headerUserID != agentUserID {
			writeAtJSON(w, http.StatusForbidden, atListResponse{OK: false, Error: "X-Nipper-User-Id does not match agent"})
			return "", false
		}
		return headerUserID, true
	}
	return agentUserID, true
}

func writeAtJSON(w http.ResponseWriter, status int, v interface{}) {
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
