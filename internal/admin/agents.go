package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)


// base62Chars is the character set for token encoding (alphanumeric only).
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// tokenPrefix is the identifying prefix for all nipper auth tokens.
const tokenPrefix = "npr_"

// generateToken creates a new `npr_` prefixed, 48-byte cryptographically random token.
// Returns the plaintext token, the SHA-256 hash hex string, and the first 8 chars (prefix storage).
func generateToken() (plaintext, hashHex, storedPrefix string, err error) {
	const tokenBytes = 48
	raw := make([]byte, tokenBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("generating random bytes: %w", err)
	}

	encoded := encodeBase62(raw)
	plaintext = tokenPrefix + encoded

	sum := sha256.Sum256([]byte(plaintext))
	hashHex = fmt.Sprintf("%x", sum[:])
	if len(plaintext) >= 8 {
		storedPrefix = plaintext[:8]
	} else {
		storedPrefix = plaintext
	}
	return plaintext, hashHex, storedPrefix, nil
}

// encodeBase62 encodes a byte slice as a base62 string.
func encodeBase62(data []byte) string {
	base := big.NewInt(62)
	n := new(big.Int).SetBytes(data)
	zero := big.NewInt(0)

	var result []byte
	mod := new(big.Int)
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		result = append(result, base62Chars[mod.Int64()])
	}

	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

func (s *Server) handleProvisionAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
		Label  string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}
	if req.UserID == "" {
		writeBadRequest(w, "user_id is required")
		return
	}
	if req.Label == "" {
		writeBadRequest(w, "label is required")
		return
	}

	// Verify user exists.
	if _, err := s.repo.GetUser(r.Context(), req.UserID); err != nil {
		writeNotFound(w, "user not found")
		return
	}

	plaintext, hashHex, storedPrefix, err := generateToken()
	if err != nil {
		s.logger.Error("token generation failed", zap.Error(err))
		writeInternalError(w, "failed to generate agent token")
		return
	}

	agent, err := s.repo.ProvisionAgent(r.Context(), models.ProvisionAgentRequest{
		UserID:      req.UserID,
		Label:       req.Label,
		TokenHash:   hashHex,
		TokenPrefix: storedPrefix,
	})
	if err != nil {
		s.logger.Error("provision agent failed", zap.Error(err), zap.String("userId", req.UserID))
		writeInternalError(w, "failed to provision agent")
		return
	}

	s.logAudit(r, "agent.provisioned", "admin", req.UserID,
		fmt.Sprintf(`{"agent_id":%q,"label":%q}`, agent.ID, agent.Label))

	writeCreated(w, map[string]interface{}{
		"agent":      agent,
		"authToken":  plaintext,
		"note":       "Save this token — it will not be shown again.",
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	agents, err := s.repo.ListAgents(r.Context(), userID)
	if err != nil {
		s.logger.Error("list agents failed", zap.Error(err))
		writeInternalError(w, "failed to list agents")
		return
	}
	if agents == nil {
		agents = []*models.Agent{}
	}
	writeOK(w, agents)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	agentID := mux.Vars(r)["agentId"]
	agent, err := s.repo.GetAgent(r.Context(), agentID)
	if err != nil {
		s.logger.Error("get agent failed", zap.Error(err), zap.String("agentId", agentID))
		writeNotFound(w, "agent not found")
		return
	}
	writeOK(w, agent)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentID := mux.Vars(r)["agentId"]

	agent, err := s.repo.GetAgent(r.Context(), agentID)
	if err != nil {
		writeNotFound(w, "agent not found")
		return
	}

	// Delete RMQ user if management client is available.
	if s.mgmt != nil && agent.RMQUsername != "" {
		if err := s.mgmt.DeleteUser(r.Context(), agent.RMQUsername); err != nil {
			s.logger.Warn("failed to delete RMQ user",
				zap.String("agentId", agentID),
				zap.String("rmqUsername", agent.RMQUsername),
				zap.Error(err),
			)
		}
	}

	if err := s.repo.DeleteAgent(r.Context(), agentID); err != nil {
		s.logger.Error("delete agent failed", zap.Error(err), zap.String("agentId", agentID))
		writeInternalError(w, "failed to delete agent")
		return
	}

	s.logAudit(r, "agent.deleted", "admin", agent.UserID,
		fmt.Sprintf(`{"agent_id":%q}`, agentID))
	writeOK(w, map[string]interface{}{"deleted": true, "agentId": agentID})
}

func (s *Server) handleRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	agentID := mux.Vars(r)["agentId"]

	agent, err := s.repo.GetAgent(r.Context(), agentID)
	if err != nil {
		writeNotFound(w, "agent not found")
		return
	}

	plaintext, hashHex, storedPrefix, err := generateToken()
	if err != nil {
		s.logger.Error("token rotation failed", zap.Error(err))
		writeInternalError(w, "failed to generate new token")
		return
	}

	if err := s.repo.RotateAgentToken(r.Context(), agentID, hashHex, storedPrefix); err != nil {
		s.logger.Error("rotate token failed", zap.Error(err), zap.String("agentId", agentID))
		writeInternalError(w, "failed to rotate token")
		return
	}

	s.logAudit(r, "agent.token_rotated", "admin", agent.UserID,
		fmt.Sprintf(`{"agent_id":%q}`, agentID))
	writeOK(w, map[string]interface{}{
		"agentId":   agentID,
		"authToken": plaintext,
		"note":      "Save this token — it will not be shown again.",
	})
}

func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	agentID := mux.Vars(r)["agentId"]

	agent, err := s.repo.GetAgent(r.Context(), agentID)
	if err != nil {
		writeNotFound(w, "agent not found")
		return
	}

	if err := s.repo.RevokeAgent(r.Context(), agentID); err != nil {
		s.logger.Error("revoke agent failed", zap.Error(err), zap.String("agentId", agentID))
		writeInternalError(w, "failed to revoke agent")
		return
	}

	s.logAudit(r, "agent.revoked", "admin", agent.UserID,
		fmt.Sprintf(`{"agent_id":%q}`, agentID))
	writeOK(w, map[string]interface{}{"revoked": true, "agentId": agentID})
}
