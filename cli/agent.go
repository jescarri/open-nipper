package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	niagent "github.com/jescarri/open-nipper/internal/agent"
	"github.com/jescarri/open-nipper/internal/agent/enrich"
	"github.com/jescarri/open-nipper/internal/agent/llm"
	agentmcp "github.com/jescarri/open-nipper/internal/agent/mcp"
	"github.com/jescarri/open-nipper/internal/agent/registration"
	"github.com/jescarri/open-nipper/internal/agent/sandbox"
	"github.com/jescarri/open-nipper/internal/agent/skills"
	"github.com/jescarri/open-nipper/internal/agent/tools"
	"github.com/jescarri/open-nipper/internal/config"
	nlogger "github.com/jescarri/open-nipper/internal/logger"
	"github.com/jescarri/open-nipper/internal/telemetry"
	"github.com/jescarri/open-nipper/pkg/session"
	"go.opentelemetry.io/otel"
)

var (
	agentDumpConfig     bool
	tokenEncryptionKey  string
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run the Open-Nipper agent",
	Long: `Start the Open-Nipper agent. The agent registers with the Gateway,
receives messages via RabbitMQ, and responds using an LLM.

Required environment variables:
  NIPPER_GATEWAY_URL  Gateway base URL (e.g. http://localhost:18789)
  NIPPER_AUTH_TOKEN   Agent auth token (npr_...)

Optional:
  OPENAI_API_KEY      API key for OpenAI-compatible inference

Config file:
  Use the root --config / -c flag to pass an agent YAML config file.
  Example: nipper agent -c agent.yaml`,
	RunE: runAgent,
}

func init() {
	agentCmd.Flags().BoolVar(&agentDumpConfig, "dump-config", false, "Print default agent configuration to stdout and exit")
	agentCmd.Flags().StringVar(&tokenEncryptionKey, "token-encryption-key", "", "Encryption key for OIDC token storage (or set OPEN_NIPPER_TOKEN_ENCRYPTION_KEY)")
}

