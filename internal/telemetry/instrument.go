package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan creates a child span in the gateway tracer with common attributes.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// RecordMessageReceived increments the messages_received_total counter.
// channelType and userID enable per-channel and per-user rate/count in Prometheus.
func RecordMessageReceived(ctx context.Context, m *Metrics, channelType, userID string) {
	if m == nil || m.MessagesReceivedTotal == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("channel_type", channelType)}
	if userID != "" {
		attrs = append(attrs, attribute.String("user_id", userID))
	}
	m.MessagesReceivedTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordMessageRejected increments the messages_rejected_total counter.
func RecordMessageRejected(ctx context.Context, m *Metrics, channelType, reason, userID string) {
	if m == nil || m.MessagesRejectedTotal == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("channel_type", channelType),
		attribute.String("reason", reason),
	}
	if userID != "" {
		attrs = append(attrs, attribute.String("user_id", userID))
	}
	m.MessagesRejectedTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordMessagePublished increments the messages_published_total counter.
func RecordMessagePublished(ctx context.Context, m *Metrics, channelType, queueMode, userID string) {
	if m == nil || m.MessagesPublishedTotal == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("channel_type", channelType),
		attribute.String("queue_mode", queueMode),
	}
	if userID != "" {
		attrs = append(attrs, attribute.String("user_id", userID))
	}
	m.MessagesPublishedTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordEventConsumed increments the events_consumed_total counter.
func RecordEventConsumed(ctx context.Context, m *Metrics, eventType string) {
	if m == nil || m.EventsConsumedTotal == nil {
		return
	}
	m.EventsConsumedTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("event_type", eventType)),
	)
}

// RecordResponseDelivered increments the responses_delivered_total counter.
func RecordResponseDelivered(ctx context.Context, m *Metrics, channelType string) {
	if m == nil || m.ResponsesDeliveredTotal == nil {
		return
	}
	m.ResponsesDeliveredTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("channel_type", channelType)),
	)
}

// RecordPublishError increments the rmq_publish_errors_total counter.
func RecordPublishError(ctx context.Context, m *Metrics) {
	if m == nil || m.RMQPublishErrorsTotal == nil {
		return
	}
	m.RMQPublishErrorsTotal.Add(ctx, 1)
}

// RecordAgentHealthReport increments the agent_health_reports_total counter.
// status should be "healthy", "degraded", or "unhealthy".
func RecordAgentHealthReport(ctx context.Context, m *Metrics, agentID, status string) {
	if m == nil || m.AgentHealthReportsTotal == nil {
		return
	}
	m.AgentHealthReportsTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("agent_id", agentID),
			attribute.String("status", status),
		),
	)
}

// RecordSkillsLoaded sets the skills-loaded gauge for the given agent (number of skills at startup).
func RecordSkillsLoaded(ctx context.Context, m *Metrics, agentID string, count int64) {
	if m == nil {
		return
	}
	m.skillsLoadedAgentID.Store(agentID)
	m.SkillsLoadedCount.Set(count)
}

// RecordSkillExecution increments the skill executions counter with skill_name and exit_code labels.
func RecordSkillExecution(ctx context.Context, m *Metrics, skillName string, exitCode int) {
	if m == nil || m.SkillExecutionsTotal == nil {
		return
	}
	m.SkillExecutionsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("skill_name", skillName),
		attribute.Int("exit_code", exitCode),
	))
}

// RecordSkillExecutionDuration records the skill execution duration in seconds.
func RecordSkillExecutionDuration(ctx context.Context, m *Metrics, skillName string, durationSec float64) {
	if m == nil || m.SkillExecutionDurationSeconds == nil {
		return
	}
	m.SkillExecutionDurationSeconds.Record(ctx, durationSec, metric.WithAttributes(
		attribute.String("skill_name", skillName),
	))
}

// RecordSkillSecretsResolved increments the secrets-resolved counter for the given provider.
func RecordSkillSecretsResolved(ctx context.Context, m *Metrics, provider string) {
	if m == nil || m.SkillSecretsResolvedTotal == nil {
		return
	}
	m.SkillSecretsResolvedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", provider),
	))
}

// SpanError marks a span as errored with the given error.
func SpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// SpanOK marks a span as successfully completed.
func SpanOK(span trace.Span) {
	if span == nil {
		return
	}
	span.SetStatus(codes.Ok, "")
}
