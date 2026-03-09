package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

// envVarPattern matches ${ENV_VAR} placeholders in config values.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Load reads the configuration from configPath (and an optional .local.yaml overlay),
// applies NIPPER_-prefixed environment variable overrides, and resolves ${ENV_VAR} placeholders.
// If the file does not exist, defaults are used and no error is returned.
func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("NIPPER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setViperDefaults(v)

	expanded, err := expandTilde(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: expand path: %w", err)
	}

	v.SetConfigFile(expanded)
	if readErr := v.ReadInConfig(); readErr != nil {
		if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("config: read %s: %w", expanded, readErr)
		}
		// File not found — use defaults; caller should log a warning.
	}

	// Overlay with .local.yaml if it exists.
	localPath := strings.TrimSuffix(expanded, filepath.Ext(expanded)) + ".local.yaml"
	if _, statErr := os.Stat(localPath); statErr == nil {
		v.SetConfigFile(localPath)
		if mergeErr := v.MergeInConfig(); mergeErr != nil {
			return nil, fmt.Errorf("config: merge local overlay %s: %w", localPath, mergeErr)
		}
	}

	cfg := &Config{}
	if unmarshalErr := v.Unmarshal(cfg); unmarshalErr != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", unmarshalErr)
	}

	applyDefaults(cfg)

	// Capture raw secret values before env expansion for security audit.
	cfg.RawSecretFields = collectRawSecretFields(cfg)

	resolveEnvPlaceholders(cfg)

	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("config: validation: %w", err)
	}

	return cfg, nil
}

// Validate checks that required and mutually exclusive config fields are consistent.
func Validate(cfg *Config) error {
	if cfg.Gateway.Port == cfg.Gateway.Admin.Port {
		return fmt.Errorf("gateway.port and gateway.admin.port must be different (both are %d)", cfg.Gateway.Port)
	}

	if cfg.Channels.WhatsApp.Enabled {
		if cfg.Channels.WhatsApp.Config.WuzapiBaseURL == "" {
			return fmt.Errorf("channels.whatsapp.config.wuzapi_base_url is required when whatsapp is enabled")
		}
		if cfg.Channels.WhatsApp.Config.WuzapiToken == "" {
			return fmt.Errorf("channels.whatsapp.config.wuzapi_token is required when whatsapp is enabled")
		}
	}

	if cfg.Channels.Slack.Enabled {
		if cfg.Channels.Slack.Config.BotToken == "" {
			return fmt.Errorf("channels.slack.config.bot_token is required when slack is enabled")
		}
		if cfg.Channels.Slack.Config.SigningSecret == "" {
			return fmt.Errorf("channels.slack.config.signing_secret is required when slack is enabled")
		}
	}

	if cfg.Channels.MQTT.Enabled {
		if cfg.Channels.MQTT.Config.Broker == "" {
			return fmt.Errorf("channels.mqtt.config.broker is required when mqtt is enabled")
		}
	}

	if _, err := expandTilde(cfg.Datastore.Path); err != nil {
		return fmt.Errorf("datastore.path is invalid: %w", err)
	}

	return nil
}

// ExpandDatastorePath expands the ~ in the datastore path and returns the absolute path.
func ExpandDatastorePath(cfg *Config) (string, error) {
	return expandTilde(cfg.Datastore.Path)
}

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, path[1:]), nil
}

// resolveEnvPlaceholders walks the config struct and replaces ${VAR} in string fields.
// This is a simple string-substitution pass; viper's AutomaticEnv handles top-level overrides.
func resolveEnvPlaceholders(cfg *Config) {
	cfg.Gateway.BaseURL = resolveString(cfg.Gateway.BaseURL)
	cfg.Gateway.Admin.Auth.Token = resolveString(cfg.Gateway.Admin.Auth.Token)
	cfg.Channels.WhatsApp.Config.WuzapiToken = resolveString(cfg.Channels.WhatsApp.Config.WuzapiToken)
	cfg.Channels.WhatsApp.Config.WuzapiHMACKey = resolveString(cfg.Channels.WhatsApp.Config.WuzapiHMACKey)
	cfg.Channels.Slack.Config.AppToken = resolveString(cfg.Channels.Slack.Config.AppToken)
	cfg.Channels.Slack.Config.BotToken = resolveString(cfg.Channels.Slack.Config.BotToken)
	cfg.Channels.Slack.Config.SigningSecret = resolveString(cfg.Channels.Slack.Config.SigningSecret)
	cfg.Channels.MQTT.Config.Username = resolveString(cfg.Channels.MQTT.Config.Username)
	cfg.Channels.MQTT.Config.Password = resolveString(cfg.Channels.MQTT.Config.Password)
	cfg.Channels.RabbitMQ.Config.Username = resolveString(cfg.Channels.RabbitMQ.Config.Username)
	cfg.Channels.RabbitMQ.Config.Password = resolveString(cfg.Channels.RabbitMQ.Config.Password)
	cfg.Queue.RabbitMQ.Username = resolveString(cfg.Queue.RabbitMQ.Username)
	cfg.Queue.RabbitMQ.Password = resolveString(cfg.Queue.RabbitMQ.Password)
	cfg.Agents.RabbitMQManagement.Username = resolveString(cfg.Agents.RabbitMQManagement.Username)
	cfg.Agents.RabbitMQManagement.Password = resolveString(cfg.Agents.RabbitMQManagement.Password)
	cfg.Telemetry.Tracing.Endpoint = resolveString(cfg.Telemetry.Tracing.Endpoint)
	cfg.Telemetry.Tracing.Protocol = resolveString(cfg.Telemetry.Tracing.Protocol)
	cfg.Telemetry.Tracing.ServiceName = resolveString(cfg.Telemetry.Tracing.ServiceName)
	cfg.Telemetry.Tracing.URLPath = resolveString(cfg.Telemetry.Tracing.URLPath)
	cfg.Telemetry.Metrics.Endpoint = resolveString(cfg.Telemetry.Metrics.Endpoint)
}

