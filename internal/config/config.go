// Package config defines the configuration structure for the Open-Nipper gateway.
package config

// Config is the root configuration struct populated at startup and passed throughout the process.
type Config struct {
	Gateway       GatewayConfig       `yaml:"gateway"     mapstructure:"gateway"`
	Channels      ChannelsConfig      `yaml:"channels"    mapstructure:"channels"`
	Queue         QueueConfig         `yaml:"queue"       mapstructure:"queue"`
	Agents        AgentsConfig        `yaml:"agents"      mapstructure:"agents"`
	Security      SecurityConfig      `yaml:"security"    mapstructure:"security"`
	Datastore     DatastoreConfig     `yaml:"datastore"   mapstructure:"datastore"`
	Telemetry     TelemetryConfig     `yaml:"telemetry"   mapstructure:"telemetry"`
	Observability ObservabilityConfig `yaml:"observability" mapstructure:"observability"`

	// RawSecretFields holds pre-resolution values of secret config fields for the security audit.
	// Populated by Load() before env var expansion. Used to detect literal secrets vs ${ENV_VAR} placeholders.
	RawSecretFields map[string]string `yaml:"-" mapstructure:"-"`
}

// GatewayConfig holds the main HTTP server configuration.
type GatewayConfig struct {
	Bind                string      `yaml:"bind"                   mapstructure:"bind"`
	Port                int         `yaml:"port"                   mapstructure:"port"`
	BaseURL             string      `yaml:"base_url"               mapstructure:"base_url"`
	ReadTimeoutSeconds  int         `yaml:"read_timeout_seconds"   mapstructure:"read_timeout_seconds"`
	WriteTimeoutSeconds int         `yaml:"write_timeout_seconds"  mapstructure:"write_timeout_seconds"`
	Admin               AdminConfig `yaml:"admin"                  mapstructure:"admin"`
}

// AdminConfig holds the admin API server configuration.
type AdminConfig struct {
	Enabled bool            `yaml:"enabled" mapstructure:"enabled"`
	Bind    string          `yaml:"bind"    mapstructure:"bind"`
	Port    int             `yaml:"port"    mapstructure:"port"`
	Auth    AdminAuthConfig `yaml:"auth"    mapstructure:"auth"`
}

// AdminAuthConfig holds admin API authentication settings.
type AdminAuthConfig struct {
	Enabled bool   `yaml:"enabled" mapstructure:"enabled"`
	Token   string `yaml:"token"   mapstructure:"token"`
}

// ChannelsConfig holds configuration for all inbound channel adapters.
type ChannelsConfig struct {
	WhatsApp WhatsAppChannelConfig `yaml:"whatsapp"          mapstructure:"whatsapp"`
	Slack    SlackChannelConfig    `yaml:"slack"             mapstructure:"slack"`
	Cron     CronChannelConfig     `yaml:"cron"              mapstructure:"cron"`
	MQTT     MQTTChannelConfig     `yaml:"mqtt"              mapstructure:"mqtt"`
	RabbitMQ RabbitMQChannelConfig `yaml:"rabbitmq_channel"  mapstructure:"rabbitmq_channel"`
}

// WhatsAppChannelConfig configures the Wuzapi-backed WhatsApp adapter.
type WhatsAppChannelConfig struct {
	Enabled bool           `yaml:"enabled" mapstructure:"enabled"`
	Config  WhatsAppConfig `yaml:"config"  mapstructure:"config"`
}

// WhatsAppConfig holds Wuzapi-specific settings.
type WhatsAppConfig struct {
	WuzapiBaseURL      string          `yaml:"wuzapi_base_url"      mapstructure:"wuzapi_base_url"`
	WuzapiToken        string          `yaml:"wuzapi_token"         mapstructure:"wuzapi_token"`
	WuzapiHMACKey      string          `yaml:"wuzapi_hmac_key"      mapstructure:"wuzapi_hmac_key"`
	WuzapiInstanceName string          `yaml:"wuzapi_instance_name" mapstructure:"wuzapi_instance_name"`
	WebhookPath        string          `yaml:"webhook_path"         mapstructure:"webhook_path"`
	Events             []string        `yaml:"events"               mapstructure:"events"`
	Delivery           DeliveryOptions `yaml:"delivery"             mapstructure:"delivery"`
	S3                 S3DefaultConfig `yaml:"s3"                   mapstructure:"s3"`
}

