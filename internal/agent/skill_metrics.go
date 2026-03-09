package agent

import (
	"context"

	"github.com/open-nipper/open-nipper/internal/agent/skills"
	"github.com/open-nipper/open-nipper/internal/telemetry"
)

// skillMetricsRecorder adapts telemetry.Metrics to skills.MetricsRecorder.
type skillMetricsRecorder struct {
	m *telemetry.Metrics
}

// NewSkillMetricsRecorder returns a skills.MetricsRecorder that records to the given Metrics.
// If m is nil, returns nil (caller may pass nil to Executor).
func NewSkillMetricsRecorder(m *telemetry.Metrics) skills.MetricsRecorder {
	if m == nil {
		return nil
	}
	return &skillMetricsRecorder{m: m}
}

func (r *skillMetricsRecorder) RecordSkillExecution(skillName string, exitCode int) {
	telemetry.RecordSkillExecution(context.Background(), r.m, skillName, exitCode)
}

func (r *skillMetricsRecorder) RecordSkillExecutionDuration(skillName string, durationSec float64) {
	telemetry.RecordSkillExecutionDuration(context.Background(), r.m, skillName, durationSec)
}

func (r *skillMetricsRecorder) RecordSkillSecretsResolved(provider string) {
	telemetry.RecordSkillSecretsResolved(context.Background(), r.m, provider)
}
