// Package telemetry provides OpenTelemetry tracing and metrics initialization.
// When telemetry is disabled, noop providers are installed so all instrumentation
// calls compile and run without error or log noise.
package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// InstallNoopProviders sets the global OpenTelemetry providers to noop implementations.
// This must be called before any instrumentation code runs when telemetry is disabled.
func InstallNoopProviders() {
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
}

// NoopTracerProvider returns a noop TracerProvider (useful for testing).
func NoopTracerProvider() trace.TracerProvider {
	return tracenoop.NewTracerProvider()
}

// NoopMeterProvider returns a noop MeterProvider (useful for testing).
func NoopMeterProvider() metric.MeterProvider {
	return metricnoop.NewMeterProvider()
}
