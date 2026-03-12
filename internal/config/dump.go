package config

import (
	"gopkg.in/yaml.v3"
)

// DumpGatewayConfig returns a default gateway Config as YAML bytes.
// Secret fields use ${ENV_VAR} placeholders so users see which env vars to set.
func DumpGatewayConfig() ([]byte, error) {
	cfg := &Config{
		Gateway: GatewayConfig{
			Bind:                "0.0.0.0",
			Port:                18789,
			BaseURL:             "https://nipper.example.com",
			ReadTimeoutSeconds:  30,
			WriteTimeoutSeconds: 30,
			Admin: AdminConfig{
				Enabled: true,
				Bind:    "0.0.0.0",
				Port:    18790,
				Auth: AdminAuthConfig{
					Enabled: false,
					Token:   "${ADMIN_API_TOKEN}",
				},
			},
		},
		Channels: ChannelsConfig{
			WhatsApp: WhatsAppChannelConfig{Enabled: false},
			Slack:    SlackChannelConfig{Enabled: false},
			Cron: CronChannelConfig{
				Enabled: true,
				Jobs:    []CronJob{},
			},
			MQTT:     MQTTChannelConfig{Enabled: false},
			RabbitMQ: RabbitMQChannelConfig{Enabled: false},
		},
		Queue: QueueConfig{
			Transport: "rabbitmq",
			RabbitMQ: QueueRabbitMQConfig{
				URL:       "amqp://rabbitmq:5672",
				Username:  "${RABBITMQ_USERNAME}",
				Password:  "${RABBITMQ_PASSWORD}",
				VHost:     "/nipper",
				Heartbeat: 60,
				Reconnect: ReconnectConfig{
					Enabled:        true,
					InitialDelayMS: 1000,
					MaxDelayMS:     30000,
				},
			},
			DefaultMode: "steer",
		},
		Agents: AgentsConfig{
			HealthCheckIntervalSeconds: 30,
			ConsumerTimeoutSeconds:     60,
			Registration: AgentRegistrationConfig{
				Enabled:                 true,
				RateLimit:               10,
				TokenRotationOnRegister: true,
			},
			RabbitMQManagement: RMQManagementConfig{
				URL:      "http://rabbitmq:15672",
				Username: "${RABBITMQ_MGMT_USERNAME}",
				Password: "${RABBITMQ_MGMT_PASSWORD}",
			},
		},
		Security: SecurityConfig{
			RateLimit: RateLimitConfig{
				PerUser: PerUserRateLimit{
					MessagesPerMinute: 20,
					MessagesPerHour:   200,
				},
			},
			Tools: ToolsConfig{
				Policy: ToolPolicy{
					Allow: []string{},
					Deny:  []string{},
				},
			},
		},
		Datastore: DatastoreConfig{
			Path:          "/data/nipper.db",
			WALMode:       true,
			BusyTimeoutMS: 5000,
			Backup: BackupConfig{
				Enabled:       true,
				Schedule:      "0 2 * * *",
				RetentionDays: 30,
				Path:          "/data/backups/",
			},
		},
		Telemetry: TelemetryConfig{
			Tracing: TracingConfig{
				Enabled:     false,
				Exporter:    "otlp",
				Protocol:    "http",
				Endpoint:    "${OTEL_EXPORTER_OTLP_ENDPOINT}",
				ServiceName: "nipper-gateway",
				SampleRate:  1.0,
			},
			Metrics: MetricsConfig{
				Enabled:        true,
				Exporter:       "prometheus",
				PrometheusPort: 9090,
			},
		},
		Observability: ObservabilityConfig{
			Enabled: true,
			Sanitizer: SanitizerConfig{
				PIIRedaction:        true,
				CredentialDetection: true,
				SecretScrubbing:     true,
			},
		},
	}

	return yaml.Marshal(cfg)
}

// DumpAgentConfig returns a default AgentFileConfig as YAML bytes.
// Secret fields use ${ENV_VAR} placeholders so users see which env vars to set.
func DumpAgentConfig() ([]byte, error) {
	cfg := &AgentFileConfig{
		Agent: AgentRuntimeConfig{
			BasePath: "/data",
			Inference: InferenceConfig{
				Provider:       "openai",
				Model:          "gpt-4o",
				APIKey:         "${OPENAI_API_KEY}",
				BaseURL:        "${INFERENCE_BASE_URL}",
				Temperature:    0.7,
				MaxTokens:      0,
				TimeoutSeconds: 120,
			},
			MaxSteps: 25,
			Prompt: PromptConfig{
				SystemPrompt:                   "You are a helpful assistant.",
				CompactionLevel:                "auto",
				AutoCompactionThresholdPercent: 60,
				CompactKeepLines:               20,
			},
			Tools: AgentToolsConfig{
				WebFetch:   true,
				WebSearch:  true,
				Bash:       true,
				DocFetcher: false,
				Memory:     true,
				Weather:    false,
				Cron:       true,
				WebSearchConfig: WebSearchConfig{
					Google:     WebSearchEngineGoogle{Enabled: false},
					DuckDuckGo: WebSearchEngineDuckDuckGo{Enabled: true},
				},
			},
			Memory: MemoryConfig{
				MaxDays:   7,
				MaxTokens: 4000,
			},
			Sandbox: SandboxConfig{
				Enabled:        true,
				Image:          "ubuntu:noble",
				WorkDir:        "/workspace",
				MemoryLimitMB:  2048,
				CPULimit:       2.0,
				TimeoutSeconds: 120,
				NetworkEnabled: true,
				ReadOnly:       false,
				VolumeMounts:   map[string]string{},
				Env:            []string{"DEBIAN_FRONTEND=noninteractive"},
			},
			Skills: SkillsConfig{
				Enabled: false,
				Path:    "",
			},
			MCP: []MCPServerConfig{
				{
					Name:             "example-sse",
					Transport:        "sse",
					URL:              "https://mcp.example.com/sse",
					KeepAliveSeconds: 30,
					Auth: &MCPAuthConfig{
						Type:      "oidc",
						ClientID:  "${OAUTH_CLIENT_ID}",
						Scopes:    []string{"openid", "email"},
						IssuerURL: "https://accounts.google.com",
					},
				},
			},
			Secrets:                  SecretsConfig{EnvMap: map[string]string{}},
			HeartbeatIntervalSeconds: 1,
		},
		Telemetry: TelemetryConfig{
			Tracing: TracingConfig{
				Enabled:     false,
				Exporter:    "otlp",
				Protocol:    "http",
				Endpoint:    "${OTEL_EXPORTER_OTLP_ENDPOINT}",
				ServiceName: "nipper-agent",
				SampleRate:  1.0,
			},
			Metrics: MetricsConfig{
				Enabled:        true,
				Exporter:       "prometheus",
				PrometheusPort: 9091,
			},
		},
	}

	return yaml.Marshal(cfg)
}