// DeliveryOptions configures UX delivery behaviors.
type DeliveryOptions struct {
	MarkAsRead    bool `yaml:"mark_as_read"    mapstructure:"mark_as_read"`
	ShowTyping    bool `yaml:"show_typing"     mapstructure:"show_typing"`
	QuoteOriginal bool `yaml:"quote_original"  mapstructure:"quote_original"`
}

// SlackChannelConfig configures the Slack adapter.
type SlackChannelConfig struct {
	Enabled bool        `yaml:"enabled" mapstructure:"enabled"`
	Config  SlackConfig `yaml:"config"  mapstructure:"config"`
}

// SlackConfig holds Slack-specific settings.
type SlackConfig struct {
	AppToken      string `yaml:"app_token"      mapstructure:"app_token"`
	BotToken      string `yaml:"bot_token"      mapstructure:"bot_token"`
	SigningSecret string `yaml:"signing_secret" mapstructure:"signing_secret"`
	WebhookPath   string `yaml:"webhook_path"   mapstructure:"webhook_path"`
}

// CronChannelConfig configures the cron scheduler adapter.
type CronChannelConfig struct {
	Enabled  bool      `yaml:"enabled"  mapstructure:"enabled"`
	Timezone string    `yaml:"timezone" mapstructure:"timezone"` // IANA timezone name (e.g. "America/Vancouver"); defaults to UTC
	Jobs     []CronJob `yaml:"jobs"     mapstructure:"jobs"`
}

// CronJob defines a single scheduled task.
type CronJob struct {
	ID             string   `yaml:"id"              mapstructure:"id"              json:"id"`
	Schedule       string   `yaml:"schedule"        mapstructure:"schedule"        json:"schedule"`
	UserID         string   `yaml:"user_id"         mapstructure:"user_id"         json:"user_id"`
	Prompt         string   `yaml:"prompt"          mapstructure:"prompt"          json:"prompt"`
	NotifyChannels []string `yaml:"notify_channels" mapstructure:"notify_channels" json:"notify_channels,omitempty"`
}

// AtJob defines a single one-shot scheduled task that fires once and is auto-deleted.
type AtJob struct {
	ID             string   `yaml:"id"              mapstructure:"id"              json:"id"`
	RunAt          string   `yaml:"run_at"          mapstructure:"run_at"          json:"run_at"` // ISO 8601 timestamp
	UserID         string   `yaml:"user_id"         mapstructure:"user_id"         json:"user_id"`
	Prompt         string   `yaml:"prompt"          mapstructure:"prompt"          json:"prompt"`
	NotifyChannels []string `yaml:"notify_channels" mapstructure:"notify_channels" json:"notify_channels,omitempty"`
}

// MQTTChannelConfig configures the MQTT adapter.
type MQTTChannelConfig struct {
	Enabled bool       `yaml:"enabled" mapstructure:"enabled"`
	Config  MQTTConfig `yaml:"config"  mapstructure:"config"`
}

// MQTTConfig holds MQTT broker settings.
type MQTTConfig struct {
	Broker       string          `yaml:"broker"        mapstructure:"broker"`
	ClientID     string          `yaml:"client_id"     mapstructure:"client_id"`
	Username     string          `yaml:"username"      mapstructure:"username"`
	Password     string          `yaml:"password"      mapstructure:"password"`
	TopicPrefix  string          `yaml:"topic_prefix"  mapstructure:"topic_prefix"`
	QoS          int             `yaml:"qos"           mapstructure:"qos"`
	CleanSession bool            `yaml:"clean_session" mapstructure:"clean_session"`
	KeepAlive    int             `yaml:"keep_alive"    mapstructure:"keep_alive"`
	Reconnect    ReconnectConfig `yaml:"reconnect"     mapstructure:"reconnect"`
}