// setViperDefaults registers all known config keys with viper defaults so that AutomaticEnv
// can override them via NIPPER_-prefixed environment variables even without a config file.
func setViperDefaults(v *viper.Viper) {
	v.SetDefault("gateway.bind", "127.0.0.1")
	v.SetDefault("gateway.port", 18789)
	v.SetDefault("gateway.read_timeout_seconds", 30)
	v.SetDefault("gateway.write_timeout_seconds", 30)
	v.SetDefault("gateway.admin.enabled", true)
	v.SetDefault("gateway.admin.bind", "127.0.0.1")
	v.SetDefault("gateway.admin.port", 18790)
	v.SetDefault("gateway.admin.auth.enabled", false)
	v.SetDefault("queue.transport", "rabbitmq")
	v.SetDefault("queue.default_mode", "steer")
	v.SetDefault("queue.rabbitmq.heartbeat", 60)
	v.SetDefault("queue.rabbitmq.reconnect.enabled", true)
	v.SetDefault("queue.rabbitmq.reconnect.initial_delay_ms", 1000)
	v.SetDefault("queue.rabbitmq.reconnect.max_delay_ms", 30000)
	v.SetDefault("agents.health_check_interval_seconds", 30)
	v.SetDefault("agents.consumer_timeout_seconds", 60)
	v.SetDefault("agents.registration.enabled", true)
	v.SetDefault("agents.registration.rate_limit", 10)
	v.SetDefault("agents.registration.token_rotation_on_register", true)
	v.SetDefault("security.rate_limit.per_user.messages_per_minute", 20)
	v.SetDefault("security.rate_limit.per_user.messages_per_hour", 200)
	v.SetDefault("datastore.path", "~/.open-nipper/nipper.db")
	v.SetDefault("datastore.wal_mode", true)
	v.SetDefault("datastore.busy_timeout_ms", 5000)
	v.SetDefault("datastore.backup.enabled", true)
	v.SetDefault("datastore.backup.schedule", "0 2 * * *")
	v.SetDefault("datastore.backup.retention_days", 30)
	v.SetDefault("datastore.backup.path", "~/.open-nipper/backups/")
	v.SetDefault("telemetry.tracing.service_name", "open-nipper-gateway")
	v.SetDefault("telemetry.tracing.sample_rate", 1.0)
	v.SetDefault("telemetry.metrics.prometheus_port", 9090)
	v.SetDefault("channels.whatsapp.config.webhook_path", "/webhook/whatsapp")
	v.SetDefault("channels.slack.config.webhook_path", "/webhook/slack")
	v.SetDefault("channels.mqtt.config.topic_prefix", "nipper")
	v.SetDefault("channels.mqtt.config.qos", 1)
	v.SetDefault("channels.mqtt.config.keep_alive", 60)
	v.SetDefault("observability.enabled", true)
	v.SetDefault("observability.sanitizer.pii_redaction", true)
	v.SetDefault("observability.sanitizer.credential_detection", true)
	v.SetDefault("observability.sanitizer.secret_scrubbing", true)
}

// collectRawSecretFields returns the pre-resolution values of secret config fields.
// Used by the security audit to detect literal secrets vs ${ENV_VAR} placeholders.
func collectRawSecretFields(cfg *Config) map[string]string {
	return map[string]string{
		"channels.whatsapp.config.wuzapi_token":     cfg.Channels.WhatsApp.Config.WuzapiToken,
		"channels.whatsapp.config.wuzapi_hmac_key":  cfg.Channels.WhatsApp.Config.WuzapiHMACKey,
		"channels.slack.config.bot_token":           cfg.Channels.Slack.Config.BotToken,
		"channels.slack.config.signing_secret":      cfg.Channels.Slack.Config.SigningSecret,
		"channels.slack.config.app_token":           cfg.Channels.Slack.Config.AppToken,
		"channels.mqtt.config.password":             cfg.Channels.MQTT.Config.Password,
		"channels.rabbitmq_channel.config.password": cfg.Channels.RabbitMQ.Config.Password,
		"queue.rabbitmq.password":                   cfg.Queue.RabbitMQ.Password,
		"agents.rabbitmq_management.password":       cfg.Agents.RabbitMQManagement.Password,
		"gateway.admin.auth.token":                  cfg.Gateway.Admin.Auth.Token,
	}
}

// resolveString replaces ${ENV_VAR} references with their environment variable values.
func resolveString(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})
}
