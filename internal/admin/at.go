package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// handleListAtJobs handles GET /admin/at/jobs (all at jobs).
func (s *Server) handleListAtJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.repo.ListAtJobs(r.Context())
	if err != nil {
		s.logger.Error("list at jobs failed", zap.Error(err))
		writeInternalError(w, "failed to list at jobs")
		return
	}
	if jobs == nil {
		jobs = []config.AtJob{}
	}
	writeOK(w, jobs)
}

// handleListAtJobsByUser handles GET /admin/users/{userId}/at/jobs.
func (s *Server) handleListAtJobsByUser(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	if userID == "" {
		writeBadRequest(w, "userId required")
		return
	}
	jobs, err := s.repo.ListAtJobsByUser(r.Context(), userID)
	if err != nil {
		s.logger.Error("list at jobs by user failed", zap.Error(err), zap.String("userId", userID))
		writeInternalError(w, "failed to list at jobs")
		return
	}
	if jobs == nil {
		jobs = []config.AtJob{}
	}
	writeOK(w, jobs)
}

// handleAddAtJob handles POST /admin/at/jobs.
func (s *Server) handleAddAtJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID         string   `json:"user_id"`
		ID             string   `json:"id"`
		RunAt          string   `json:"run_at"`
		Prompt         string   `json:"prompt"`
		NotifyChannels []string `json:"notify_channels,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	if req.UserID == "" || req.ID == "" || req.RunAt == "" || req.Prompt == "" {
		writeBadRequest(w, "user_id, id, run_at, and prompt are required")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeBadRequest(w, "prompt must be non-empty")
		return
	}
	if _, err := time.Parse(time.RFC3339, req.RunAt); err != nil {
		writeBadRequest(w, "run_at must be a valid RFC3339 timestamp")
		return
	}

	job := config.AtJob{
		ID:             req.ID,
		RunAt:          req.RunAt,
		UserID:         req.UserID,
		Prompt:         req.Prompt,
		NotifyChannels: req.NotifyChannels,
	}
	if err := s.repo.AddAtJob(r.Context(), job); err != nil {
		s.logger.Error("add at job failed", zap.Error(err), zap.String("id", req.ID))
		writeInternalError(w, "failed to add at job")
		return
	}
	// If an at job mutator is available, schedule it.
	if s.atMutator != nil {
		if err := s.atMutator.AddAtJob(r.Context(), job); err != nil {
			_ = s.repo.RemoveAtJob(r.Context(), req.ID, req.UserID)
			s.logger.Error("schedule at job failed", zap.Error(err), zap.String("id", req.ID))
			writeInternalError(w, "failed to schedule at job: "+err.Error())
			return
		}
	}
	s.logAudit(r, "at_job.created", "admin", req.UserID, `{"job_id":"`+req.ID+`"}`)
	writeCreated(w, job)
}

// handleRemoveAtJob handles DELETE /admin/at/jobs/{userId}/{id}.
func (s *Server) handleRemoveAtJob(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	id := mux.Vars(r)["id"]
	if userID == "" || id == "" {
		writeBadRequest(w, "userId and id required")
		return
	}
	if err := s.repo.RemoveAtJob(r.Context(), id, userID); err != nil {
		writeNotFound(w, "at job not found or not owned by user")
		return
	}
	if s.atMutator != nil {
		s.atMutator.RemoveAtJob(r.Context(), userID, id)
	}
	s.logAudit(r, "at_job.deleted", "admin", userID, `{"job_id":"`+id+`"}`)
	writeOK(w, map[string]string{"status": "deleted"})
}
