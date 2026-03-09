package skills

import "context"

// contextKey type for skill observer in context.
type contextKeySkillObserver struct{}

// SkillObserverContextKey is the key used to attach a SkillObserver to context.
var SkillObserverContextKey = &contextKeySkillObserver{}

// ObserverFromContext returns the SkillObserver from ctx, or nil.
func ObserverFromContext(ctx context.Context) SkillObserver {
	if o, ok := ctx.Value(SkillObserverContextKey).(SkillObserver); ok {
		return o
	}
	return nil
}

// MetricsRecorder records skill metrics (executions, duration, secrets resolved).
// Implementations typically wrap telemetry.Metrics. Optional; pass nil to disable.
type MetricsRecorder interface {
	RecordSkillExecution(skillName string, exitCode int)
	RecordSkillExecutionDuration(skillName string, durationSec float64)
	RecordSkillSecretsResolved(provider string)
}

// SkillObserver is used to emit skill events (e.g. skill_secret_resolved) during execution.
// The runtime provides an implementation via context when handling a message.
type SkillObserver interface {
	RecordSecretResolved(ctx context.Context, skillName, secretName, provider string)
}