// RabbitMQChannelConfig configures the RabbitMQ service-to-service channel adapter.
type RabbitMQChannelConfig struct {
	Enabled bool               `yaml:"enabled" mapstructure:"enabled"`
	Config  RabbitMQChanConfig `yaml:"config"  mapstructure:"config"`
}

// RabbitMQChanConfig holds RabbitMQ channel adapter settings.
type RabbitMQChanConfig struct {
	URL              string          `yaml:"url"                mapstructure:"url"`
	Username         string          `yaml:"username"           mapstructure:"username"`
	Password         string          `yaml:"password"           mapstructure:"password"`
	VHost            string          `yaml:"vhost"              mapstructure:"vhost"`
	ExchangeInbound  string          `yaml:"exchange_inbound"   mapstructure:"exchange_inbound"`
	ExchangeOutbound string          `yaml:"exchange_outbound"  mapstructure:"exchange_outbound"`
	ExchangeDLX      string          `yaml:"exchange_dlx"       mapstructure:"exchange_dlx"`
	Prefetch         int             `yaml:"prefetch"           mapstructure:"prefetch"`
	Heartbeat        int             `yaml:"heartbeat"          mapstructure:"heartbeat"`
	Reconnect        ReconnectConfig `yaml:"reconnect"          mapstructure:"reconnect"`
}

// ReconnectConfig holds backoff reconnect settings.
type ReconnectConfig struct {
	Enabled        bool `yaml:"enabled"          mapstructure:"enabled"`
	InitialDelayMS int  `yaml:"initial_delay_ms" mapstructure:"initial_delay_ms"`
	MaxDelayMS     int  `yaml:"max_delay_ms"     mapstructure:"max_delay_ms"`
}

// QueueConfig configures the internal Gateway↔Agent RabbitMQ system.
type QueueConfig struct {
	Transport   string                      `yaml:"transport"    mapstructure:"transport"`
	RabbitMQ    QueueRabbitMQConfig         `yaml:"rabbitmq"     mapstructure:"rabbitmq"`
	DefaultMode string                      `yaml:"default_mode" mapstructure:"default_mode"`
	PerChannel  map[string]ChannelQueueMode `yaml:"per_channel"  mapstructure:"per_channel"`
}

// QueueRabbitMQConfig holds connection settings for the internal queue RabbitMQ.
type QueueRabbitMQConfig struct {
	URL       string          `yaml:"url"       mapstructure:"url"`
	Username  string          `yaml:"username"  mapstructure:"username"`
	Password  string          `yaml:"password"  mapstructure:"password"`
	VHost     string          `yaml:"vhost"     mapstructure:"vhost"`
	Heartbeat int             `yaml:"heartbeat" mapstructure:"heartbeat"`
	Reconnect ReconnectConfig `yaml:"reconnect" mapstructure:"reconnect"`
}

// ChannelQueueMode configures per-channel queue behavior.
type ChannelQueueMode struct {
	Mode       string `yaml:"mode"        mapstructure:"mode"`
	DebounceMS int    `yaml:"debounce_ms" mapstructure:"debounce_ms"`
	CollectCap int    `yaml:"collect_cap" mapstructure:"collect_cap"`
	Priority   int    `yaml:"priority"    mapstructure:"priority"`
}

// AgentsConfig configures agent health monitoring and registration.
type AgentsConfig struct {
	HealthCheckIntervalSeconds int                     `yaml:"health_check_interval_seconds" mapstructure:"health_check_interval_seconds"`
	ConsumerTimeoutSeconds     int                     `yaml:"consumer_timeout_seconds"      mapstructure:"consumer_timeout_seconds"`
	Registration               AgentRegistrationConfig `yaml:"registration"                 mapstructure:"registration"`
	RabbitMQManagement         RMQManagementConfig     `yaml:"rabbitmq_management"           mapstructure:"rabbitmq_management"`
}

