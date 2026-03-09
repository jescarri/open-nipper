package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	policies, err := s.repo.ListUserPolicies(r.Context(), userID)
	if err != nil {
		s.logger.Error("list policies failed", zap.Error(err), zap.String("userId", userID))
		writeInternalError(w, "failed to list policies")
		return
	}
	if policies == nil {
		policies = []*models.UserPolicy{}
	}
	writeOK(w, policies)
}

func (s *Server) handleSetPolicy(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userId"]
	policyType := vars["type"]

	var data models.PolicyData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}

	if err := s.repo.SetUserPolicy(r.Context(), userID, policyType, &data); err != nil {
		s.logger.Error("set policy failed", zap.Error(err), zap.String("userId", userID), zap.String("policyType", policyType))
		writeInternalError(w, "failed to set policy")
		return
	}

	s.logAudit(r, "policy.set", "admin", userID,
		fmt.Sprintf(`{"policy_type":%q}`, policyType))
	writeOK(w, map[string]interface{}{"userId": userID, "policyType": policyType})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	userID := vars["userId"]
	policyType := vars["type"]

	if err := s.repo.DeleteUserPolicy(r.Context(), userID, policyType); err != nil {
		s.logger.Error("delete policy failed", zap.Error(err), zap.String("userId", userID), zap.String("policyType", policyType))
		writeNotFound(w, "policy not found")
		return
	}

	s.logAudit(r, "policy.deleted", "admin", userID,
		fmt.Sprintf(`{"policy_type":%q}`, policyType))
	writeOK(w, map[string]interface{}{"deleted": true, "userId": userID, "policyType": policyType})
}
