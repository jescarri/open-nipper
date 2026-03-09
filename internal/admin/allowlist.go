package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

func (s *Server) handleAddAllowlist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID      string `json:"user_id"`
		ChannelType string `json:"channel_type"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	if req.UserID == "" || req.ChannelType == "" {
		writeBadRequest(w, "user_id and channel_type are required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := s.repo.SetAllowed(r.Context(), req.UserID, req.ChannelType, enabled, "admin"); err != nil {
		s.logger.Error("set allowlist failed", zap.Error(err))
		writeInternalError(w, "failed to set allowlist entry")
		return
	}

	s.logAudit(r, "allowlist.added", "admin", req.UserID,
		fmt.Sprintf(`{"channel_type":%q,"enabled":%v}`, req.ChannelType, enabled))
	writeCreated(w, map[string]interface{}{"userId": req.UserID, "channelType": req.ChannelType, "enabled": enabled})
}

func (s *Server) handleListAllowlist(w http.ResponseWriter, r *http.Request) {
	entries, err := s.repo.ListAllowed(r.Context(), "")
	if err != nil {
		s.logger.Error("list allowlist failed", zap.Error(err))
		writeInternalError(w, "failed to list allowlist")
		return
	}
	if entries == nil {
		entries = []*models.AllowlistEntry{}
	}
	writeOK(w, entries)
}

func (s *Server) handleListAllowlistByChannel(w http.ResponseWriter, r *http.Request) {
	channelType := mux.Vars(r)["channelType"]
	entries, err := s.repo.ListAllowed(r.Context(), channelType)
	if err != nil {
		s.logger.Error("list allowlist by channel failed", zap.Error(err))
		writeInternalError(w, "failed to list allowlist")
		return
	}
	if entries == nil {
		entries = []*models.AllowlistEntry{}
	}
	writeOK(w, entries)
}

func (s *Server) handleUpdateAllowlist(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userId"]
	channelType := vars["channelType"]

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}

	if err := s.repo.SetAllowed(r.Context(), userID, channelType, req.Enabled, "admin"); err != nil {
		s.logger.Error("update allowlist failed", zap.Error(err))
		writeInternalError(w, "failed to update allowlist entry")
		return
	}

	s.logAudit(r, "allowlist.updated", "admin", userID,
		fmt.Sprintf(`{"channel_type":%q,"enabled":%v}`, channelType, req.Enabled))
	writeOK(w, map[string]interface{}{"userId": userID, "channelType": channelType, "enabled": req.Enabled})
}

func (s *Server) handleRemoveAllowlist(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userId"]
	channelType := vars["channelType"]

	if err := s.repo.RemoveAllowed(r.Context(), userID, channelType); err != nil {
		s.logger.Error("remove allowlist failed", zap.Error(err))
		writeNotFound(w, "allowlist entry not found")
		return
	}

	s.logAudit(r, "allowlist.removed", "admin", userID,
		fmt.Sprintf(`{"channel_type":%q}`, channelType))
	writeOK(w, map[string]interface{}{"deleted": true, "userId": userID, "channelType": channelType})
}