// AgentRegistrationConfig configures the agent auto-registration endpoint.
type AgentRegistrationConfig struct {
	Enabled                 bool `yaml:"enabled"                    mapstructure:"enabled"`
	RateLimit               int  `yaml:"rate_limit"                 mapstructure:"rate_limit"`
	TokenRotationOnRegister bool `yaml:"token_rotation_on_register" mapstructure:"token_rotation_on_register"`
}

// RMQManagementConfig holds RabbitMQ Management API credentials.
type RMQManagementConfig struct {
	URL      string `yaml:"url"      mapstructure:"url"`
	Username string `yaml:"username" mapstructure:"username"`
	Password string `yaml:"password" mapstructure:"password"`
}

// SecurityConfig configures rate limits and tool policies.
type SecurityConfig struct {
	RateLimit RateLimitConfig `yaml:"rate_limit" mapstructure:"rate_limit"`
	Tools     ToolsConfig     `yaml:"tools"      mapstructure:"tools"`
}

// RateLimitConfig configures per-user message rate limits.
type RateLimitConfig struct {
	PerUser PerUserRateLimit `yaml:"per_user" mapstructure:"per_user"`
}

// PerUserRateLimit sets the message frequency limits per user.
type PerUserRateLimit struct {
	MessagesPerMinute int `yaml:"messages_per_minute" mapstructure:"messages_per_minute"`
	MessagesPerHour   int `yaml:"messages_per_hour"   mapstructure:"messages_per_hour"`
}

// ToolsConfig holds the default tool policy applied to all agents.
type ToolsConfig struct {
	Policy ToolPolicy `yaml:"policy" mapstructure:"policy"`
}

// ToolPolicy lists allowed and denied tool names/patterns.
type ToolPolicy struct {
	Allow []string `yaml:"allow" mapstructure:"allow"`
	Deny  []string `yaml:"deny"  mapstructure:"deny"`
}

// DatastoreConfig configures the SQLite database.
type DatastoreConfig struct {
	Path          string       `yaml:"path"            mapstructure:"path"`
	WALMode       bool         `yaml:"wal_mode"        mapstructure:"wal_mode"`
	BusyTimeoutMS int          `yaml:"busy_timeout_ms" mapstructure:"busy_timeout_ms"`
	Backup        BackupConfig `yaml:"backup"          mapstructure:"backup"`
}

// BackupConfig configures automated SQLite backups.
type BackupConfig struct {
	Enabled       bool   `yaml:"enabled"        mapstructure:"enabled"`
	Schedule      string `yaml:"schedule"       mapstructure:"schedule"`
	RetentionDays int    `yaml:"retention_days" mapstructure:"retention_days"`
	Path          string `yaml:"path"           mapstructure:"path"`
}

// TelemetryConfig configures OpenTelemetry tracing and metrics.
type TelemetryConfig struct {
	Tracing TracingConfig `yaml:"tracing" mapstructure:"tracing"`
	Metrics MetricsConfig `yaml:"metrics" mapstructure:"metrics"`
}

// TracingConfig configures OTLP trace exporting.
type TracingConfig struct {
	Enabled     bool    `yaml:"enabled"      mapstructure:"enabled"`
	Exporter    string  `yaml:"exporter"     mapstructure:"exporter"` // "otlp" | "stdout" | "none"
	Protocol    string  `yaml:"protocol"    mapstructure:"protocol"` // "grpc" or "http"; if empty, inferred from endpoint port (4318 → grpc)
	Endpoint    string  `yaml:"endpoint"     mapstructure:"endpoint"`
	URLPath     string  `yaml:"url_path"    mapstructure:"url_path"` // optional; for HTTP only, e.g. "/otlp/v1/traces" if collector uses a prefix
	ServiceName string  `yaml:"service_name" mapstructure:"service_name"`
	SampleRate  float64 `yaml:"sample_rate"  mapstructure:"sample_rate"`
}

