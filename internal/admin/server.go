// Package admin implements the Open-Nipper admin REST API server on :18790.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/datastore"
	"github.com/open-nipper/open-nipper/internal/models"
	"github.com/open-nipper/open-nipper/internal/queue"
)

// AgentHealthProvider supplies cached agent health data to the admin health endpoint.
type AgentHealthProvider interface {
	GetAllStatuses() []models.AgentHealthInfo
}

// AgentHeartbeatLister returns agent heartbeats (from POST /agents/health, in-memory only).
type AgentHeartbeatLister interface {
	GetAll() []models.AgentHeartbeatInfo
}

// AtJobMutator adds/removes at jobs at runtime (used by admin at job endpoints).
type AtJobMutator interface {
	AddAtJob(ctx context.Context, job config.AtJob) error
	RemoveAtJob(ctx context.Context, userID, id string) bool
}

// Server is the admin API HTTP server.
type Server struct {
	cfg            *config.Config
	repo           datastore.Repository
	mgmt           queue.ManagementClient
	agentHealth    AgentHealthProvider
	heartbeatStore AgentHeartbeatLister
	atMutator      AtJobMutator
	logger         *zap.Logger
	httpServer     *http.Server
}

// NewServer constructs an admin Server.
// mgmt may be nil when the RabbitMQ Management API is not configured.
func NewServer(cfg *config.Config, repo datastore.Repository, mgmt queue.ManagementClient, logger *zap.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		repo:   repo,
		mgmt:   mgmt,
		logger: logger,
	}

	r := mux.NewRouter()
	s.registerRoutes(r)

	addr := fmt.Sprintf("%s:%d", cfg.Gateway.Admin.Bind, cfg.Gateway.Admin.Port)
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
}

// SetAgentHealthProvider sets the provider for agent health data (queue-based).
// Must be called before Start if agent health data should appear in the health endpoint.
func (s *Server) SetAgentHealthProvider(p AgentHealthProvider) {
	s.agentHealth = p
}

// SetHeartbeatStore sets the in-memory store for agent heartbeats (POST /agents/health).
func (s *Server) SetHeartbeatStore(l AgentHeartbeatLister) {
	s.heartbeatStore = l
}

// SetAtMutator sets the at job mutator for scheduling at jobs from admin endpoints.
func (s *Server) SetAtMutator(m AtJobMutator) {
	s.atMutator = m
}

// Start starts the admin HTTP server in a background goroutine.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("admin server listen %s: %w", s.httpServer.Addr, err)
	}
	// Enforce localhost-only binding regardless of config.
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	if host != "127.0.0.1" && host != "::1" {
		_ = ln.Close()
		return fmt.Errorf("admin server must bind to 127.0.0.1, got %s", host)
	}
	s.logger.Info("admin server listening", zap.String("addr", s.httpServer.Addr))
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error("admin server error", zap.Error(err))
		}
	}()
	return nil
}

