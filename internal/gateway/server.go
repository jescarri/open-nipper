package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/telemetry"
)

// Server is the main HTTP server on :18789 that handles channel webhooks,
// agent registration, WebSocket connections, and a health endpoint.
type Server struct {
	httpServer         *http.Server
	router             *mux.Router
	logger             *zap.Logger
	cfg                *config.Config
	msgRouter          *Router
	dispatcher         *Dispatcher
	adapters           map[models.ChannelType]channels.ChannelAdapter
	registerHandler     *RegisterHandler
	agentHealthHandler *AgentHealthHandler
	agentCronHandler   *AgentCronHandler
	agentAtHandler     *AgentAtHandler
}

// ServerDeps bundles the dependencies for the main HTTP server.
type ServerDeps struct {
	Logger              *zap.Logger
	Config              *config.Config
	MsgRouter            *Router
	Dispatcher          *Dispatcher
	Adapters            map[models.ChannelType]channels.ChannelAdapter
	RegisterHandler     *RegisterHandler
	AgentHealthHandler  *AgentHealthHandler
	AgentCronHandler     *AgentCronHandler // optional; when set, GET/POST/DELETE /agents/me/cron/jobs are registered
	AgentAtHandler       *AgentAtHandler   // optional; when set, GET/POST/DELETE /agents/me/at/jobs are registered
	Metrics             *telemetry.Metrics
	// MetricsHandler is the Prometheus /metrics HTTP handler; if set, GET /metrics is registered and not logged or traced.
	MetricsHandler http.Handler
}

// NewServer creates the main HTTP server without starting it.
func NewServer(deps ServerDeps) *Server {
	r := mux.NewRouter()

	s := &Server{
		router:             r,
		logger:             deps.Logger,
		cfg:                deps.Config,
		msgRouter:          deps.MsgRouter,
		dispatcher:         deps.Dispatcher,
		adapters:           deps.Adapters,
		registerHandler:   deps.RegisterHandler,
		agentHealthHandler: deps.AgentHealthHandler,
		agentCronHandler:  deps.AgentCronHandler,
		agentAtHandler:    deps.AgentAtHandler,
	}
	if deps.MetricsHandler != nil {
		r.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			deps.MetricsHandler.ServeHTTP(w, req)
		}).Methods("GET")
	}

	s.registerRoutes()

	readTimeout := 30 * time.Second
	writeTimeout := 30 * time.Second
	if deps.Config != nil {
		if deps.Config.Gateway.ReadTimeoutSeconds > 0 {
			readTimeout = time.Duration(deps.Config.Gateway.ReadTimeoutSeconds) * time.Second
		}
		if deps.Config.Gateway.WriteTimeoutSeconds > 0 {
			writeTimeout = time.Duration(deps.Config.Gateway.WriteTimeoutSeconds) * time.Second
		}
	}

	bind := "127.0.0.1"
	port := 18789
	if deps.Config != nil {
		if deps.Config.Gateway.Bind != "" {
			bind = deps.Config.Gateway.Bind
		}
		if deps.Config.Gateway.Port > 0 {
			port = deps.Config.Gateway.Port
		}
	}

	var handler http.Handler = s.loggingMiddleware(r)
	if deps.Metrics != nil {
		handler = telemetry.HTTPMiddleware(deps.Metrics)(handler)
	}

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", bind, port),
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	return s
}

func (s *Server) registerRoutes() {
	// Channel webhooks
	if _, ok := s.adapters[models.ChannelTypeWhatsApp]; ok {
		s.router.HandleFunc("/webhook/whatsapp", s.handleWebhookWhatsApp).Methods("POST")
	}
	if _, ok := s.adapters[models.ChannelTypeSlack]; ok {
		s.router.HandleFunc("/webhook/slack", s.handleWebhookSlack).Methods("POST")
	}

	// Agent registration
	if s.registerHandler != nil {
		s.router.HandleFunc("/agents/register", s.registerHandler.Handle).Methods("POST")
	}

	// Agent health (POST status for metrics)
	if s.agentHealthHandler != nil {
		s.router.HandleFunc("/agents/health", s.agentHealthHandler.Handle).Methods("POST")
	}

	// Agent cron jobs (Bearer auth, scoped to agent's user)
	if s.agentCronHandler != nil {
		s.router.HandleFunc("/agents/me/cron/jobs", s.agentCronHandler.HandleList).Methods("GET")
		s.router.HandleFunc("/agents/me/cron/jobs", s.agentCronHandler.HandleAdd).Methods("POST")
		s.router.HandleFunc("/agents/me/cron/jobs/{id}", s.agentCronHandler.HandleRemove).Methods("DELETE")
	}

	// Agent at jobs (one-shot scheduled prompts, Bearer auth)
	if s.agentAtHandler != nil {
		s.router.HandleFunc("/agents/me/at/jobs", s.agentAtHandler.HandleList).Methods("GET")
		s.router.HandleFunc("/agents/me/at/jobs", s.agentAtHandler.HandleAdd).Methods("POST")
		s.router.HandleFunc("/agents/me/at/jobs/{id}", s.agentAtHandler.HandleRemove).Methods("DELETE")
	}

	// Health
	s.router.HandleFunc("/health", s.handleHealth).Methods("GET")
}

// Handler returns the HTTP handler (used in tests).
func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(s.router)
}

// Start begins accepting connections. It blocks until the server is stopped.
func (s *Server) Start() error {
	s.logger.Info("main HTTP server starting",
		zap.String("addr", s.httpServer.Addr),
	)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// StartOnListener starts the server on the provided listener (useful for tests).
func (s *Server) StartOnListener(ln net.Listener) error {
	s.logger.Info("main HTTP server starting on listener",
		zap.String("addr", ln.Addr().String()),
	)
	if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully shuts down with a 30-second deadline.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.logger.Info("main HTTP server shutting down")
	return s.httpServer.Shutdown(ctx)
}

// handleHealth returns a simple health check response.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": "healthy",
	})
}

// loggingMiddleware logs each request at debug level. Healthcheck and metrics endpoints are not logged.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if telemetry.PathExcludedFromTelemetry(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.logger.Debug("http request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", rw.statusCode),
			zap.Duration("duration", time.Since(start)),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