// MetricsConfig configures OTLP or Prometheus metrics exporting.
type MetricsConfig struct {
	Enabled        bool   `yaml:"enabled"         mapstructure:"enabled"`
	Exporter       string `yaml:"exporter"        mapstructure:"exporter"`
	PrometheusPort int    `yaml:"prometheus_port" mapstructure:"prometheus_port"`
	Endpoint       string `yaml:"endpoint"        mapstructure:"endpoint"`
}

// ObservabilityConfig configures the sanitization pipeline.
type ObservabilityConfig struct {
	Enabled   bool            `yaml:"enabled"   mapstructure:"enabled"`
	Sanitizer SanitizerConfig `yaml:"sanitizer" mapstructure:"sanitizer"`
}

// SanitizerConfig controls PII/secret redaction behavior.
type SanitizerConfig struct {
	PIIRedaction        bool `yaml:"pii_redaction"        mapstructure:"pii_redaction"`
	CredentialDetection bool `yaml:"credential_detection" mapstructure:"credential_detection"`
	SecretScrubbing     bool `yaml:"secret_scrubbing"     mapstructure:"secret_scrubbing"`
}

// --- Agent-side configuration (not used by the gateway) ---

// AgentFileConfig is the top-level struct for the agent YAML config file.
// Gateway connection details (RabbitMQ) come from auto-registration, NOT from here.
type AgentFileConfig struct {
	Agent     AgentRuntimeConfig `yaml:"agent"     mapstructure:"agent"`
	Telemetry TelemetryConfig    `yaml:"telemetry" mapstructure:"telemetry"`
}

// AgentRuntimeConfig holds all agent-side settings loaded from agent.yaml (or env vars).
// RabbitMQ config is intentionally absent — it comes from Gateway auto-registration.
type AgentRuntimeConfig struct {
	BasePath  string            `yaml:"base_path"  mapstructure:"base_path"`
	Inference InferenceConfig   `yaml:"inference"  mapstructure:"inference"`
	Sandbox   SandboxConfig     `yaml:"sandbox"    mapstructure:"sandbox"`
	Prompt    PromptConfig      `yaml:"prompt"     mapstructure:"prompt"`
	Tools     AgentToolsConfig  `yaml:"tools"      mapstructure:"tools"`
	S3        S3DefaultConfig   `yaml:"s3"         mapstructure:"s3"`
	MCP                    []MCPServerConfig `yaml:"mcp"        mapstructure:"mcp"`
	MediaEnrichment        MediaEnrichmentConfig `yaml:"media_enrichment" mapstructure:"media_enrichment"`
	MaxSteps               int               `yaml:"max_steps"  mapstructure:"max_steps"`
	Secrets                SecretsConfig     `yaml:"secrets"    mapstructure:"secrets"`
	HeartbeatIntervalSeconds int             `yaml:"heartbeat_interval_seconds" mapstructure:"heartbeat_interval_seconds"` // 0 = disabled, default 1
	Skills                 SkillsConfig     `yaml:"skills"    mapstructure:"skills"`
	Embeddings             EmbeddingsConfig `yaml:"embeddings" mapstructure:"embeddings"`
}

// SkillsConfig configures the skills (plugins) loader.
type SkillsConfig struct {
	Enabled bool   `yaml:"enabled" mapstructure:"enabled"`
	Path    string `yaml:"path"    mapstructure:"path"` // override; default: {base_path}/skills
}

