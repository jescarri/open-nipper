package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

// --------------------------------------------------------------------------
// Health
// --------------------------------------------------------------------------

type healthComponent struct {
	Status string `json:"status"`
}

type healthResult struct {
	Status     string                         `json:"status"`
	Components map[string]healthComponent     `json:"components"`
	Agents     []models.AgentHealthInfo       `json:"agents,omitempty"`
	Heartbeats []models.AgentHeartbeatInfo   `json:"heartbeats,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	components := map[string]healthComponent{}
	overallOK := true

	// Datastore
	if err := s.repo.Ping(r.Context()); err != nil {
		components["datastore"] = healthComponent{Status: "error"}
		overallOK = false
		s.logger.Error("datastore health check failed", zap.Error(err))
	} else {
		components["datastore"] = healthComponent{Status: "ok"}
	}

	// RabbitMQ Management
	if s.mgmt != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, err := s.mgmt.ListQueues(ctx, s.cfg.Queue.RabbitMQ.VHost)
		if err != nil {
			components["rabbitmq_management"] = healthComponent{Status: "degraded"}
		} else {
			components["rabbitmq_management"] = healthComponent{Status: "ok"}
		}
	} else {
		components["rabbitmq_management"] = healthComponent{Status: "disabled"}
	}

	// Channels
	if s.cfg.Channels.WhatsApp.Enabled {
		components["whatsapp"] = healthComponent{Status: "ok"}
	} else {
		components["whatsapp"] = healthComponent{Status: "disabled"}
	}
	if s.cfg.Channels.Slack.Enabled {
		components["slack"] = healthComponent{Status: "ok"}
	} else {
		components["slack"] = healthComponent{Status: "disabled"}
	}
	if s.cfg.Channels.MQTT.Enabled {
		components["mqtt"] = healthComponent{Status: "ok"}
	} else {
		components["mqtt"] = healthComponent{Status: "disabled"}
	}

	status := "healthy"
	if !overallOK {
		status = "degraded"
	}

	result := healthResult{
		Status:     status,
		Components: components,
	}

	if s.agentHealth != nil {
		result.Agents = s.agentHealth.GetAllStatuses()
	}
	if s.heartbeatStore != nil {
		result.Heartbeats = s.heartbeatStore.GetAll()
	}

	writeOK(w, result)
}

// --------------------------------------------------------------------------
// Audit log
// --------------------------------------------------------------------------

func (s *Server) handleQueryAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filters := models.AuditQueryFilters{
		Action:       q.Get("action"),
		TargetUserID: q.Get("user_id"),
	}

	if sinceStr := q.Get("since"); sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			writeBadRequest(w, "invalid since: must be RFC3339")
			return
		}
		filters.Since = &t
	}
	if untilStr := q.Get("until"); untilStr != "" {
		t, err := time.Parse(time.RFC3339, untilStr)
		if err != nil {
			writeBadRequest(w, "invalid until: must be RFC3339")
			return
		}
		filters.Until = &t
	}

	entries, err := s.repo.QueryAuditLog(r.Context(), filters)
	if err != nil {
		s.logger.Error("query audit log failed", zap.Error(err))
		writeInternalError(w, "failed to query audit log")
		return
	}
	writeOK(w, entries)
}

// --------------------------------------------------------------------------
// Backup
// --------------------------------------------------------------------------

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	destPath := s.cfg.Datastore.Backup.Path
	if destPath == "" {
		writeBadRequest(w, "backup path not configured")
		return
	}

	if err := s.repo.Backup(r.Context(), destPath); err != nil {
		s.logger.Error("backup failed", zap.Error(err))
		writeInternalError(w, "backup failed")
		return
	}

	s.logAudit(r, "backup.created", "admin", "", fmt.Sprintf(`{"path":%q}`, destPath))
	writeOK(w, map[string]interface{}{"path": destPath, "timestamp": time.Now().UTC()})
}

// --------------------------------------------------------------------------
// Config view (secrets redacted)
// --------------------------------------------------------------------------

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Marshal and unmarshal to get a generic map, then redact secrets.
	raw, err := json.Marshal(s.cfg)
	if err != nil {
		writeInternalError(w, "failed to serialize config")
		return
	}

	var cfgMap map[string]interface{}
	if err := json.Unmarshal(raw, &cfgMap); err != nil {
		writeInternalError(w, "failed to process config")
		return
	}

	redactConfig(cfgMap)
	writeOK(w, cfgMap)
}

// redactConfig recursively replaces known secret field names with "[REDACTED]".
var secretFields = map[string]bool{
	"token": true, "password": true, "secret": true,
	"api_key": true, "wuzapi_token": true, "wuzapi_hmac_key": true,
	"app_token": true, "bot_token": true, "signing_secret": true,
}

func redactConfig(m map[string]interface{}) {
	for k, v := range m {
		if secretFields[k] {
			if s, ok := v.(string); ok && s != "" {
				m[k] = "[REDACTED]"
			}
			continue
		}
		switch val := v.(type) {
		case map[string]interface{}:
			redactConfig(val)
		case []interface{}:
			for _, item := range val {
				if sub, ok := item.(map[string]interface{}); ok {
					redactConfig(sub)
				}
			}
		}
	}
}

// --------------------------------------------------------------------------
// Audit entry constructor (used across handlers)
// --------------------------------------------------------------------------

func buildAuditEntry(r *http.Request, action, actor, targetUserID, details string) models.AdminAuditEntry {
	ip := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = xff
	}
	return models.AdminAuditEntry{
		Timestamp:    time.Now().UTC(),
		Action:       action,
		Actor:        actor,
		TargetUserID: targetUserID,
		Details:      details,
		IPAddress:    ip,
	}
}
