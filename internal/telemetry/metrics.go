package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// GaugeValue is a thread-safe holder for a gauge that can be set to an absolute value.
// In OTel v1.23, synchronous Int64Gauge is not available; we use an observable gauge with a callback.
type GaugeValue struct {
	val atomic.Int64
}

// Set stores the current value of the gauge.
func (g *GaugeValue) Set(v int64) { g.val.Store(v) }

// Load returns the current value.
func (g *GaugeValue) Load() int64 { return g.val.Load() }

// Metrics holds all pre-defined metric instruments for the gateway.
type Metrics struct {
	MessagesReceivedTotal   metric.Int64Counter
	MessagesRejectedTotal   metric.Int64Counter
	MessagesPublishedTotal  metric.Int64Counter
	EventsConsumedTotal     metric.Int64Counter
	ResponsesDeliveredTotal metric.Int64Counter
	HTTPRequestDuration     metric.Float64Histogram
	AllowlistCacheHitTotal  metric.Int64Counter
	AllowlistCacheMissTotal metric.Int64Counter
	RMQPublishErrorsTotal   metric.Int64Counter
	AgentHealthReportsTotal metric.Int64Counter

	// Skill metrics (Stage 5).
	SkillExecutionsTotal         metric.Int64Counter
	SkillExecutionDurationSeconds metric.Float64Histogram
	SkillSecretsResolvedTotal     metric.Int64Counter

	// Gauge values — callers write to these; the registered callback reads them.
	QueueDepth         GaugeValue
	AgentConsumerCount GaugeValue
	AgentCount         GaugeValue

	// Skills loaded gauge: observable reads (agent_id, count).
	skillsLoadedAgentID atomic.Value // string; use SetSkillsLoadedAgentID to set
	SkillsLoadedCount   GaugeValue
}

// InitMetrics configures the OpenTelemetry meter provider.
// Returns the Metrics instruments, a shutdown function, and an optional Prometheus HTTP handler.
// prometheusServePort: if >0 and exporter is "prometheus", a dedicated server is started on that port and handler is nil.
// If 0 and exporter is "prometheus", no server is started and the returned handler should be mounted (e.g. GET /metrics).
// If metrics are disabled, noop instruments are returned and handler is nil.
func InitMetrics(ctx context.Context, cfg config.MetricsConfig, log *zap.Logger, prometheusServePort int) (m *Metrics, shutdown func(context.Context) error, metricsHandler http.Handler, err error) {
	var mp metric.MeterProvider

	if !cfg.Enabled {
		mp = NoopMeterProvider()
		otel.SetMeterProvider(mp)
		m = buildMetrics(mp)
		return m, func(context.Context) error { return nil }, nil, nil
	}

	switch cfg.Exporter {
	case "prometheus":
		exp, err := otelprometheus.New()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("metrics: prometheus exporter: %w", err)
		}
		sdkMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
		otel.SetMeterProvider(sdkMP)
		mp = sdkMP
		m = buildMetrics(mp)

		// Exporter registers with prometheus.DefaultRegisterer when created with New(); Handler serves that registry.
		handler := promhttp.Handler()

		if prometheusServePort > 0 {
			mux := http.NewServeMux()
			mux.Handle("/metrics", handler)
			srv := &http.Server{
				Addr:    fmt.Sprintf(":%d", prometheusServePort),
				Handler: mux,
			}
			go func() {
				if log != nil {
					log.Info("metrics: prometheus server started", zap.Int("port", prometheusServePort))
				}
				if serveErr := srv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
					if log != nil {
						log.Error("metrics: prometheus server error", zap.Error(serveErr))
					}
				}
			}()
			return m, sdkMP.Shutdown, nil, nil
		}
		return m, sdkMP.Shutdown, handler, nil

	case "otlp":
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
		if err != nil {
			if log != nil {
				log.Warn("metrics: OTLP exporter unavailable, falling back to noop",
					zap.String("endpoint", cfg.Endpoint),
					zap.Error(err),
				)
			}
			mp = NoopMeterProvider()
			otel.SetMeterProvider(mp)
			m = buildMetrics(mp)
			return m, func(context.Context) error { return nil }, nil, nil
		}
		sdkMP := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
		otel.SetMeterProvider(sdkMP)
		mp = sdkMP
		m = buildMetrics(mp)
		return m, sdkMP.Shutdown, nil, nil

	default:
		mp = NoopMeterProvider()
		otel.SetMeterProvider(mp)
		m = buildMetrics(mp)
		return m, func(context.Context) error { return nil }, nil, nil
	}
}