// EmbeddingsConfig configures the embedding-based tool matcher.
// When enabled, an OpenAI-compatible embedding server is used for semantic
// tool matching in lean MCP mode.
type EmbeddingsConfig struct {
	Enabled             bool    `yaml:"enabled"              mapstructure:"enabled"`
	BaseURL             string  `yaml:"base_url"             mapstructure:"base_url"`             // e.g. "http://localhost:11434/v1"; empty = OpenAI default
	Model               string  `yaml:"model"                mapstructure:"model"`                // e.g. "embeddinggemma:300m", "nomic-embed-text"
	APIKey              string  `yaml:"api_key"              mapstructure:"api_key"`              // optional; supports ${ENV_VAR}
	SimilarityThreshold float64 `yaml:"similarity_threshold" mapstructure:"similarity_threshold"` // minimum cosine similarity (0–1); default 0.3
	HybridAlpha         float64 `yaml:"hybrid_alpha"         mapstructure:"hybrid_alpha"`         // blend weight (0 = keyword only, 1 = embedding only); default 0.6
}

// InferenceConfig configures the LLM backend.
type InferenceConfig struct {
	Provider          string  `yaml:"provider"               mapstructure:"provider"`        // "openai" | "ollama"
	Model             string  `yaml:"model"                  mapstructure:"model"`
	BaseURL           string  `yaml:"base_url"                mapstructure:"base_url"`
	APIKey            string  `yaml:"api_key"                mapstructure:"api_key"`
	Temperature       float64 `yaml:"temperature"              mapstructure:"temperature"`
	MaxTokens         int     `yaml:"max_tokens"              mapstructure:"max_tokens"`
	FrequencyPenalty  float64 `yaml:"frequency_penalty"       mapstructure:"frequency_penalty"`  // 0.0–2.0; penalises repeated tokens proportional to frequency (0 = off)
	TimeoutSeconds    int     `yaml:"timeout_seconds"         mapstructure:"timeout_seconds"`    // hard deadline for each LLM HTTP call; 0 = no timeout (default 120)
	// ContextWindowSize is the model context limit in tokens. Used for auto-compaction and usage %.
	// The LLM server may report this via model capabilities (probe); if not, set this explicitly.
	ContextWindowSize int `yaml:"context_window_size" mapstructure:"context_window_size"`
	StreamGenerate    bool     `yaml:"stream_generate"        mapstructure:"stream_generate"`    // use streaming + aggregation for Generate calls (workaround for vLLM tool call bugs)
	StopTokens        []string `yaml:"stop_tokens"            mapstructure:"stop_tokens"`        // optional stop sequences sent to the LLM; useful for local models that leak chat-template tokens (e.g. "<|end|>")
	// LeanMCPTools enables on-demand MCP tool loading. When true, only native tools + search_tools
	// are bound initially. The LLM calls search_tools(intent) to discover relevant MCP tools,
	// then the runtime rebuilds the agent with only the matched tools. Reduces tool schema tokens by ~70%.
	LeanMCPTools bool `yaml:"lean_mcp_tools" mapstructure:"lean_mcp_tools"`
}

// SandboxConfig configures the Docker sandbox for bash execution.
type SandboxConfig struct {
	Enabled        bool              `yaml:"enabled"         mapstructure:"enabled"`
	Image          string            `yaml:"image"           mapstructure:"image"`
	WorkDir        string            `yaml:"work_dir"        mapstructure:"work_dir"`
	MemoryLimitMB  int               `yaml:"memory_limit_mb" mapstructure:"memory_limit_mb"`
	CPULimit       float64           `yaml:"cpu_limit"       mapstructure:"cpu_limit"`
	TimeoutSeconds int               `yaml:"timeout_seconds" mapstructure:"timeout_seconds"`
	NetworkEnabled bool              `yaml:"network_enabled" mapstructure:"network_enabled"`
	VolumeMounts   map[string]string `yaml:"volume_mounts"   mapstructure:"volume_mounts"`
	Env            []string          `yaml:"env"             mapstructure:"env"`
	// SkillsPath is the host path to the skills directory; mounted at /skills in the container (read-only).
	SkillsPath string `yaml:"skills_path" mapstructure:"skills_path"`
	// ReadOnly runs the container root filesystem in read-only mode when true (default). Set false to allow writes outside tmpfs.
	ReadOnly bool `yaml:"read_only" mapstructure:"read_only"`
	// ExtraCapabilities lists additional Linux capabilities to grant the sandbox container
	// beyond the default set (SETUID, SETGID, CHOWN, FOWNER, DAC_OVERRIDE).
	// Example: ["NET_RAW"] to allow raw sockets for nmap/ping.
	ExtraCapabilities []string `yaml:"extra_capabilities" mapstructure:"extra_capabilities"`
}

