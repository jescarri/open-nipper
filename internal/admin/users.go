package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req models.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	if req.Name == "" {
		writeBadRequest(w, "name is required")
		return
	}

	user, err := s.repo.CreateUser(r.Context(), req)
	if err != nil {
		s.logger.Error("create user failed", zap.Error(err), zap.String("name", req.Name))
		writeInternalError(w, "failed to create user")
		return
	}

	s.logAudit(r, "user.created", "admin", user.ID, `{"user_id":"`+user.ID+`"}`)
	writeCreated(w, user)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.repo.ListUsers(r.Context())
	if err != nil {
		s.logger.Error("list users failed", zap.Error(err))
		writeInternalError(w, "failed to list users")
		return
	}
	if users == nil {
		users = []*models.User{}
	}
	writeOK(w, users)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	user, err := s.repo.GetUser(r.Context(), userID)
	if err != nil {
		s.logger.Error("get user failed", zap.Error(err), zap.String("userId", userID))
		writeNotFound(w, "user not found")
		return
	}
	writeOK(w, user)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]
	var req models.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}

	user, err := s.repo.UpdateUser(r.Context(), userID, req)
	if err != nil {
		s.logger.Error("update user failed", zap.Error(err), zap.String("userId", userID))
		writeNotFound(w, "user not found")
		return
	}

	s.logAudit(r, "user.updated", "admin", userID, `{"user_id":"`+userID+`"}`)
	writeOK(w, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userId"]

	// Clean up RabbitMQ resources if management client is available.
	if s.mgmt != nil {
		agents, err := s.repo.ListAgents(r.Context(), userID)
		if err == nil {
			for _, agent := range agents {
				if agent.RMQUsername != "" {
					if err := s.mgmt.DeleteUser(r.Context(), agent.RMQUsername); err != nil {
						s.logger.Warn("failed to delete RMQ user for agent",
							zap.String("agentId", agent.ID),
							zap.String("rmqUsername", agent.RMQUsername),
							zap.Error(err),
						)
					}
				}
			}
		}
	}

	if err := s.repo.DeleteUser(r.Context(), userID); err != nil {
		s.logger.Error("delete user failed", zap.Error(err), zap.String("userId", userID))
		writeNotFound(w, "user not found")
		return
	}

	s.logAudit(r, "user.deleted", "admin", userID, `{"user_id":"`+userID+`"}`)
	writeOK(w, map[string]interface{}{"deleted": true, "userId": userID, "deletedAt": time.Now().UTC()})
}