// Stop gracefully shuts down the admin server.
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying HTTP handler (useful for testing with httptest.NewServer).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// registerRoutes wires all admin API routes.
func (s *Server) registerRoutes(r *mux.Router) {
	// Middleware chain: logger → optional auth
	r.Use(s.loggingMiddleware)
	if s.cfg.Gateway.Admin.Auth.Enabled {
		r.Use(s.authMiddleware)
	}

	// Users
	r.HandleFunc("/admin/users", s.handleCreateUser).Methods(http.MethodPost)
	r.HandleFunc("/admin/users", s.handleListUsers).Methods(http.MethodGet)
	r.HandleFunc("/admin/users/{userId}", s.handleGetUser).Methods(http.MethodGet)
	r.HandleFunc("/admin/users/{userId}", s.handleUpdateUser).Methods(http.MethodPut)
	r.HandleFunc("/admin/users/{userId}", s.handleDeleteUser).Methods(http.MethodDelete)

	// Identities
	r.HandleFunc("/admin/users/{userId}/identities", s.handleAddIdentity).Methods(http.MethodPost)
	r.HandleFunc("/admin/users/{userId}/identities", s.handleListIdentities).Methods(http.MethodGet)
	r.HandleFunc("/admin/users/{userId}/identities/{id}", s.handleRemoveIdentity).Methods(http.MethodDelete)

	// Allowlist
	r.HandleFunc("/admin/allowlist", s.handleAddAllowlist).Methods(http.MethodPost)
	r.HandleFunc("/admin/allowlist", s.handleListAllowlist).Methods(http.MethodGet)
	r.HandleFunc("/admin/allowlist/{channelType}", s.handleListAllowlistByChannel).Methods(http.MethodGet)
	r.HandleFunc("/admin/allowlist/{userId}/{channelType}", s.handleUpdateAllowlist).Methods(http.MethodPut)
	r.HandleFunc("/admin/allowlist/{userId}/{channelType}", s.handleRemoveAllowlist).Methods(http.MethodDelete)

	// Policies
	r.HandleFunc("/admin/users/{userId}/policies", s.handleListPolicies).Methods(http.MethodGet)
	r.HandleFunc("/admin/users/{userId}/policies/{type}", s.handleSetPolicy).Methods(http.MethodPut)
	r.HandleFunc("/admin/users/{userId}/policies/{type}", s.handleDeletePolicy).Methods(http.MethodDelete)

	// Agents
	r.HandleFunc("/admin/agents", s.handleProvisionAgent).Methods(http.MethodPost)
	r.HandleFunc("/admin/agents", s.handleListAgents).Methods(http.MethodGet)
	r.HandleFunc("/admin/agents/{agentId}", s.handleGetAgent).Methods(http.MethodGet)
	r.HandleFunc("/admin/agents/{agentId}", s.handleDeleteAgent).Methods(http.MethodDelete)
	r.HandleFunc("/admin/agents/{agentId}/rotate", s.handleRotateAgentToken).Methods(http.MethodPost)
	r.HandleFunc("/admin/agents/{agentId}/revoke", s.handleRevokeAgent).Methods(http.MethodPost)

	// Cron jobs (view only; add/remove via agent API or config)
	r.HandleFunc("/admin/cron/jobs", s.handleListCronJobs).Methods(http.MethodGet)
	r.HandleFunc("/admin/users/{userId}/cron/jobs", s.handleListCronJobsByUser).Methods(http.MethodGet)

	// At jobs (one-shot scheduled prompts)
	r.HandleFunc("/admin/at/jobs", s.handleListAtJobs).Methods(http.MethodGet)
	r.HandleFunc("/admin/at/jobs", s.handleAddAtJob).Methods(http.MethodPost)
	r.HandleFunc("/admin/at/jobs/{userId}/{id}", s.handleRemoveAtJob).Methods(http.MethodDelete)
	r.HandleFunc("/admin/users/{userId}/at/jobs", s.handleListAtJobsByUser).Methods(http.MethodGet)

	// System
	r.HandleFunc("/admin/health", s.handleHealth).Methods(http.MethodGet)
	r.HandleFunc("/admin/audit", s.handleQueryAudit).Methods(http.MethodGet)
	r.HandleFunc("/admin/backup", s.handleBackup).Methods(http.MethodPost)
	r.HandleFunc("/admin/config", s.handleConfig).Methods(http.MethodGet)
}

// --------------------------------------------------------------------------
// Response helpers
// --------------------------------------------------------------------------

// apiResponse is the standard admin API JSON envelope.
type apiResponse struct {
	OK     bool        `json:"ok"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOK(w http.ResponseWriter, result interface{}) {
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Result: result})
}

func writeCreated(w http.ResponseWriter, result interface{}) {
	writeJSON(w, http.StatusCreated, apiResponse{OK: true, Result: result})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiResponse{OK: false, Error: msg})
}

func writeBadRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, msg)
}

func writeNotFound(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusNotFound, msg)
}

func writeInternalError(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusInternalServerError, msg)
}

// --------------------------------------------------------------------------
// Middleware
// --------------------------------------------------------------------------

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Debug("admin request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Duration("duration", time.Since(start)),
		)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	token := s.cfg.Gateway.Admin.Auth.Token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if strings.TrimPrefix(auth, "Bearer ") != token {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------------
// Audit helper
// --------------------------------------------------------------------------

func (s *Server) logAudit(r *http.Request, action, actor, targetUserID, details string) {
	entry := buildAuditEntry(r, action, actor, targetUserID, details)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.LogAdminAction(ctx, entry); err != nil {
		s.logger.Error("failed to write audit entry", zap.Error(err), zap.String("action", action))
	}
}