// PromptConfig holds system prompt and compaction settings.
type PromptConfig struct {
	SystemPrompt                  string `yaml:"system_prompt"                       mapstructure:"system_prompt"`
	CompactionLevel               string `yaml:"compaction_level"                    mapstructure:"compaction_level"`
	BootstrapFile                 string `yaml:"bootstrap_file"                      mapstructure:"bootstrap_file"`
	AutoCompactionThresholdPercent int   `yaml:"auto_compaction_threshold_percent"   mapstructure:"auto_compaction_threshold_percent"` // 0 = disabled; default 60
	CompactKeepLines              int   `yaml:"compact_keep_lines"                   mapstructure:"compact_keep_lines"`               // lines to keep when auto-compacting; default 20
}

// AgentToolsConfig enables or disables individual tools.
type AgentToolsConfig struct {
	WebFetch        bool            `yaml:"web_fetch"   mapstructure:"web_fetch"`
	WebSearch       bool            `yaml:"web_search"  mapstructure:"web_search"`
	Bash            bool            `yaml:"bash"        mapstructure:"bash"`
	DocFetcher      bool            `yaml:"doc_fetcher" mapstructure:"doc_fetcher"`
	Weather         bool            `yaml:"weather"     mapstructure:"weather"`
	Cron            bool            `yaml:"cron"        mapstructure:"cron"`
	WebSearchConfig WebSearchConfig `yaml:"web_search_config" mapstructure:"web_search_config"`
}

// WebSearchConfig holds per-engine settings. Exactly one of Google or DuckDuckGo must be enabled when web_search is used.
type WebSearchConfig struct {
	Google    WebSearchEngineGoogle    `yaml:"google"     mapstructure:"google"`
	DuckDuckGo WebSearchEngineDuckDuckGo `yaml:"duck_duck_go" mapstructure:"duck_duck_go"`
}

// WebSearchEngineGoogle configures Google Custom Search (mutually exclusive with DuckDuckGo).
type WebSearchEngineGoogle struct {
	Enabled      bool   `yaml:"enabled"         mapstructure:"enabled"`
	GoogleAPIKey string `yaml:"google_api_key" mapstructure:"google_api_key"` // ${GOOGLE_SEARCH_API_KEY}
	GoogleCX     string `yaml:"google_cx"      mapstructure:"google_cx"`      // ${GOOGLE_SEARCH_CX} — Custom Search Engine ID
}

// WebSearchEngineDuckDuckGo configures DuckDuckGo HTML search (mutually exclusive with Google).
type WebSearchEngineDuckDuckGo struct {
	Enabled bool `yaml:"enabled" mapstructure:"enabled"`
}

// EffectiveEngine returns the single enabled engine ("google" or "duckduckgo") and true if exactly one is enabled.
// Returns ("", false) if both or neither are enabled (invalid mutually-exclusive state).
func (c *WebSearchConfig) EffectiveEngine() (engine string, ok bool) {
	g, d := c.Google.Enabled, c.DuckDuckGo.Enabled
	if g && d {
		return "", false
	}
	if g {
		return "google", true
	}
	if d {
		return "duckduckgo", true
	}
	return "", false
}

