package gateway

import (
	"sync"
	"time"

	"github.com/open-nipper/open-nipper/internal/models"
)

// AgentHeartbeatStore holds agent heartbeat reports in memory only (no DB).
// Updated on each POST /agents/health; read by admin health endpoint.
type AgentHeartbeatStore struct {
	mu   sync.RWMutex
	byID map[string]*heartbeatEntry
}

type heartbeatEntry struct {
	AgentID  string
	UserID   string
	Status   string
	LastSeen time.Time
}

// NewAgentHeartbeatStore creates an empty in-memory heartbeat store.
func NewAgentHeartbeatStore() *AgentHeartbeatStore {
	return &AgentHeartbeatStore{
		byID: make(map[string]*heartbeatEntry),
	}
}

// Record stores a heartbeat for the given agent (in-memory only).
func (s *AgentHeartbeatStore) Record(agentID, userID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = make(map[string]*heartbeatEntry)
	}
	s.byID[agentID] = &heartbeatEntry{
		AgentID:  agentID,
		UserID:   userID,
		Status:   status,
		LastSeen: time.Now().UTC(),
	}
}

// GetAll returns a snapshot of all heartbeats for the admin health endpoint.
func (s *AgentHeartbeatStore) GetAll() []models.AgentHeartbeatInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.AgentHeartbeatInfo, 0, len(s.byID))
	for _, e := range s.byID {
		out = append(out, models.AgentHeartbeatInfo{
			AgentID:  e.AgentID,
			UserID:   e.UserID,
			Status:   e.Status,
			LastSeen: e.LastSeen.Format(time.RFC3339),
		})
	}
	return out
}

const defaultStaleStatus = "failed"

// MarkStaleAsFailed sets status to "failed" for any agent whose LastSeen is older than olderThan.
// Call periodically from a cleanup routine.
func (s *AgentHeartbeatStore) MarkStaleAsFailed(olderThan time.Duration) {
	cutoff := time.Now().UTC().Add(-olderThan)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.byID {
		if e.LastSeen.Before(cutoff) && e.Status != defaultStaleStatus {
			e.Status = defaultStaleStatus
		}
	}
}