func runAgent(cmd *cobra.Command, args []string) error {
	if agentDumpConfig {
		out, err := config.DumpAgentConfig()
		if err != nil {
			return fmt.Errorf("dumping config: %w", err)
		}
		fmt.Print(string(out))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if logLevel != "" {
		if err := os.Setenv("NIPPER_LOG_LEVEL", logLevel); err != nil {
			return fmt.Errorf("setting log level override: %w", err)
		}
	}
	if logFormat != "" {
		if err := os.Setenv("NIPPER_LOG_FORMAT", logFormat); err != nil {
			return fmt.Errorf("setting log format override: %w", err)
		}
	}

	// 1. Load agent config from YAML (if provided).
	// cfgFile is set by the root persistent --config / -c flag.
	agentFileCfg, err := config.LoadAgentConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("loading agent config: %w", err)
	}
	cfg := &agentFileCfg.Agent

	// Expand API key from env if it contains ${...}.
	if cfg.Inference.APIKey == "" {
		cfg.Inference.APIKey = os.Getenv("OPENAI_API_KEY")
	}

	// 2. Initialize logger.
	logger, err := nlogger.NewWithService("open-nipper-agent")
	if err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	logger.Info("open-nipper agent starting")

	// 3. Initialize telemetry (noop if disabled or endpoints not configured).
	telemetryCfg := agentFileCfg.Telemetry
	shutdownTrace, traceErr := telemetry.InitTracing(ctx, telemetryCfg.Tracing, "dev", logger)
	if traceErr != nil {
		logger.Debug("tracing init failed; using noop", zap.Error(traceErr))
		shutdownTrace = func(context.Context) error { return nil }
	} else if telemetryCfg.Tracing.Enabled {
		logger.Info("tracing enabled", zap.String("endpoint", telemetryCfg.Tracing.Endpoint), zap.String("service_name", telemetryCfg.Tracing.ServiceName))
		// Emit a startup span so the pipeline is exercised immediately and export failures surface.
		tracer := otel.Tracer("open-nipper-agent")
		_, span := tracer.Start(context.Background(), "agent.started")
		span.End()
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = shutdownTrace(shutdownCtx)
	}()
	metrics, shutdownMetrics, _, metricsErr := telemetry.InitMetrics(ctx, telemetryCfg.Metrics, logger, telemetryCfg.Metrics.PrometheusPort)
	if metricsErr != nil {
		logger.Debug("metrics init failed; using noop", zap.Error(metricsErr))
		shutdownMetrics = func(context.Context) error { return nil }
	}
	defer func() { _ = shutdownMetrics(context.Background()) }()

	// 4. Read required env vars.
	gatewayURL := os.Getenv("NIPPER_GATEWAY_URL")
	authToken := os.Getenv("NIPPER_AUTH_TOKEN")
	if gatewayURL == "" {
		return fmt.Errorf("NIPPER_GATEWAY_URL environment variable is required")
	}
	if authToken == "" {
		return fmt.Errorf("NIPPER_AUTH_TOKEN environment variable is required")
	}

	// 5. Register with Gateway (once for UserID; re-register on reconnect in loop).
	regClient := registration.NewClient(gatewayURL, authToken, logger)
	logger.Info("registering with gateway", zap.String("url", gatewayURL))
	reg, err := regClient.Register(ctx)
	if err != nil {
		return fmt.Errorf("gateway registration failed: %w", err)
	}
	logger.Info("registered with gateway",
		zap.String("agentId", reg.AgentID),
		zap.String("userId", reg.UserID),
		zap.String("queue", reg.RabbitMQ.Queues.Agent),
	)

	// 5b. Start heartbeat to gateway (configurable interval; 0 = disabled).
	if cfg.HeartbeatIntervalSeconds > 0 {
		interval := time.Duration(cfg.HeartbeatIntervalSeconds) * time.Second
		regClient.StartHeartbeat(ctx, interval, "healthy")
		logger.Info("heartbeat started", zap.Duration("interval", interval))
	}

	// 6. Probe the inference server for model capabilities (best-effort).
	// Done BEFORE creating the ChatModel so we can auto-cap max_tokens to
	// fit within the context window — this makes model switching transparent
	// without manual config changes.
	var contextWindowMax int
	var probedCap *llm.ModelCapabilities
	if cap, probeErr := llm.ProbeModelCapabilities(ctx, cfg.Inference); probeErr != nil {
		logger.Debug("model capability probe skipped", zap.Error(probeErr))
	} else {
		probedCap = cap
		fields := []zap.Field{
			zap.String("modelId", probedCap.ID),
			zap.String("source", probedCap.Source),
		}
		if probedCap.MaxContextLength > 0 {
			fields = append(fields, zap.Int("maxContextLength", probedCap.MaxContextLength))
			contextWindowMax = probedCap.MaxContextLength
		}
		if probedCap.State != "" {
			fields = append(fields, zap.String("state", probedCap.State))
		}
		if probedCap.Architecture != "" {
			fields = append(fields, zap.String("architecture", probedCap.Architecture))
		}
		if probedCap.Quantization != "" {
			fields = append(fields, zap.String("quantization", probedCap.Quantization))
		}
		logger.Info("inference server model capabilities", fields...)

		if probedCap.State != "" && probedCap.State != "loaded" {
			logger.Warn("model is not loaded on the inference server",
				zap.String("state", probedCap.State),
				zap.String("model", probedCap.ID),
			)
		}
	}

	// Resolve context window: model metadata first, then config fallback.
	configContextWindow := cfg.Inference.ContextWindowSize
	if contextWindowMax == 0 && configContextWindow > 0 {
		contextWindowMax = configContextWindow
		logger.Info("using config context window (model probe unavailable)",
			zap.Int("contextWindowSize", configContextWindow),
		)
	} else if probedCap != nil && probedCap.MaxContextLength > 0 && configContextWindow > 0 && probedCap.MaxContextLength >= configContextWindow {
		logger.Info("using model metadata for context window (model >= config)",
			zap.Int("modelMaxContextLength", probedCap.MaxContextLength),
			zap.Int("configContextWindowSize", configContextWindow),
		)
	}

	// Auto-cap max_tokens when explicitly set (>0) so prompt + output fits
	// within the context window. Reserve 4096 tokens minimum for input
	// (system prompt + tools + history). If that leaves no room, fall back
	// to auto (0) and let the server size it per-request.
	const minPromptReserve = 4096
	if contextWindowMax > 0 && cfg.Inference.MaxTokens > 0 {
		maxSafeOutput := contextWindowMax - minPromptReserve
		if maxSafeOutput <= 0 {
			logger.Warn("context window too small for explicit max_tokens; switching to auto (server-managed)",
				zap.Int("configuredMaxTokens", cfg.Inference.MaxTokens),
				zap.Int("contextWindow", contextWindowMax),
			)
			cfg.Inference.MaxTokens = 0
		} else if cfg.Inference.MaxTokens > maxSafeOutput {
			logger.Warn("auto-capping max_tokens to fit within context window",
				zap.Int("configuredMaxTokens", cfg.Inference.MaxTokens),
				zap.Int("contextWindow", contextWindowMax),
				zap.Int("cappedMaxTokens", maxSafeOutput),
				zap.Int("promptReserve", minPromptReserve),
			)
			cfg.Inference.MaxTokens = maxSafeOutput
		}
	}

	// 6b. Create ChatModel.
	chatModel, err := llm.NewChatModel(ctx, cfg.Inference)
	if err != nil {
		return fmt.Errorf("creating chat model: %w", err)
	}
	// Wrap with streaming-based Generate when configured (workaround for
	// vLLM non-streaming endpoint not returning tool_calls for GPT-OSS models).
	if cfg.Inference.StreamGenerate {
		if tcm, ok := chatModel.(llm.ToolCallingChatModel); ok {
			chatModel = llm.NewStreamingGenerateModel(tcm)
			logger.Info("stream_generate enabled: Generate calls will use streaming internally")
		} else {
			logger.Warn("stream_generate configured but model does not implement ToolCallingChatModel; ignored")
		}
	}
	logger.Info("chat model created",
		zap.String("provider", cfg.Inference.Provider),
		zap.String("model", cfg.Inference.Model),
		zap.Int("maxTokens", cfg.Inference.MaxTokens),
	)

	// 7. Create sandbox (if enabled). Mount skills dir when skills are enabled so /skills is available.
	var sandboxMgr *sandbox.Manager
	if cfg.Sandbox.Enabled && cfg.Tools.Bash {
		if cfg.Skills.Enabled && cfg.Skills.Path != "" {
			cfg.Sandbox.SkillsPath = cfg.Skills.Path
		}
		sandboxMgr = sandbox.NewManager(cfg.Sandbox, logger)
		if err := sandboxMgr.Create(ctx); err != nil {
			return fmt.Errorf("creating sandbox container: %w", err)
		}
		defer sandboxMgr.Cleanup(context.Background())
		logger.Info("sandbox container ready",
			zap.String("image", cfg.Sandbox.Image),
			zap.Bool("network", cfg.Sandbox.NetworkEnabled),
		)
	}

	// 8. Load skills (if enabled). Must run before building tools so bash can use skill executor.
	var skillsLoader *skills.Loader
	var skillExecutor *skills.Executor
	if cfg.Skills.Enabled && cfg.Skills.Path != "" {
		skillsLoader, err = skills.NewLoader(cfg.Skills.Path, logger)
		if err != nil {
			return fmt.Errorf("loading skills: %w", err)
		}
		skillCount := int64(len(skillsLoader.Skills()))
		telemetry.RecordSkillsLoaded(ctx, metrics, reg.AgentID, skillCount)
		logger.Info("skills loaded",
			zap.Int("count", len(skillsLoader.Skills())),
			zap.String("path", cfg.Skills.Path),
		)
		// Always create executor so skill_exec tool is registered and can
		// return clear errors. Sandbox may be nil — script skills will be
		// rejected at execution time with a descriptive message.
		secretRegistry := skills.NewProviderRegistry()
		secretRegistry.Register(skills.NewEnvVarProvider(logger))
		skillExecutor = skills.NewExecutor(sandboxMgr, secretRegistry, logger, niagent.NewSkillMetricsRecorder(metrics))
		skillExecutor.UserID = reg.UserID
		// Tell the loader whether sandbox is available so script-type skills
		// are excluded from prompts when they cannot execute.
		skillsLoader.SetSandboxAvailable(sandboxMgr != nil)
	}

	// 9. Build tools.
	var toolsPolicy *registration.ToolsPolicy
	if reg.Policies.Tools != nil {
		toolsPolicy = reg.Policies.Tools
	}
	toolOpts := &tools.BuildToolsOptions{
		SandboxMgr:     sandboxMgr,
		Logger:         logger,
		SkillsLoader:   skillsLoader,
		SkillExecutor:  skillExecutor,
	}
	agentTools, err := tools.BuildTools(ctx, cfg, toolsPolicy, toolOpts)
	if err != nil {
		return fmt.Errorf("building tools: %w", err)
	}
	logger.Info("tools built", zap.Int("count", len(agentTools)))
	if len(agentTools) == 0 {
		logger.Warn("no tools are enabled; the agent will not be able to fetch URLs or run commands — set tools.web_fetch: true in your agent config to enable web access")
	}

	// 10. Resolve base path (needed for MCP token storage and session store).
	basePath := cfg.BasePath
	if basePath == "" {
		basePath = defaultBasePath()
	}

	// 11. Load MCP tools (if configured).
	// The loader manages its own keepalive and reconnection for SSE transports.
	// Tools are resolved dynamically via the loader at each message, so the
	// runtime always sees a fresh set even after reconnection.
	var mcpLoader *agentmcp.Loader
	if len(cfg.MCP) > 0 {
		// Resolve token encryption key: flag > env var.
		encKey := tokenEncryptionKey
		if encKey == "" {
			encKey = os.Getenv("OPEN_NIPPER_TOKEN_ENCRYPTION_KEY")
		}
		// Validate: if any MCP server has OIDC auth, encryption key is required.
		for _, mcpCfg := range cfg.MCP {
			if mcpCfg.Auth != nil && strings.ToLower(mcpCfg.Auth.Type) == "oidc" && encKey == "" {
				return fmt.Errorf("OIDC auth is configured for MCP server %q but no token encryption key provided; set OPEN_NIPPER_TOKEN_ENCRYPTION_KEY or --token-encryption-key", mcpCfg.Name)
			}
		}

		// Build a notifier that sends device auth URLs to the user via the
		// gateway's /agents/me/notify endpoint (same notification infra as cron).
		mcpNotifier := agentmcp.DeviceAuthNotifier(func(nctx context.Context, serverName, verificationURI, userCode string, expiresIn int) {
			msg := fmt.Sprintf("🔐 *OIDC Authentication Required* for MCP server *%s*\n\nVisit: %s\nCode: *%s*\nExpires in: %d seconds",
				serverName, verificationURI, userCode, expiresIn)
			if err := notifyUser(nctx, gatewayURL, authToken, msg); err != nil {
				logger.Warn("failed to send OIDC device auth notification", zap.Error(err))
			}
		})

		mcpLoader, err = agentmcp.NewLoader(ctx, cfg.MCP, logger, basePath, encKey, mcpNotifier)
		if err != nil {
			return fmt.Errorf("loading MCP tools: %w", err)
		}
		defer mcpLoader.Close()

		logger.Info("mcp tools loaded",
			zap.Int("mcpToolCount", len(mcpLoader.Tools())),
			zap.Int("baseToolCount", len(agentTools)),
			zap.Strings("mcpToolNames", mcpLoader.ToolNames()),
		)
	}
	sessionStore := session.NewStore(basePath, logger)
	logger.Info("session store ready", zap.String("basePath", basePath))

	// 12. Create usage tracker.
	usageTracker := niagent.NewUsageTracker(cfg.Inference.Model, contextWindowMax)

	// 13–14. Reconnect loop: register → connect → run; on connection loss, re-register and retry.
	runtimeOpts := []niagent.RuntimeOption{
		niagent.WithUsageTracker(usageTracker),
		niagent.WithContextWindow(contextWindowMax),
	}
	if mcpLoader != nil {
		runtimeOpts = append(runtimeOpts, niagent.WithMCPLoader(mcpLoader))
	}
	if skillsLoader != nil {
		runtimeOpts = append(runtimeOpts, niagent.WithSkillsLoader(skillsLoader))
	}
	if cfg.MediaEnrichment.Speech.Enabled && cfg.MediaEnrichment.Speech.Endpoint != "" {
		timeout := time.Duration(cfg.MediaEnrichment.Speech.TimeoutSeconds) * time.Second
		speechEnricher := enrich.NewSpeechEnricher(cfg.MediaEnrichment.Speech.Endpoint, timeout, cfg.S3)
		pipeline := enrich.NewPipeline(speechEnricher)
		runtimeOpts = append(runtimeOpts, niagent.WithEnrichPipeline(pipeline))
		logger.Info("media enrichment enabled",
			zap.String("speechEndpoint", cfg.MediaEnrichment.Speech.Endpoint),
		)
	}

	const reconnectBackoff = 5 * time.Second
	for {
		reg, err := regClient.Register(ctx)
		if err != nil {
			return fmt.Errorf("gateway registration failed: %w", err)
		}
		logger.Info("registered with gateway (reconnect)",
			zap.String("agentId", reg.AgentID),
			zap.String("userId", reg.UserID),
			zap.String("queue", reg.RabbitMQ.Queues.Agent),
		)

		amqpURL, err := buildAMQPURL(reg.RabbitMQ)
		if err != nil {
			return fmt.Errorf("building AMQP URL: %w", err)
		}
		conn, err := amqp.DialConfig(amqpURL, amqp.Config{Heartbeat: 60})
		if err != nil {
			return fmt.Errorf("connecting to RabbitMQ: %w", err)
		}
		logger.Info("connected to RabbitMQ",
			zap.String("vhost", reg.RabbitMQ.VHost),
			zap.String("username", reg.RabbitMQ.Username),
		)

		runtime := niagent.NewRuntime(cfg, reg, chatModel, agentTools, sessionStore, logger, runtimeOpts...)
		logger.Info("agent runtime starting")
		runErr := runtime.Run(ctx, conn)
		_ = conn.Close()

		if runErr != nil && runErr != context.Canceled {
			logger.Warn("agent run ended; reconnecting after connection loss", zap.Error(runErr))
		}
		if ctx.Err() != nil {
			logger.Info("agent shutdown complete")
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			logger.Info("agent shutdown complete")
			return ctx.Err()
		case <-time.After(reconnectBackoff):
			// re-register and reconnect
		}
	}
}

// buildAMQPURL constructs an AMQP URL from the registration RMQConfig.
func buildAMQPURL(rmq registration.RMQConfig) (string, error) {
	rawURL := rmq.URL
	if rawURL == "" {
		return "", fmt.Errorf("RabbitMQ URL is empty in registration response")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing RabbitMQ URL %q: %w", rawURL, err)
	}
	if rmq.Username != "" {
		u.User = url.UserPassword(rmq.Username, rmq.Password)
	}
	if rmq.VHost != "" {
		u.Path = "/" + rmq.VHost
		u.RawPath = "/" + url.PathEscape(rmq.VHost)
	}
	return u.String(), nil
}

// notifyUser sends a message to the user through the gateway's notification endpoint.
// Uses the same auth + channel resolution infrastructure as cron/at jobs.
func notifyUser(ctx context.Context, gatewayURL, authToken, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshalling notify request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", gatewayURL+"/agents/me/notify", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating notify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %s", resp.Status)
	}
	return nil
}

// defaultBasePath returns $HOME/.open-nipper for session/memory store when config base_path is empty.
func defaultBasePath() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".open-nipper")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".open-nipper"
	}
	return filepath.Join(home, ".open-nipper")
}
