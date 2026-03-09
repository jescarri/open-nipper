package config

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(cfg *Config) {
	// Gateway defaults
	if cfg.Gateway.Bind == "" {
		cfg.Gateway.Bind = "127.0.0.1"
	}
	if cfg.Gateway.Port == 0 {
		cfg.Gateway.Port = 18789
	}
	if cfg.Gateway.ReadTimeoutSeconds == 0 {
		cfg.Gateway.ReadTimeoutSeconds = 30
	}
	if cfg.Gateway.WriteTimeoutSeconds == 0 {
		cfg.Gateway.WriteTimeoutSeconds = 30
	}

	// Admin defaults
	if cfg.Gateway.Admin.Bind == "" {
		cfg.Gateway.Admin.Bind = "127.0.0.1"
	}
	if cfg.Gateway.Admin.Port == 0 {
		cfg.Gateway.Admin.Port = 18790
	}

	// Queue defaults
	if cfg.Queue.DefaultMode == "" {
		cfg.Queue.DefaultMode = "steer"
	}
	if cfg.Queue.RabbitMQ.Heartbeat == 0 {
		cfg.Queue.RabbitMQ.Heartbeat = 60
	}
	if cfg.Queue.RabbitMQ.Reconnect.InitialDelayMS == 0 {
		cfg.Queue.RabbitMQ.Reconnect.InitialDelayMS = 1000
	}
	if cfg.Queue.RabbitMQ.Reconnect.MaxDelayMS == 0 {
		cfg.Queue.RabbitMQ.Reconnect.MaxDelayMS = 30000
	}

	// Agent defaults
	if cfg.Agents.HealthCheckIntervalSeconds == 0 {
		cfg.Agents.HealthCheckIntervalSeconds = 30
	}
	if cfg.Agents.ConsumerTimeoutSeconds == 0 {
		cfg.Agents.ConsumerTimeoutSeconds = 60
	}

	// Security rate limit defaults
	if cfg.Security.RateLimit.PerUser.MessagesPerMinute == 0 {
		cfg.Security.RateLimit.PerUser.MessagesPerMinute = 20
	}
	if cfg.Security.RateLimit.PerUser.MessagesPerHour == 0 {
		cfg.Security.RateLimit.PerUser.MessagesPerHour = 200
	}

	// Datastore defaults
	if cfg.Datastore.Path == "" {
		cfg.Datastore.Path = "~/.open-nipper/nipper.db"
	}
	if cfg.Datastore.BusyTimeoutMS == 0 {
		cfg.Datastore.BusyTimeoutMS = 5000
	}
	if cfg.Datastore.Backup.Schedule == "" {
		cfg.Datastore.Backup.Schedule = "0 2 * * *"
	}
	if cfg.Datastore.Backup.RetentionDays == 0 {
		cfg.Datastore.Backup.RetentionDays = 30
	}
	if cfg.Datastore.Backup.Path == "" {
		cfg.Datastore.Backup.Path = "~/.open-nipper/backups/"
	}

	// Telemetry defaults
	if cfg.Telemetry.Tracing.ServiceName == "" {
		cfg.Telemetry.Tracing.ServiceName = "open-nipper-gateway"
	}
	if cfg.Telemetry.Tracing.SampleRate == 0 {
		cfg.Telemetry.Tracing.SampleRate = 1.0
	}
	if cfg.Telemetry.Metrics.PrometheusPort == 0 {
		cfg.Telemetry.Metrics.PrometheusPort = 9090
	}

	// WhatsApp defaults
	if cfg.Channels.WhatsApp.Config.WebhookPath == "" {
		cfg.Channels.WhatsApp.Config.WebhookPath = "/webhook/whatsapp"
	}

	// Slack defaults
	if cfg.Channels.Slack.Config.WebhookPath == "" {
		cfg.Channels.Slack.Config.WebhookPath = "/webhook/slack"
	}

	// MQTT defaults
	if cfg.Channels.MQTT.Config.TopicPrefix == "" {
		cfg.Channels.MQTT.Config.TopicPrefix = "nipper"
	}
	if cfg.Channels.MQTT.Config.QoS == 0 {
		cfg.Channels.MQTT.Config.QoS = 1
	}
	if cfg.Channels.MQTT.Config.KeepAlive == 0 {
		cfg.Channels.MQTT.Config.KeepAlive = 60
	}
}
