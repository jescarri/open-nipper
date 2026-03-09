package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestInstallNoopProviders(t *testing.T) {
	InstallNoopProviders()

	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	span.End()
	if ctx == nil {
		t.Error("expected non-nil context from noop tracer")
	}

	meter := otel.Meter("test")
	counter, err := meter.Int64Counter("test_counter")
	if err != nil {
		t.Errorf("unexpected error creating noop counter: %v", err)
	}
	counter.Add(context.Background(), 1)
}

func TestInitTracing_Disabled(t *testing.T) {
	cfg := newDisabledTracingConfig()
	shutdown, err := InitTracing(context.Background(), cfg, "0.0.1", nil)
	if err != nil {
		t.Fatalf("InitTracing disabled: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestInitMetrics_Disabled(t *testing.T) {
	cfg := newDisabledMetricsConfig()
	m, shutdown, _, err := InitMetrics(context.Background(), cfg, nil, 0)
	if err != nil {
		t.Fatalf("InitMetrics disabled: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil Metrics")
	}
	// All instruments must be usable without panicking.
	m.MessagesReceivedTotal.Add(context.Background(), 1)
	m.QueueDepth.Set(5)
	m.HTTPRequestDuration.Record(context.Background(), 0.1)

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
