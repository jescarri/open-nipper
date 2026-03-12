package telemetry

import "github.com/jescarri/open-nipper/internal/config"

func newDisabledTracingConfig() config.TracingConfig {
	return config.TracingConfig{
		Enabled:     false,
		ServiceName: "test",
		SampleRate:  1.0,
	}
}

func newDisabledMetricsConfig() config.MetricsConfig {
	return config.MetricsConfig{
		Enabled: false,
	}
}