// MCPServerConfig configures a single MCP tool server.
type MCPServerConfig struct {
	Name             string            `yaml:"name"               mapstructure:"name"`
	Transport        string            `yaml:"transport"          mapstructure:"transport"` // "stdio" | "sse" | "streamable"
	Command          string            `yaml:"command"            mapstructure:"command"`
	Args             []string          `yaml:"args"               mapstructure:"args"`
	URL              string            `yaml:"url"                mapstructure:"url"`
	Env              []string          `yaml:"env"                mapstructure:"env"`
	Headers          map[string]string `yaml:"headers"            mapstructure:"headers"`
	KeepAliveSeconds int               `yaml:"keep_alive_seconds" mapstructure:"keep_alive_seconds"` // SSE ping interval; 0 = default 30s
	Auth             *MCPAuthConfig    `yaml:"auth,omitempty"     mapstructure:"auth"`
}

// MCPAuthConfig configures OAuth/OIDC for an MCP SSE or Streamable HTTP connection.
type MCPAuthConfig struct {
	// Type selects the auth method. Currently only "oidc" is supported.
	Type string `yaml:"type" mapstructure:"type"`
	// Flow selects the OAuth flow: "device" (default) or "authorization_code".
	// The device flow (RFC 8628) is designed for headless environments.
	// The authorization_code flow uses PKCE with a localhost redirect and
	// supports broader scopes (e.g. Google Calendar, Gmail).
	Flow string `yaml:"flow" mapstructure:"flow"`
	// ClientID is the OAuth 2.0 client ID.
	ClientID string `yaml:"client_id" mapstructure:"client_id"`
	// ClientSecret is optional (for confidential clients). Supports ${ENV_VAR}.
	ClientSecret string `yaml:"client_secret" mapstructure:"client_secret"`
	// Scopes to request (e.g. ["openid", "email"]).
	Scopes []string `yaml:"scopes" mapstructure:"scopes"`
	// IssuerURL is the OIDC issuer (e.g. "https://accounts.google.com").
	// Used to discover authorization_endpoint, device_authorization_endpoint, and token_endpoint.
	IssuerURL string `yaml:"issuer_url" mapstructure:"issuer_url"`
	// AuthorizationURL overrides the discovered authorization_endpoint (for authorization_code flow).
	AuthorizationURL string `yaml:"authorization_url" mapstructure:"authorization_url"`
	// DeviceAuthURL overrides the discovered device_authorization_endpoint.
	DeviceAuthURL string `yaml:"device_auth_url" mapstructure:"device_auth_url"`
	// TokenURL overrides the discovered token_endpoint.
	TokenURL string `yaml:"token_url" mapstructure:"token_url"`
	// Audience for the token request (some providers like Auth0 require this).
	Audience string `yaml:"audience" mapstructure:"audience"`
}

// SecretsConfig maps secret names to environment variable keys.
type SecretsConfig struct {
	EnvMap map[string]string `yaml:"env_map" mapstructure:"env_map"`
}

// MediaEnrichmentConfig configures the media enrichment pipeline.
type MediaEnrichmentConfig struct {
	Speech MediaEnricherConfig `yaml:"speech" mapstructure:"speech"`
}

// MediaEnricherConfig configures a single media enricher.
type MediaEnricherConfig struct {
	Enabled        bool   `yaml:"enabled"         mapstructure:"enabled"`
	Endpoint       string `yaml:"endpoint"        mapstructure:"endpoint"`        // whisper.cpp server URL
	TimeoutSeconds int    `yaml:"timeout_seconds" mapstructure:"timeout_seconds"` // 0 = default 60s
}

// S3DefaultConfig holds default S3/Minio connection settings for the doc_fetch tool.
// Credentials use ${VAR} syntax and are resolved from env vars at startup.
type S3DefaultConfig struct {
	Endpoint  string `yaml:"endpoint"   mapstructure:"endpoint"`
	Bucket    string `yaml:"bucket"     mapstructure:"bucket"`
	AccessKey string `yaml:"access_key" mapstructure:"access_key"` // ${MINIO_ACCESS_KEY}
	SecretKey string `yaml:"secret_key" mapstructure:"secret_key"` // ${MINIO_SECRET_KEY}
	Region    string `yaml:"region"     mapstructure:"region"`
	UseSSL    bool   `yaml:"use_ssl"    mapstructure:"use_ssl"`
}
