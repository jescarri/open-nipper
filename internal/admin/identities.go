package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/models"
)

func (s *Server) handleAddIdentity(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]

	var req struct {
		ChannelType     string `json:"channel_type"`
		ChannelIdentity string `json:"channel_identity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	if req.ChannelType == "" || req.ChannelIdentity == "" {
		writeBadRequest(w, "channel_type and channel_identity are required")
		return
	}

	if err := s.repo.AddIdentity(r.Context(), userID, req.ChannelType, req.ChannelIdentity); err != nil {
		s.logger.Error("add identity failed", zap.Error(err), zap.String("userId", userID))
		writeInternalError(w, "failed to add identity")
		return
	}

	s.logAudit(r, "identity.added", "admin", userID,
		fmt.Sprintf(`{"channel_type":%q,"channel_identity":"[REDACTED]"}`, req.ChannelType))
	writeCreated(w, map[string]interface{}{"userId": userID, "channelType": req.ChannelType})
}

func (s *Server) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]

	identities, err := s.repo.ListIdentities(r.Context(), userID)
	if err != nil {
		s.logger.Error("list identities failed", zap.Error(err), zap.String("userId", userID))
		writeInternalError(w, "failed to list identities")
		return
	}
	if identities == nil {
		identities = []*models.Identity{}
	}
	writeOK(w, identities)
}

func (s *Server) handleRemoveIdentity(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userId"]
	idStr := vars["id"]

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeBadRequest(w, "invalid identity id")
		return
	}

	if err := s.repo.RemoveIdentity(r.Context(), id); err != nil {
		s.logger.Error("remove identity failed", zap.Error(err), zap.Int64("identityId", id))
		writeNotFound(w, "identity not found")
		return
	}

	s.logAudit(r, "identity.removed", "admin", userID,
		fmt.Sprintf(`{"identity_id":%d}`, id))
	writeOK(w, map[string]interface{}{"deleted": true, "id": id})
}
