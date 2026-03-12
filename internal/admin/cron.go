package admin

import (
	"net/http"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// handleListCronJobs handles GET /admin/cron/jobs (all cron jobs).
func (s *Server) handleListCronJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.repo.ListCronJobs(r.Context())
	if err != nil {
		s.logger.Error("list cron jobs failed", zap.Error(err))
		writeInternalError(w, "failed to list cron jobs")
		return
	}
	if jobs == nil {
		jobs = []config.CronJob{}
	}
	writeOK(w, jobs)
}

// handleListCronJobsByUser handles GET /admin/users/{userId}/cron/jobs (cron jobs for one user).
func (s *Server) handleListCronJobsByUser(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	if userID == "" {
		writeBadRequest(w, "userId required")
		return
	}
	jobs, err := s.repo.ListCronJobsByUser(r.Context(), userID)
	if err != nil {
		s.logger.Error("list cron jobs by user failed", zap.Error(err), zap.String("userId", userID))
		writeInternalError(w, "failed to list cron jobs")
		return
	}
	if jobs == nil {
		jobs = []config.CronJob{}
	}
	writeOK(w, jobs)
}
