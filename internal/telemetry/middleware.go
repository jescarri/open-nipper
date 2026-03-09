package telemetry

import (
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "open-nipper-gateway"

// PathMetrics is the path that must not be traced or logged (scrape endpoint).
const PathMetrics = "/metrics"

// pathsExcludedFromTelemetry are HTTP paths that must not be traced or recorded in metrics.
var pathsExcludedFromTelemetry = map[string]struct{}{
	"/metrics":       {}, // Prometheus scrape endpoint
	"/health":        {}, // Main server health check
	"/agents/health": {}, // Agent heartbeat POST
}

// PathExcludedFromTelemetry returns true if the path should not be traced, logged, or recorded in metrics.
func PathExcludedFromTelemetry(path string) bool {
	_, ok := pathsExcludedFromTelemetry[path]
	return ok
}

// HTTPMiddleware returns an HTTP middleware that creates OpenTelemetry spans
// for each request and records request duration to the provided histogram.
// Healthcheck and metrics endpoints are not traced or recorded.
func HTTPMiddleware(m *Metrics) func(http.Handler) http.Handler {
	tracer := otel.Tracer(tracerName)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, excluded := pathsExcludedFromTelemetry[r.URL.Path]; excluded {
				next.ServeHTTP(w, r)
				return
			}
			ctx, span := tracer.Start(r.Context(),
				fmt.Sprintf("%s %s", r.Method, r.URL.Path),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					semconv.HTTPMethod(r.Method),
					semconv.HTTPTarget(r.URL.Path),
					attribute.String("http.remote_addr", r.RemoteAddr),
				),
			)
			defer span.End()

			rw := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
			start := time.Now()

			next.ServeHTTP(rw, r.WithContext(ctx))

			elapsed := time.Since(start).Seconds()
			span.SetAttributes(semconv.HTTPStatusCode(rw.statusCode))

			if m != nil && m.HTTPRequestDuration != nil {
				m.HTTPRequestDuration.Record(ctx, elapsed,
					metric.WithAttributes(
						semconv.HTTPMethod(r.Method),
						attribute.String("http.route", r.URL.Path),
						semconv.HTTPStatusCode(rw.statusCode),
					),
				)
			}
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

// Tracer returns a named tracer for the gateway instrumentation.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}
