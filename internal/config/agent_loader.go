package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// LoadAgentConfig loads the agent configuration from configPath.
// If configPath is empty or the file does not exist, sensible defaults are returned.
// ${ENV_VAR} placeholders in string fields are expanded at load time.
func LoadAgentConfig(configPath string) (*AgentFileConfig, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("NIPPER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setAgentViperDefaults(v)

	if configPath != "" {
		expanded, err := expandTilde(configPath)
		if err != nil {
			return nil, fmt.Errorf("agent config: expand path: %w", err)
		}
		v.SetConfigFile(expanded)
		if readErr := v.ReadInConfig(); readErr != nil {
			if !os.IsNotExist(readErr) {
				return nil, fmt.Errorf("agent config: read %s: %w", expanded, readErr)
			}
		}
	}

	cfg := &AgentFileConfig{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("agent config: unmarshal: %w", err)
	}

	applyAgentDefaults(&cfg.Agent)
	resolveAgentEnvPlaceholders(&cfg.Agent)
	resolveTelemetryEnvPlaceholders(&cfg.Telemetry)

	return cfg, nil
}

func setAgentViperDefaults(v *viper.Viper) {
	v.SetDefault("agent.base_path", "${HOME}/.open-nipper")
	v.SetDefault("agent.inference.provider", "openai")
	v.SetDefault("agent.inference.model", "gpt-4o")
	v.SetDefault("agent.inference.temperature", 0.7)
	v.SetDefault("agent.inference.max_tokens", 0)
	v.SetDefault("agent.inference.context_window_size", 0)
	v.SetDefault("agent.max_steps", 25)
	v.SetDefault("agent.prompt.system_prompt", "You are a helpful assistant.")
	v.SetDefault("agent.prompt.compaction_level", "auto")
	v.SetDefault("agent.prompt.auto_compaction_threshold_percent", 60)
	v.SetDefault("agent.prompt.compact_keep_lines", 20)
	v.SetDefault("agent.tools.web_fetch", false)
	v.SetDefault("agent.tools.web_search", false)
	v.SetDefault("agent.tools.bash", false)
	v.SetDefault("agent.sandbox.image", "ubuntu:noble")
	v.SetDefault("agent.sandbox.work_dir", "/workspace")
	v.SetDefault("agent.sandbox.memory_limit_mb", 2048)
	v.SetDefault("agent.sandbox.cpu_limit", 2.0)
	v.SetDefault("agent.sandbox.timeout_seconds", 120)
	v.SetDefault("agent.sandbox.read_only", true)
	v.SetDefault("agent.tools.web_search_config.duck_duck_go.enabled", true)
	v.SetDefault("agent.tools.web_search_config.google.enabled", false)
	v.SetDefault("agent.heartbeat_interval_seconds", 1)
	v.SetDefault("agent.skills.enabled", false)
	v.SetDefault("telemetry.tracing.sample_rate", 1.0) // 1.0 = trace 100% of calls
}

func applyAgentDefaults(cfg *AgentRuntimeConfig) {
	if cfg.BasePath == "" {
		cfg.BasePath = "${HOME}/.open-nipper"
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 25
	}
	if cfg.HeartbeatIntervalSeconds < 0 {
		cfg.HeartbeatIntervalSeconds = 0
	}
	if cfg.Inference.Provider == "" {
		cfg.Inference.Provider = "openai"
	}
	if cfg.Inference.Model == "" {
		cfg.Inference.Model = "gpt-4o"
	}
	// MaxTokens 0 = auto: omit from API request so the server picks the
	// optimal output length per request (context_window − prompt_tokens).
	// This makes model switching transparent without manual tuning.
	if cfg.Inference.MaxTokens < 0 {
		cfg.Inference.MaxTokens = 0
	}
	if cfg.Prompt.SystemPrompt == "" {
		cfg.Prompt.SystemPrompt = "You are a helpful assistant."
	}
	if cfg.Sandbox.Image == "" {
		cfg.Sandbox.Image = "ubuntu:noble"
	}
	if cfg.Sandbox.WorkDir == "" {
		cfg.Sandbox.WorkDir = "/workspace"
	}
	if cfg.Sandbox.MemoryLimitMB <= 0 {
		cfg.Sandbox.MemoryLimitMB = 2048
	}
	if cfg.Sandbox.TimeoutSeconds <= 0 {
		cfg.Sandbox.TimeoutSeconds = 120
	}
	// Web search: exactly one engine must be enabled when web_search is true; default to DuckDuckGo if none set.
	if cfg.Tools.WebSearch && !cfg.Tools.WebSearchConfig.Google.Enabled && !cfg.Tools.WebSearchConfig.DuckDuckGo.Enabled {
		cfg.Tools.WebSearchConfig.DuckDuckGo.Enabled = true
	}
}

func resolveAgentEnvPlaceholders(cfg *AgentRuntimeConfig) {
	// BasePath: expand ${HOME} and ~ so runtime and profile paths use actual home dir.
	cfg.BasePath = resolveString(cfg.BasePath)
	if strings.HasPrefix(cfg.BasePath, "~") {
		if expanded, err := expandTilde(cfg.BasePath); err == nil {
			cfg.BasePath = expanded
		}
	}

	cfg.Inference.APIKey = resolveString(cfg.Inference.APIKey)
	cfg.Inference.BaseURL = resolveString(cfg.Inference.BaseURL)

	// Tool-specific credentials/config.
	cfg.Tools.WebSearchConfig.Google.GoogleAPIKey = resolveString(cfg.Tools.WebSearchConfig.Google.GoogleAPIKey)
	cfg.Tools.WebSearchConfig.Google.GoogleCX = resolveString(cfg.Tools.WebSearchConfig.Google.GoogleCX)

	// S3/Minio config for doc_fetch.
	cfg.S3.Endpoint = resolveString(cfg.S3.Endpoint)
	cfg.S3.Bucket = resolveString(cfg.S3.Bucket)
	cfg.S3.AccessKey = resolveString(cfg.S3.AccessKey)
	cfg.S3.SecretKey = resolveString(cfg.S3.SecretKey)
	cfg.S3.Region = resolveString(cfg.S3.Region)

	// Prompt fields may also contain placeholders.
	cfg.Prompt.SystemPrompt = resolveString(cfg.Prompt.SystemPrompt)
	cfg.Prompt.BootstrapFile = resolveString(cfg.Prompt.BootstrapFile)

	cfg.Skills.Path = resolveString(cfg.Skills.Path)
	if strings.HasPrefix(cfg.Skills.Path, "~") {
		if expanded, err := expandTilde(cfg.Skills.Path); err == nil {
			cfg.Skills.Path = expanded
		}
	}
	// Skills path default: {base_path}/skills (after BasePath is resolved).
	if cfg.Skills.Enabled && cfg.Skills.Path == "" {
		cfg.Skills.Path = cfg.BasePath + "/skills"
	}

	for k, v := range cfg.Secrets.EnvMap {
		cfg.Secrets.EnvMap[k] = resolveString(v)
	}
}

// resolveTelemetryEnvPlaceholders expands ${VAR} in telemetry config.
// If tracing/metrics are enabled but endpoint is empty after expansion, they are disabled
// so the program runs without instrumentation and without OTEL connection errors.
func resolveTelemetryEnvPlaceholders(cfg *TelemetryConfig) {
	cfg.Tracing.Endpoint = resolveString(cfg.Tracing.Endpoint)
	cfg.Tracing.Protocol = resolveString(cfg.Tracing.Protocol)
	cfg.Tracing.ServiceName = resolveString(cfg.Tracing.ServiceName)
	cfg.Metrics.Endpoint = resolveString(cfg.Metrics.Endpoint)
	if cfg.Tracing.Enabled && cfg.Tracing.Endpoint == "" {
		cfg.Tracing.Enabled = false
	}
	if cfg.Metrics.Enabled && cfg.Metrics.Exporter == "otlp" && cfg.Metrics.Endpoint == "" {
		cfg.Metrics.Enabled = false
	}
}
