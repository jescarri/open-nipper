package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
)

// loggingSpanExporter wraps a SpanExporter and logs export failures.
type loggingSpanExporter struct {
	inner tracesdk.SpanExporter
	log   *zap.Logger
}

func (e *loggingSpanExporter) ExportSpans(ctx context.Context, spans []tracesdk.ReadOnlySpan) error {
	err := e.inner.ExportSpans(ctx, spans)
	if e.log != nil {
		if err != nil {
			fields := []zap.Field{zap.Error(err), zap.Int("span_count", len(spans))}
			if errStr := err.Error(); strings.Contains(errStr, "HTTP/1.1") {
				fields = append(fields, zap.String("remediation", "collector expects OTLP over HTTP; set telemetry.tracing.protocol to \"http\""))
			} else if strings.Contains(errStr, "malformed HTTP response") {
				fields = append(fields, zap.String("remediation", "collector expects gRPC; set telemetry.tracing.protocol to \"grpc\""))
			}
			e.log.Warn("tracing: span export failed", fields...)
		} else if len(spans) > 0 {
			e.log.Debug("tracing: spans exported", zap.Int("span_count", len(spans)))
		}
	}
	return err
}

func (e *loggingSpanExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// otlpGRPCDefaultPort is the standard OTLP gRPC port (4317); HTTP is 4318.
const otlpGRPCDefaultPort = 4317

// InitTracing configures the OpenTelemetry trace provider.
// If tracing is disabled or exporter is not "otlp", the global noop provider is installed.
// The returned shutdown function must be called on process exit.
func InitTracing(ctx context.Context, cfg config.TracingConfig, version string, log *zap.Logger) (shutdown func(context.Context) error, err error) {
	if !cfg.Enabled {
		InstallNoopProviders()
		return func(context.Context) error { return nil }, nil
	}
	exporterKind := strings.ToLower(strings.TrimSpace(cfg.Exporter))
	if exporterKind != "" && exporterKind != "otlp" {
		if log != nil {
			log.Debug("tracing: exporter is not otlp, using noop", zap.String("exporter", cfg.Exporter))
		}
		InstallNoopProviders()
		return func(context.Context) error { return nil }, nil
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		if log != nil {
			log.Warn("tracing: endpoint is empty, using noop")
		}
		InstallNoopProviders()
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := newOTLPTraceExporter(ctx, cfg)
	if err != nil {
		if log != nil {
			log.Warn("tracing: OTLP exporter unavailable, falling back to noop",
				zap.String("endpoint", cfg.Endpoint),
				zap.Error(err),
			)
		}
		InstallNoopProviders()
		return func(context.Context) error { return nil }, nil
	}
	protocol := "http"
	if wantGRPC(cfg) {
		protocol = "grpc"
	}
	if log != nil {
		log.Info("tracing: OTLP exporter ready",
			zap.String("protocol", protocol),
			zap.String("endpoint", cfg.Endpoint),
			zap.String("config_protocol", strings.TrimSpace(cfg.Protocol)),
		)
		exporter = &loggingSpanExporter{inner: exporter, log: log}
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("tracer: resource: %w", err)
	}

	sampler := tracesdk.ParentBased(tracesdk.TraceIDRatioBased(cfg.SampleRate))

	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(exporter,
			tracesdk.WithBatchTimeout(2*time.Second),   // flush sooner so export errors surface quickly
			tracesdk.WithExportTimeout(30*time.Second), // allow slow collector to respond
			// WithBlocking() omitted: when queue is full we drop spans so agent never blocks. Enable if you prefer backpressure over trace loss.
		),
		tracesdk.WithResource(res),
		tracesdk.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

func newOTLPTraceExporter(ctx context.Context, cfg config.TracingConfig) (tracesdk.SpanExporter, error) {
	useGRPC := wantGRPC(cfg)
	if useGRPC {
		hostPort, err := endpointToHostPort(cfg.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter (grpc): %w", err)
		}
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(hostPort)}
		if endpointIsInsecure(cfg.Endpoint) {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exp, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter (grpc): %w", err)
		}
		return exp, nil
	}
	opts := []otlptracehttp.Option{}
	if cfg.Endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	}
	if path := strings.TrimSpace(cfg.URLPath); path != "" {
		opts = append(opts, otlptracehttp.WithURLPath(path))
	}
	// Explicit timeout so export failures surface instead of hanging (default 10s in env is often overridden).
	opts = append(opts, otlptracehttp.WithTimeout(30*time.Second))
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter (http): %w", err)
	}
	return exp, nil
}

// wantGRPC returns true when the OTLP gRPC exporter should be used.
// Standard: port 4318 is gRPC, 4317 is HTTP. Explicit cfg.Protocol wins.
func wantGRPC(cfg config.TracingConfig) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "grpc":
		return true
	case "http":
		return false
	}
	port, _ := portFromEndpoint(cfg.Endpoint)
	return port == otlpGRPCDefaultPort
}

func portFromEndpoint(endpoint string) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	portStr := u.Port()
	if portStr == "" {
		if u.Scheme == "https" {
			return 443, nil
		}
		return 80, nil
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, err
	}
	return p, nil
}

func endpointToHostPort(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		host = "localhost"
	}
	port := u.Port()
	if port == "" {
		port = strconv.Itoa(otlpGRPCDefaultPort)
	}
	return host + ":" + port, nil
}

func endpointIsInsecure(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return true
	}
	return u.Scheme != "https"
}
