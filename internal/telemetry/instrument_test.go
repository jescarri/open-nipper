package telemetry

import (
	"context"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/codes"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestRecordMessageReceived_NilMetrics(t *testing.T) {
	RecordMessageReceived(context.Background(), nil, "whatsapp", "")
}

func TestRecordMessageReceived_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordMessageReceived(context.Background(), m, "whatsapp", "user-01")
}

func TestRecordMessageRejected_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordMessageRejected(context.Background(), m, "whatsapp", "not_in_allowlist", "user-01")
}

func TestRecordMessagePublished_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordMessagePublished(context.Background(), m, "whatsapp", "queue", "user-01")
}

func TestRecordEventConsumed_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordEventConsumed(context.Background(), m, "done")
}

func TestRecordResponseDelivered_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordResponseDelivered(context.Background(), m, "whatsapp")
}

func TestRecordPublishError_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordPublishError(context.Background(), m)
}

func TestRecordPublishError_NilMetrics(t *testing.T) {
	RecordPublishError(context.Background(), nil)
}

func TestStartSpan_ReturnsSpan(t *testing.T) {
	InstallNoopProviders()
	ctx, span := StartSpan(context.Background(), "test-span")
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	span.End()
}

func TestSpanError_NilSpan(t *testing.T) {
	SpanError(nil, fmt.Errorf("test error"))
}

func TestSpanError_NilError(t *testing.T) {
	span := tracenoop.Span{}
	SpanError(span, nil)
}

func TestSpanError_WithError(t *testing.T) {
	InstallNoopProviders()
	_, span := StartSpan(context.Background(), "test")
	SpanError(span, fmt.Errorf("something went wrong"))
	span.End()
}

func TestSpanOK_NilSpan(t *testing.T) {
	SpanOK(nil)
}

func TestSpanOK_WithSpan(t *testing.T) {
	InstallNoopProviders()
	_, span := StartSpan(context.Background(), "test")
	SpanOK(span)
	span.End()
}

// verifySpan is a helper type for testing that implements trace.Span.
type verifySpan struct {
	tracenoop.Span
	statusCode codes.Code
}

func (v *verifySpan) SetStatus(code codes.Code, _ string) {
	v.statusCode = code
}

func TestRecordAllNilSafe(t *testing.T) {
	ctx := context.Background()
	RecordMessageReceived(ctx, nil, "", "")
	RecordMessageRejected(ctx, nil, "", "", "")
	RecordMessagePublished(ctx, nil, "", "", "")
	RecordEventConsumed(ctx, nil, "")
	RecordResponseDelivered(ctx, nil, "")
	RecordPublishError(ctx, nil)
	RecordAgentHealthReport(ctx, nil, "", "")
	RecordSkillsLoaded(ctx, nil, "", 0)
	RecordSkillExecution(ctx, nil, "", 0)
	RecordSkillExecutionDuration(ctx, nil, "", 0)
	RecordSkillSecretsResolved(ctx, nil, "")
}

func TestRecordAgentHealthReport_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordAgentHealthReport(context.Background(), m, "agt-01", "healthy")
}

func TestRecordSkillsLoaded_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordSkillsLoaded(context.Background(), m, "agent-01", 3)
	if m.SkillsLoadedCount.Load() != 3 {
		t.Errorf("SkillsLoadedCount = %d, want 3", m.SkillsLoadedCount.Load())
	}
}

func TestRecordSkillExecution_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordSkillExecution(context.Background(), m, "deploy", 0)
	RecordSkillExecution(context.Background(), m, "deploy", 1)
}

func TestRecordSkillExecutionDuration_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordSkillExecutionDuration(context.Background(), m, "deploy", 1.5)
}

func TestRecordSkillSecretsResolved_WithMetrics(t *testing.T) {
	InstallNoopProviders()
	m := buildMetrics(NoopMeterProvider())
	RecordSkillSecretsResolved(context.Background(), m, "env")
}
