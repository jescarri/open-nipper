package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/admin"
	"github.com/jescarri/open-nipper/internal/allowlist"
	"github.com/jescarri/open-nipper/internal/channels"
	cronadapter "github.com/jescarri/open-nipper/internal/channels/cron"
	mqttadapter "github.com/jescarri/open-nipper/internal/channels/mqtt"
	rabbitmqadapter "github.com/jescarri/open-nipper/internal/channels/rabbitmq"
	slackadapter "github.com/jescarri/open-nipper/internal/channels/slack"
	waadapter "github.com/jescarri/open-nipper/internal/channels/whatsapp"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/datastore/sqlite"
	"github.com/jescarri/open-nipper/internal/gateway"
	"github.com/jescarri/open-nipper/internal/lifecycle"
	"github.com/jescarri/open-nipper/internal/logger"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/queue"
	"github.com/jescarri/open-nipper/internal/ratelimit"
	"github.com/jescarri/open-nipper/internal/security"
	"github.com/jescarri/open-nipper/internal/telemetry"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Open-Nipper gateway",
	RunE:  runServe,
}

func runServe(cmd *cobra.Command, _ []string) error {
	if logLevel != "" {
		if err := os.Setenv("NIPPER_LOG_LEVEL", logLevel); err != nil {
			return fmt.Errorf("setting log level override: %w", err)
		}
	}

	// --- 1. Load configuration ---
	configPath, _ := cmd.Root().PersistentFlags().GetString("config")
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// --- 2. Initialize logger ---
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}

	lm := lifecycle.NewManager(log.Named("lifecycle"), 30*time.Second)

	lm.RegisterStop("logger", lifecycle.PhaseLogger, func(_ context.Context) error {
		return log.Sync()
	})

	// --- 3. Initialize OpenTelemetry (noop if not configured) ---
	shutdownTrace, traceErr := telemetry.InitTracing(cmd.Context(), cfg.Telemetry.Tracing, "dev", log)
	if traceErr != nil {
		log.Warn("tracing init failed; using noop", zap.Error(traceErr))
		shutdownTrace = func(context.Context) error { return nil }
	}

	metrics, shutdownMetrics, metricsHandler, metricsErr := telemetry.InitMetrics(cmd.Context(), cfg.Telemetry.Metrics, log, 0)
	if metricsErr != nil {
		log.Warn("metrics init failed; using noop", zap.Error(metricsErr))
		shutdownMetrics = func(context.Context) error { return nil }
		metricsHandler = nil
	}

	lm.RegisterStop("tracing", lifecycle.PhaseTelemetry, shutdownTrace)
	lm.RegisterStop("metrics", lifecycle.PhaseTelemetry, shutdownMetrics)

	// --- 3b. Run startup security audit ---
	_ = security.RunStartupAudit(cmd.Context(), cfg, log.Named("security"))

	// --- 4. Expand datastore path and open SQLite ---
	dbPath, err := config.ExpandDatastorePath(cfg)
	if err != nil {
		return fmt.Errorf("expanding datastore path: %w", err)
	}
	cfg.Datastore.Path = dbPath

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return fmt.Errorf("creating datastore directory: %w", err)
	}

	repo, err := sqlite.Open(dbPath, cfg.Datastore.WALMode, cfg.Datastore.BusyTimeoutMS)
	if err != nil {
		return fmt.Errorf("opening datastore: %w", err)
	}
	lm.RegisterStop("datastore", lifecycle.PhaseDatastore, func(_ context.Context) error {
		return repo.Close()
	})
	log.Info("datastore opened", zap.String("path", dbPath))

	// --- 5. RabbitMQ Management API (optional) ---
	var mgmtClient queue.ManagementClient
	if cfg.Agents.Registration.Enabled && cfg.Agents.RabbitMQManagement.URL != "" {
		mgmtClient = queue.NewHTTPManagementClient(&cfg.Agents.RabbitMQManagement, log)
		log.Info("rabbitmq management client configured", zap.String("url", cfg.Agents.RabbitMQManagement.URL))
	}

	// --- 5b. In-memory agent heartbeat store (POST /agents/health; never persisted to DB) ---
	heartbeatStore := gateway.NewAgentHeartbeatStore()

	// --- 5b2. Heartbeat cleanup: mark agents as failed if no heartbeat seen in 1 min ---
	heartbeatCleanupStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCleanupStop:
				return
			case <-ticker.C:
				heartbeatStore.MarkStaleAsFailed(1 * time.Minute)
			}
		}
	}()
	lm.RegisterStop("heartbeat-cleanup", lifecycle.PhaseConsumers, func(_ context.Context) error {
		close(heartbeatCleanupStop)
		return nil
	})

	// --- 5c. Agent health monitor (queue-based; must exist before admin server to wire provider) ---
	var healthMon *gateway.HealthMonitor
	if mgmtClient != nil {
		healthMon = gateway.NewHealthMonitor(gateway.HealthMonitorDeps{
			Repo:     repo,
			Mgmt:     mgmtClient,
			Config:   &cfg.Agents,
			QueueCfg: &cfg.Queue.RabbitMQ,
			Metrics:  metrics,
			Logger:   log.Named("healthmon"),
		})
		healthMon.Start()
		lm.RegisterStop("health-monitor", lifecycle.PhaseTelemetry, func(_ context.Context) error {
			healthMon.Stop()
			return nil
		})
		log.Info("agent health monitor started")
	}

	// --- 6. Admin API server ---
	var adminServer *admin.Server
	if cfg.Gateway.Admin.Enabled {
		adminServer = admin.NewServer(cfg, repo, mgmtClient, log)
		if healthMon != nil {
			adminServer.SetAgentHealthProvider(healthMon)
		}
		adminServer.SetHeartbeatStore(heartbeatStore)
		if err := adminServer.Start(); err != nil {
			return fmt.Errorf("starting admin server: %w", err)
		}
		log.Info("admin server started",
			zap.String("bind", cfg.Gateway.Admin.Bind),
			zap.Int("port", cfg.Gateway.Admin.Port),
		)
		lm.RegisterStop("admin-server", lifecycle.PhaseHTTP, adminServer.Stop)
	}

	// --- 7. Initialize channel adapters ---
	gatewayURL := strings.TrimRight(cfg.Gateway.BaseURL, "/")
	if gatewayURL == "" || strings.Contains(gatewayURL, "${") {
		gatewayURL = fmt.Sprintf("http://%s:%d", cfg.Gateway.Bind, cfg.Gateway.Port)
	}
	log.Info("gateway base URL", zap.String("baseURL", gatewayURL))
	adapters := make(map[models.ChannelType]channels.ChannelAdapter)

	if cfg.Channels.WhatsApp.Enabled {
		wa := waadapter.NewAdapter(waadapter.AdapterDeps{
			Config:     cfg.Channels.WhatsApp.Config,
			S3Config:   cfg.Channels.WhatsApp.Config.S3,
			Logger:     log.Named("whatsapp"),
			GatewayURL: gatewayURL,
		})
		adapters[models.ChannelTypeWhatsApp] = wa
		log.Info("whatsapp channel adapter enabled")
	}

	if cfg.Channels.Slack.Enabled {
		sl := slackadapter.NewAdapter(slackadapter.AdapterDeps{
			Config: cfg.Channels.Slack.Config,
			Logger: log.Named("slack"),
		})
		adapters[models.ChannelTypeSlack] = sl
		log.Info("slack channel adapter enabled")
	}

	if cfg.Channels.Cron.Enabled {
		cr := cronadapter.NewAdapter(cronadapter.AdapterDeps{
			Config:    cfg.Channels.Cron,
			Logger:    log.Named("cron"),
			Validator: repo.IsUserEnabled,
		})
		adapters[models.ChannelTypeCron] = cr
		log.Info("cron channel adapter enabled", zap.Int("jobs", len(cfg.Channels.Cron.Jobs)))
	}

	if cfg.Channels.MQTT.Enabled {
		mqttUserLister := func(ctx context.Context) ([]string, error) {
			users, err := repo.ListUsers(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, len(users))
			for i, u := range users {
				ids[i] = u.ID
			}
			return ids, nil
		}
		mq := mqttadapter.NewAdapter(mqttadapter.AdapterDeps{
			Config:     cfg.Channels.MQTT.Config,
			Logger:     log.Named("mqtt"),
			UserLister: mqttUserLister,
		})
		adapters[models.ChannelTypeMQTT] = mq
		log.Info("mqtt channel adapter enabled", zap.String("broker", cfg.Channels.MQTT.Config.Broker))
	}

	if cfg.Channels.RabbitMQ.Enabled {
		rmqUserLister := func(ctx context.Context) ([]string, error) {
			users, err := repo.ListUsers(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, len(users))
			for i, u := range users {
				ids[i] = u.ID
			}
			return ids, nil
		}
		rmqChan := rabbitmqadapter.NewAdapter(rabbitmqadapter.AdapterDeps{
			Config:     cfg.Channels.RabbitMQ.Config,
			Logger:     log.Named("rabbitmq-channel"),
			UserLister: rmqUserLister,
		})
		adapters[models.ChannelTypeRabbitMQ] = rmqChan
		log.Info("rabbitmq channel adapter enabled")
	}

	// --- 8. Build message pipeline components ---
	guard := allowlist.New(repo, log.Named("allowlist"))
	resolver := gateway.NewResolver(log.Named("resolver"), "claude-sonnet-4-20250514")
	registry := gateway.NewRegistry()
	dedup := gateway.NewDeduplicator(30 * time.Second)

	// Connect to RabbitMQ, declare topology, and create publisher.
	var publisher queue.Publisher
	var broker *queue.Broker
	if cfg.Queue.Transport == "rabbitmq" && cfg.Queue.RabbitMQ.URL != "" {
		broker = queue.NewBroker(&cfg.Queue.RabbitMQ, log.Named("amqp"))
		if err := broker.Connect(cmd.Context()); err != nil {
			return fmt.Errorf("connecting to RabbitMQ: %w", err)
		}
		lm.RegisterStop("amqp-broker", lifecycle.PhaseBrokers, func(_ context.Context) error {
			return broker.Close()
		})
		log.Info("rabbitmq broker connected", zap.String("url", cfg.Queue.RabbitMQ.URL))

		// Declare static exchanges, queues, and bindings.
		topoCh, err := broker.PublishChannel()
		if err != nil {
			return fmt.Errorf("opening topology channel: %w", err)
		}
		if err := queue.DeclareTopology(topoCh); err != nil {
			topoCh.Close()
			return fmt.Errorf("declaring RabbitMQ topology: %w", err)
		}
		log.Info("rabbitmq topology declared")

		// Ensure per-user queues exist for all provisioned users.
		users, err := repo.ListUsers(cmd.Context())
		if err != nil {
			topoCh.Close()
			return fmt.Errorf("listing users for queue setup: %w", err)
		}
		for _, u := range users {
			if err := queue.DeclareUserQueues(topoCh, u.ID); err != nil {
				log.Warn("failed to declare queues for user",
					zap.String("userId", u.ID), zap.Error(err))
			} else {
				log.Info("user queues declared", zap.String("userId", u.ID))
			}
		}
		topoCh.Close()

		pub, err := queue.NewRabbitMQPublisher(broker, log.Named("publisher"))
		if err != nil {
			return fmt.Errorf("creating RabbitMQ publisher: %w", err)
		}
		lm.RegisterStop("amqp-publisher", lifecycle.PhasePublishers, func(_ context.Context) error {
			return pub.Close()
		})
		publisher = pub
		log.Info("rabbitmq publisher ready")
	}

	msgRouter := gateway.NewRouter(gateway.RouterDeps{
		Logger:    log.Named("router"),
		Repo:      repo,
		Guard:     guard,
		Resolver:  resolver,
		Registry:  registry,
		Publisher: publisher,
		Dedup:     dedup,
		Config:    cfg,
		Metrics:   metrics,
	})

	// --- 8b. Set handlers on push-based adapters ---
	// These adapters normalize internally and produce NipperMessages, so they
	// route through HandleNormalizedMessage (skipping the raw→normalize step).
	pipelineHandler := func(ctx context.Context, msg *models.NipperMessage) error {
		return msgRouter.HandleNormalizedMessage(ctx, msg)
	}
	if a, ok := adapters[models.ChannelTypeCron]; ok {
		cr := a.(*cronadapter.Adapter)
		cr.SetHandler(pipelineHandler)
		// Load cron jobs from DB; seed from config if DB empty
		jobs, err := repo.ListCronJobs(cmd.Context())
		if err != nil {
			log.Warn("loading cron jobs from DB", zap.Error(err))
		} else {
			if len(jobs) == 0 && len(cfg.Channels.Cron.Jobs) > 0 {
				for _, j := range cfg.Channels.Cron.Jobs {
					if err := repo.AddCronJob(cmd.Context(), j); err != nil {
						log.Warn("seed cron job", zap.String("id", j.ID), zap.Error(err))
					}
				}
				jobs, _ = repo.ListCronJobs(cmd.Context())
			}
			cr.SetInitialJobs(jobs)
		}
	}
	if a, ok := adapters[models.ChannelTypeMQTT]; ok {
		a.(*mqttadapter.Adapter).SetHandler(pipelineHandler)
	}
	if a, ok := adapters[models.ChannelTypeRabbitMQ]; ok {
		a.(*rabbitmqadapter.Adapter).SetHandler(pipelineHandler)
	}

	dispatcher := gateway.NewDispatcher(log.Named("dispatcher"), registry, adapters)
	lm.RegisterStop("dispatcher", lifecycle.PhaseConsumers, func(_ context.Context) error {
		dispatcher.Stop()
		return nil
	})
	lm.RegisterStop("deduplicator", lifecycle.PhaseConsumers, func(_ context.Context) error {
		dedup.Stop()
		return nil
	})

	// --- 8c. Start event consumer (nipper-events-gateway → dispatcher) ---
	if cfg.Queue.Transport == "rabbitmq" && broker != nil {
		consumer := queue.NewRabbitMQConsumer(broker, log.Named("event-consumer"))
		consumer.SetHandler(dispatcher.HandleEvent)
		go func() {
			if err := consumer.Start(cmd.Context()); err != nil {
				log.Error("event consumer stopped", zap.Error(err))
			}
		}()
		lm.RegisterStop("event-consumer", lifecycle.PhaseConsumers, func(_ context.Context) error {
			consumer.Stop()
			return nil
		})
		log.Info("event consumer started", zap.String("queue", "nipper-events-gateway"))
	}

	// --- 9. Agent registration handler ---
	var registerHandler *gateway.RegisterHandler
	if cfg.Agents.Registration.Enabled && mgmtClient != nil {
		rateMax := cfg.Agents.Registration.RateLimit
		if rateMax <= 0 {
			rateMax = 10
		}
		limiter := ratelimit.NewLimiter(rateMax, 1*time.Minute)
		registerHandler = gateway.NewRegisterHandler(gateway.RegisterHandlerDeps{
			Repo:    repo,
			Mgmt:    mgmtClient,
			Limiter: limiter,
			Config:  cfg,
			Logger:  log.Named("register"),
		})
		log.Info("agent registration endpoint enabled")
	}

	// --- 9b. Security runtime monitor ---
	runtimeMon := security.NewRuntimeMonitor(security.RuntimeMonitorDeps{
		Config:       cfg,
		Logger:       log.Named("security-runtime"),
		Datastore:    repo,
		QueueChecker: brokerHealthChecker(broker),
	})
	runtimeMon.Start(cmd.Context())
	lm.RegisterStop("runtime-monitor", lifecycle.PhaseConsumers, func(_ context.Context) error {
		runtimeMon.Stop()
		return nil
	})

	// --- 9c. Agent health handler (POST /agents/health; metrics + in-memory heartbeat) ---
	agentHealthHandler := gateway.NewAgentHealthHandler(gateway.AgentHealthHandlerDeps{
		Repo:      repo,
		Metrics:   metrics,
		Heartbeat: heartbeatStore,
		Logger:    log.Named("agent-health"),
	})

	// --- 9d. Agent cron handler (GET/POST/DELETE /agents/me/cron/jobs); always registered so clients get JSON errors instead of 404 ---
	var cronMutator gateway.CronJobMutator
	var atMutator gateway.AtJobMutator
	if a, ok := adapters[models.ChannelTypeCron]; ok {
		cr := a.(*cronadapter.Adapter)
		cronMutator = cr
		atMutator = cr

		// Set at job cleanup (auto-delete from DB after firing).
		cr.SetAtCleanup(func(ctx context.Context, id, userID string) error {
			return repo.RemoveAtJob(ctx, id, userID)
		})

		// Load at jobs from DB.
		atJobs, err := repo.ListAtJobs(cmd.Context())
		if err != nil {
			log.Warn("loading at jobs from DB", zap.Error(err))
		} else if len(atJobs) > 0 {
			cr.LoadAtJobs(cmd.Context(), atJobs)
		}
	}
	agentCronHandler := gateway.NewAgentCronHandler(gateway.AgentCronHandlerDeps{
		Repo:   repo,
		Cron:   cronMutator,
		Logger: log.Named("agent-cron"),
	})
	agentAtHandler := gateway.NewAgentAtHandler(gateway.AgentAtHandlerDeps{
		Repo:   repo,
		At:     atMutator,
		Logger: log.Named("agent-at"),
	})

	// Set the at mutator on the admin server (created before adapters are available).
	if adminServer != nil && atMutator != nil {
		adminServer.SetAtMutator(atMutator)
	}

	// --- 10. Start the main HTTP server ---
	mainServer := gateway.NewServer(gateway.ServerDeps{
		Logger:              log.Named("http"),
		Config:              cfg,
		MsgRouter:           msgRouter,
		Dispatcher:          dispatcher,
		Adapters:            adapters,
		RegisterHandler:     registerHandler,
		AgentHealthHandler:  agentHealthHandler,
		AgentCronHandler:    agentCronHandler,
		AgentAtHandler:      agentAtHandler,
		Metrics:            metrics,
		MetricsHandler:     metricsHandler,
	})

	go func() {
		if err := mainServer.Start(); err != nil {
			log.Error("main HTTP server failed", zap.Error(err))
		}
	}()
	lm.RegisterStop("http-server", lifecycle.PhaseHTTP, func(_ context.Context) error {
		return mainServer.Stop()
	})

	// --- 11. Start channel adapters ---
	startCtx := context.Background()
	for ct, adapter := range adapters {
		if err := adapter.Start(startCtx); err != nil {
			log.Error("channel adapter failed to start",
				zap.String("channelType", string(ct)),
				zap.Error(err),
			)
		}
	}
	for ct, adapter := range adapters {
		ct, adapter := ct, adapter
		lm.RegisterStop("adapter-"+string(ct), lifecycle.PhaseAdapters, func(ctx context.Context) error {
			return adapter.Stop(ctx)
		})
	}

	log.Info("gateway started",
		zap.String("bind", cfg.Gateway.Bind),
		zap.Int("port", cfg.Gateway.Port),
		zap.Int("adapters", len(adapters)),
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down gateway")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()
	if err := lm.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown completed with errors", zap.Error(err))
	}
	log.Info("gateway stopped")
	return nil
}

// brokerHealthChecker wraps a *queue.Broker to implement security.HealthChecker.
// Returns nil if the broker is nil (no RabbitMQ configured).
func brokerHealthChecker(b *queue.Broker) security.HealthChecker {
	if b == nil {
		return nil
	}
	return &brokerPinger{b: b}
}

type brokerPinger struct{ b *queue.Broker }

func (bp *brokerPinger) Ping(_ context.Context) error {
	if bp.b.IsConnected() {
		return nil
	}
	return fmt.Errorf("rabbitmq broker not connected")
}