func buildMetrics(mp metric.MeterProvider) *Metrics {
	meter := mp.Meter("open-nipper-gateway")
	m := &Metrics{}

	mustCounter := func(name, desc string) metric.Int64Counter {
		c, _ := meter.Int64Counter(name, metric.WithDescription(desc))
		return c
	}
	mustHistogram := func(name, desc string) metric.Float64Histogram {
		h, _ := meter.Float64Histogram(name, metric.WithDescription(desc))
		return h
	}

	m.MessagesReceivedTotal = mustCounter("nipper_messages_received_total", "Total inbound messages received")
	m.MessagesRejectedTotal = mustCounter("nipper_messages_rejected_total", "Total messages rejected")
	m.MessagesPublishedTotal = mustCounter("nipper_messages_published_total", "Total messages published to queue")
	m.EventsConsumedTotal = mustCounter("nipper_events_consumed_total", "Total events consumed from agent queue")
	m.ResponsesDeliveredTotal = mustCounter("nipper_responses_delivered_total", "Total responses delivered to channels")
	m.HTTPRequestDuration = mustHistogram("nipper_http_request_duration_seconds", "HTTP request latency")
	m.AllowlistCacheHitTotal = mustCounter("nipper_allowlist_cache_hit_total", "Allowlist cache hits")
	m.AllowlistCacheMissTotal = mustCounter("nipper_allowlist_cache_miss_total", "Allowlist cache misses")
	m.RMQPublishErrorsTotal = mustCounter("nipper_rmq_publish_errors_total", "RabbitMQ publish errors")
	m.AgentHealthReportsTotal = mustCounter("nipper_agent_health_reports_total", "Total agent health status reports received via POST /agents/health")

	m.SkillExecutionsTotal = mustCounter("nipper_agent_skill_executions_total", "Total skill executions")
	m.SkillExecutionDurationSeconds = mustHistogram("nipper_agent_skill_execution_duration_seconds", "Skill execution duration in seconds")
	m.SkillSecretsResolvedTotal = mustCounter("nipper_agent_skill_secrets_resolved_total", "Total skill secrets resolved")

	// Register observable gauges backed by atomic GaugeValues.
	_, _ = meter.Int64ObservableGauge("nipper_queue_depth",
		metric.WithDescription("Current agent queue depth per user"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.QueueDepth.Load())
			return nil
		}),
	)
	_, _ = meter.Int64ObservableGauge("nipper_agent_consumer_count",
		metric.WithDescription("Number of agent consumers per user"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.AgentConsumerCount.Load())
			return nil
		}),
	)
	_, _ = meter.Int64ObservableGauge("nipper_agent_count",
		metric.WithDescription("Number of agents (users with at least one active agent queue)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(m.AgentCount.Load())
			return nil
		}),
	)
	_, _ = meter.Int64ObservableGauge("nipper_agent_skills_loaded",
		metric.WithDescription("Number of skills loaded at startup"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if agentID, ok := m.skillsLoadedAgentID.Load().(string); ok && agentID != "" {
				o.Observe(m.SkillsLoadedCount.Load(), metric.WithAttributes(attribute.String("agent_id", agentID)))
			}
			return nil
		}),
	)

	return m
}
