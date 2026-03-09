# Agent Implementation Plan

## Goal

Build the Open-Nipper Go agent using the **Eino SDK** (`react.NewAgent`). The agent consumes `NipperMessage` from RabbitMQ, runs an agentic loop with tools, and publishes `NipperEvent` back. Secrets come from **environment variables** (not 1Password).

## Implementation Status

| Stage | Description | Status |
|-------|-------------|--------|
| **0** | Config + Deps + Registration + CLI | ✅ Complete |
| **1** | LLM Abstraction (OpenAI + Ollama) | ✅ Complete |
| **2** | Agent Loop (ReAct + RabbitMQ) | ✅ Complete |
| **3** | Web Fetch Tool | ✅ Complete |
| **4** | Bash Tool + Container Sandbox | ✅ Complete |
| **5** | Web Search Tool | ✅ Complete |
| **6** | Document Fetcher | ✅ Complete |
| **7** | EXIF Parsing Tool | ❌ Cancelled (absorbed by doc_fetch) |
| **8** | MCP Loader | ✅ Complete |
| **9** | Memory, Commands, Usage Tracking | ✅ Complete |
| **10** | Observability + Polish | ✅ Complete |

## Build Note

Due to a `github.com/bytedance/sonic` + Go 1.24 compatibility issue, build with:
```bash
go build -ldflags="-checklinkname=0" ./...
# or
GOFLAGS='-ldflags=-checklinkname=0' go test ./...
```

## Quick Start

```bash
NIPPER_GATEWAY_URL=http://localhost:18789 \
NIPPER_AUTH_TOKEN=npr_... \
OPENAI_API_KEY=sk-... \
nipper agent
```

## Dependency Map

```
Stage 0 (Config + Deps) ✅
    └─► Stage 1 (LLM Abstraction) ✅
         └─► Stage 2 (Agent Loop — MINIMAL VIABLE AGENT) ✅
              ├─► Stage 3 (Web Fetch Tool) ✅
              ├─► Stage 4 (Bash Tool + Sandbox) ✅
              ├─► Stage 5 (Web Search Tool) ✅
              ├─► Stage 6 (Document Fetcher) ✅
              ├─► Stage 7 (EXIF Tool) ❌ Cancelled — absorbed by doc_fetch
              ├─► Stage 8 (MCP Loader) ✅
              └─► Stage 9 (Memory + Commands + Usage) ✅
                   └─► Stage 10 (Observability + Polish) ✅
```

---

## Stage 0 — Agent Config, Dependencies, and Secret Resolution

**Objective:** Add agent config structs, update `go.mod` with Eino deps, implement env-var secret resolver, create the `nipper agent` CLI subcommand skeleton.

### 0.1 — Agent Config Structs

File: `internal/config/config.go` — add to existing `Config`:

```go
type AgentRuntimeConfig struct {
    // UserID is NOT configured here — it comes from Gateway auto-registration.
    // RabbitMQ config is NOT here — it comes from Gateway auto-registration.

    BasePath     string            `yaml:"base_path"     mapstructure:"base_path"` // ~/.open-nipper
    Inference    InferenceConfig   `yaml:"inference"     mapstructure:"inference"`
    Sandbox      SandboxConfig     `yaml:"sandbox"       mapstructure:"sandbox"`
    Prompt       PromptConfig      `yaml:"prompt"        mapstructure:"prompt"`
    Tools        AgentToolsConfig  `yaml:"tools"         mapstructure:"tools"`
    MCP          []MCPServerConfig `yaml:"mcp"           mapstructure:"mcp"`
    MaxSteps     int               `yaml:"max_steps"     mapstructure:"max_steps"`     // default 25
    Secrets      SecretsConfig     `yaml:"secrets"       mapstructure:"secrets"`
}

type InferenceConfig struct {
    Provider    string  `yaml:"provider"    mapstructure:"provider"`    // "openai" | "local"
    Model       string  `yaml:"model"       mapstructure:"model"`
    BaseURL     string  `yaml:"base_url"    mapstructure:"base_url"`   // for OpenAI-compatible
    APIKey      string  `yaml:"api_key"     mapstructure:"api_key"`    // ${OPENAI_API_KEY}
    Temperature float64 `yaml:"temperature" mapstructure:"temperature"`
    MaxTokens   int     `yaml:"max_tokens"  mapstructure:"max_tokens"`
}

type SandboxConfig struct {
    Enabled        bool              `yaml:"enabled"         mapstructure:"enabled"`
    Image          string            `yaml:"image"           mapstructure:"image"`     // default: ubuntu:noble
    WorkDir        string            `yaml:"work_dir"        mapstructure:"work_dir"`  // /workspace
    MemoryLimitMB  int               `yaml:"memory_limit_mb" mapstructure:"memory_limit_mb"`
    CPULimit       float64           `yaml:"cpu_limit"       mapstructure:"cpu_limit"`
    TimeoutSeconds int               `yaml:"timeout_seconds" mapstructure:"timeout_seconds"`
    NetworkEnabled bool              `yaml:"network_enabled" mapstructure:"network_enabled"`
    VolumeMounts   map[string]string `yaml:"volume_mounts"   mapstructure:"volume_mounts"`
    Env            []string          `yaml:"env"             mapstructure:"env"`
}

type PromptConfig struct {
    SystemPrompt    string `yaml:"system_prompt"    mapstructure:"system_prompt"`
    CompactionLevel string `yaml:"compaction_level" mapstructure:"compaction_level"` // auto|full|standard|compact|minimal
    BootstrapFile   string `yaml:"bootstrap_file"   mapstructure:"bootstrap_file"`
}

type AgentToolsConfig struct {
    WebFetch   bool `yaml:"web_fetch"   mapstructure:"web_fetch"`
    WebSearch  bool `yaml:"web_search"  mapstructure:"web_search"`
    Bash       bool `yaml:"bash"        mapstructure:"bash"`
    EXIF       bool `yaml:"exif"        mapstructure:"exif"`
    DocFetcher bool `yaml:"doc_fetcher" mapstructure:"doc_fetcher"`
}

type MCPServerConfig struct {
    Name      string            `yaml:"name"       mapstructure:"name"`
    Transport string            `yaml:"transport"   mapstructure:"transport"` // "stdio" | "sse"
    Command   string            `yaml:"command"     mapstructure:"command"`   // for stdio
    Args      []string          `yaml:"args"        mapstructure:"args"`
    URL       string            `yaml:"url"         mapstructure:"url"`       // for sse
    Env       []string          `yaml:"env"         mapstructure:"env"`
    Headers   map[string]string `yaml:"headers"     mapstructure:"headers"`
}

type SecretsConfig struct {
    // All secrets resolved from env vars at startup. Map of name → env-var key.
    EnvMap map[string]string `yaml:"env_map" mapstructure:"env_map"`
}
```

### 0.2 — Add Eino Dependencies

```
go get github.com/cloudwego/eino
go get github.com/cloudwego/eino-ext/components/model/openai
go get github.com/cloudwego/eino-ext/components/model/ollama
go get github.com/cloudwego/eino-ext/components/tool/mcp
go get github.com/cloudwego/eino-ext/components/tool/commandline
go get github.com/mark3labs/mcp-go
```

### 0.3 — Env-Var Secret Resolver

File: `internal/agent/secrets/resolver.go`

Replaces 1Password `op` CLI with env-var resolution. Config fields with `${VAR}` syntax are expanded. The resolver:
- Reads `SecretsConfig.EnvMap` at startup
- Resolves each value from `os.Getenv()`
- Returns a `map[string]string` of resolved secrets
- Logs which secret names were resolved (never values)

### 0.4 — Auto-Registration Client

File: `internal/agent/registration/client.go`

The agent **does not configure RabbitMQ directly**. On startup it calls `POST /agents/register` on the Gateway and receives the full RabbitMQ config blob dynamically.

**Required env vars (the ONLY bootstrap config):**
- `NIPPER_GATEWAY_URL` — Gateway base URL (e.g., `http://gateway:18789`)
- `NIPPER_AUTH_TOKEN` — Agent auth token issued during provisioning (`npr_...`)

```go
type RegistrationClient struct {
    gatewayURL string
    authToken  string
    httpClient *http.Client
    logger     *zap.Logger
}

// RegistrationResult mirrors the Gateway's registrationResultBlob (register.go:67-76).
type RegistrationResult struct {
    AgentID  string           `json:"agent_id"`
    UserID   string           `json:"user_id"`
    UserName string           `json:"user_name"`
    RabbitMQ RMQConfig        `json:"rabbitmq"`
    User     UserInfo         `json:"user"`
    Policies PoliciesInfo     `json:"policies"`
}

type RMQConfig struct {
    URL         string       `json:"url"`
    TLSURL      string       `json:"tls_url,omitempty"`
    Username    string       `json:"username"`
    Password    string       `json:"password"`
    VHost       string       `json:"vhost"`
    Queues      QueuesConfig `json:"queues"`
    Exchanges   ExchangesConfig `json:"exchanges"`
    RoutingKeys RoutingKeysConfig `json:"routing_keys"`
}

type QueuesConfig struct {
    Agent   string `json:"agent"`   // e.g. "nipper-agent-user-01"
    Control string `json:"control"` // e.g. "nipper-control-user-01"
}

type ExchangesConfig struct {
    Sessions string `json:"sessions"` // "nipper.sessions"
    Events   string `json:"events"`   // "nipper.events"
    Control  string `json:"control"`  // "nipper.control"
    Logs     string `json:"logs"`     // "nipper.logs"
}

type RoutingKeysConfig struct {
    EventsPublish string `json:"events_publish"` // "nipper.events.{userId}.{sessionId}"
    LogsPublish   string `json:"logs_publish"`   // "nipper.logs.{userId}.{eventType}"
}

type UserInfo struct {
    ID           string         `json:"id"`
    Name         string         `json:"name"`
    DefaultModel string         `json:"default_model"`
    Preferences  map[string]any `json:"preferences"`
}

type PoliciesInfo struct {
    Tools *ToolsPolicy `json:"tools,omitempty"`
}

type ToolsPolicy struct {
    Allow []string `json:"allow"`
    Deny  []string `json:"deny"`
}

func (c *RegistrationClient) Register(ctx context.Context) (*RegistrationResult, error)
```

**Register() flow:**
1. Build `POST {gatewayURL}/agents/register` request
2. Set `Authorization: Bearer {authToken}`
3. Optional JSON body: `{"agent_type":"eino-go","version":"...","hostname":"..."}`
4. Parse response → `RegistrationResult`
5. Retry with exponential backoff on 503 (service unavailable) and 429 (rate limited, respect `Retry-After`)
6. Fatal on 401 (bad token) or 403 (user disabled) — these are not retryable

**The agent uses the returned `RMQConfig` to connect to RabbitMQ** — no RabbitMQ config in the agent's own YAML. The agent config only contains inference settings, tool flags, sandbox config, and MCP server list.

### 0.5 — CLI Subcommand `nipper agent`

File: `cli/agent.go`

```
nipper agent --config agent.yaml
```

Or with env vars only (no config file needed for basic usage):

```
NIPPER_GATEWAY_URL=http://gateway:18789 \
NIPPER_AUTH_TOKEN=npr_a1b2... \
OPENAI_API_KEY=sk-... \
nipper agent
```

Startup flow:
1. Load agent config (inference, tools, sandbox, MCP) from YAML if provided
2. Read `NIPPER_GATEWAY_URL` and `NIPPER_AUTH_TOKEN` from env (required)
3. Initialize logger, telemetry
4. **Call `RegistrationClient.Register()`** → receive RabbitMQ config + user info + policies
5. Connect to RabbitMQ using received credentials (`RMQConfig.URL`, `.Username`, `.Password`, `.VHost`)
6. Start the agent runtime loop (Stage 2)

### Completion Checks

- [x] `AgentRuntimeConfig` struct compiles and is loadable from YAML
- [x] `go mod tidy` succeeds with Eino deps
- [x] `internal/agent/secrets/resolver.go` resolves `${VAR}` from env
- [x] `RegistrationClient.Register()` successfully calls Gateway and parses response
- [x] Registration retries on 503/429, fails fast on 401/403
- [x] Agent connects to RabbitMQ with credentials from registration response
- [x] `nipper agent --help` prints usage
- [x] Unit test: config loads from sample YAML with env-var placeholders
- [x] Unit test: registration client parses mock response correctly
- [x] Unit test: registration client retries on 503 with backoff

> **Status: ✅ COMPLETE** — Gateway logs confirm `agent.registered` with `agentType:eino-go`.

---

## Stage 1 — LLM Abstraction (Eino ChatModel)

**Objective:** Create a factory that returns an `eino model.ChatModel` from `InferenceConfig`. Supports OpenAI and Ollama (LocalAI) endpoints.

### 1.1 — ChatModel Factory

File: `internal/agent/llm/factory.go`

```go
func NewChatModel(ctx context.Context, cfg config.InferenceConfig) (model.ChatModel, error)
```

Logic:
- If `cfg.Provider == "openai"` or `cfg.BaseURL` is set → use `openai.NewChatModel` with `BaseURL` override
- If `cfg.Provider == "ollama"` → use `ollama.NewChatModel`
- Apply `Temperature`, `MaxTokens` from config
- API key from `cfg.APIKey` (already env-expanded by config loader)

### 1.2 — Integration Test

File: `internal/agent/llm/factory_test.go`

- Test with `OPENAI_API_KEY` set → verifies model creation (not invocation)
- Test with ollama config → verifies model creation
- Test missing API key returns error

### Completion Checks

- [x] `NewChatModel()` returns a valid `model.ChatModel` for openai provider
- [x] `NewChatModel()` returns a valid `model.ChatModel` for ollama provider
- [x] `NewChatModel()` with custom `BaseURL` creates model pointing at that URL
- [x] Unit tests pass

> **Status: ✅ COMPLETE** — 5/5 tests pass in `internal/agent/llm`.

---

## Stage 2 — Agent Loop (Eino ReAct Agent + RabbitMQ)

**Objective:** Implement the core agent loop: consume NipperMessage → run Eino ReAct agent → publish NipperEvent. This is the **minimum viable agent** — no tools yet, just LLM chat.

### 2.1 — Agent Runtime

File: `internal/agent/runtime.go`

```go
type Runtime struct {
    cfg       *config.AgentRuntimeConfig
    reg       *registration.RegistrationResult  // from Gateway auto-registration
    chatModel model.ChatModel
    tools     []tool.BaseTool  // empty initially
    broker    *queue.Broker
    sessions  session.SessionStore
    logger    *zap.Logger
}

func NewRuntime(cfg *config.AgentRuntimeConfig, reg *registration.RegistrationResult, ...) (*Runtime, error)
func (r *Runtime) Run(ctx context.Context) error  // main consume loop
func (r *Runtime) handleMessage(ctx context.Context, msg *models.NipperMessage) error
```

`NewRuntime()`:
- Receives `RegistrationResult` from Stage 0.4 auto-registration
- Uses `reg.RabbitMQ.*` for all queue/exchange names (not hardcoded)
- Uses `reg.User.ID` as the user ID for sessions/memory paths
- Uses `reg.Policies.Tools` for tool policy enforcement

`Run()`:
1. Connect to RabbitMQ using `reg.RabbitMQ.URL`, `.Username`, `.Password`, `.VHost`
2. Consume from `reg.RabbitMQ.Queues.Agent` (e.g., `nipper-agent-user-01`) with prefetch=1
3. Unmarshal `QueueItem` → extract `NipperMessage`
4. Call `handleMessage()`
5. Ack on success, nack on failure
6. On RabbitMQ disconnect: reconnect with backoff, re-register if credentials expired

`handleMessage()`:
1. Load/create session via `SessionStore` (using `msg.SessionKey`)
2. Load transcript → build `[]*schema.Message` for Eino
3. Build Eino `react.Agent` (or reuse cached agent)
4. Call agent with user message
5. Collect response
6. Append to transcript
7. Publish `NipperEvent{Type: "done", Delta: {Text: response}}` to `reg.RabbitMQ.Exchanges.Events`
   with routing key from `reg.RabbitMQ.RoutingKeys.EventsPublish` (template: replace `{sessionId}`)

### 2.2 — Message Conversion

File: `internal/agent/convert.go`

Convert between `NipperMessage`/`TranscriptLine` and Eino `*schema.Message`:
- `TranscriptLinesToEinoMessages(lines []session.TranscriptLine) []*schema.Message`
- `EinoMessageToTranscriptLine(msg *schema.Message, runID string) session.TranscriptLine`
- `NipperMessageToEinoMessage(msg *models.NipperMessage) *schema.Message`

### 2.3 — Event Publisher

File: `internal/agent/publisher.go`

Wraps RabbitMQ channel for agent-side event emission. Uses values from `RegistrationResult`:
- `PublishEvent(ctx, event *models.NipperEvent) error`
- Exchange: `reg.RabbitMQ.Exchanges.Events` (e.g., `nipper.events`)
- Routing key: derived from `reg.RabbitMQ.RoutingKeys.EventsPublish` template
  - Template: `nipper.events.{userId}.{sessionId}` → replace `{sessionId}` with actual session ID
  - `{userId}` is already baked into the template by the Gateway

### 2.4 — Wire It Up in CLI

Update `cli/agent.go` — the full startup sequence:

```
1. Load agent config from YAML (inference, tools, sandbox, MCP settings)
2. Read NIPPER_GATEWAY_URL and NIPPER_AUTH_TOKEN from env (required, fatal if missing)
3. Initialize logger (zap, JSON output)
4. Initialize telemetry (OTel, noop if not configured)
5. Create RegistrationClient(gatewayURL, authToken)
6. Call reg.Register(ctx) → RegistrationResult
   - On 401/403: fatal ("invalid token" or "user disabled")
   - On 503/429: retry with backoff
   - On success: log agent_id, user_id, queue names
7. Create ChatModel via llm.NewChatModel(ctx, cfg.Inference)
8. Create SessionStore(cfg.BasePath or "~/.open-nipper", logger)
   - Uses reg.User.ID for user-scoped paths
9. Connect to RabbitMQ using reg.RabbitMQ config:
   - URL: reg.RabbitMQ.URL (or .TLSURL if available)
   - Credentials: reg.RabbitMQ.Username, reg.RabbitMQ.Password
   - VHost: reg.RabbitMQ.VHost
10. Create Runtime(cfg, reg, chatModel, sessionStore, broker, logger)
11. runtime.Run(ctx) — blocks until SIGINT/SIGTERM
12. Graceful shutdown: close broker, cleanup sandbox, flush logger
```

### 2.5 — Agent Config Example

File: `agent.example.yaml`

**NOTE:** RabbitMQ config is NOT in this file. It comes from Gateway auto-registration.
The agent only needs `NIPPER_GATEWAY_URL` and `NIPPER_AUTH_TOKEN` env vars to bootstrap.

```yaml
# Agent-side settings only. RabbitMQ config comes from Gateway registration.
agent:
  base_path: "${HOME}/.open-nipper"
  inference:
    provider: "openai"
    model: "gpt-4o"
    api_key: "${OPENAI_API_KEY}"
    base_url: ""                 # empty = api.openai.com; set for LocalAI/vLLM
    temperature: 0.7
    max_tokens: 4096
  max_steps: 25
  prompt:
    system_prompt: "You are a helpful assistant."
    compaction_level: "auto"
  tools:
    web_fetch: true
    web_search: false
    bash: false
    exif: false
    doc_fetcher: false
  sandbox:
    enabled: false
    image: "ubuntu:noble"
    work_dir: "/workspace"
    memory_limit_mb: 2048
    cpu_limit: 2.0
    timeout_seconds: 120
    network_enabled: false
    volume_mounts: {}
    env: ["DEBIAN_FRONTEND=noninteractive"]
  mcp: []
  secrets:
    env_map: {}

# Telemetry (optional, independent of Gateway telemetry)
telemetry:
  tracing:
    enabled: false
    exporter: "otlp"
    endpoint: "${OTEL_EXPORTER_OTLP_ENDPOINT}"
    service_name: "open-nipper-agent"
    sample_rate: 1.0
  metrics:
    enabled: false
    exporter: "prometheus"
    prometheus_port: 9091
```

**Minimal startup (env vars only, no config file):**
```bash
NIPPER_GATEWAY_URL=http://gateway:18789 \
NIPPER_AUTH_TOKEN=npr_a1b2c3d4... \
OPENAI_API_KEY=sk-... \
nipper agent
```

**With config file:**
```bash
NIPPER_GATEWAY_URL=http://gateway:18789 \
NIPPER_AUTH_TOKEN=npr_a1b2c3d4... \
nipper agent --config agent.yaml
```

### Completion Checks

- [x] Agent starts with only `NIPPER_GATEWAY_URL` + `NIPPER_AUTH_TOKEN` env vars
- [x] Agent calls `POST /agents/register` on Gateway at startup
- [x] Agent receives RabbitMQ credentials, queue names, exchanges from registration
- [x] Agent connects to RabbitMQ using registration-provided credentials
- [x] Agent consumes from `reg.RabbitMQ.Queues.Agent` (e.g., `nipper-agent-user-01`)
- [ ] Send a test message via RabbitMQ → agent responds via LLM *(requires live LLM API key)*
- [x] Response published as `NipperEvent` to `reg.RabbitMQ.Exchanges.Events`
- [x] Routing key uses template from `reg.RabbitMQ.RoutingKeys.EventsPublish`
- [x] Tool policy enforcement uses `reg.Policies.Tools` (allow/deny lists)
- [x] Transcript persisted to JSONL file under `~/.open-nipper/users/{reg.User.ID}/sessions/`
- [x] Session metadata created/updated
- [x] Graceful shutdown on SIGINT/SIGTERM
- [x] Works with OpenAI API key
- [x] Works with local Ollama endpoint (if running)
- [ ] Re-registration on RabbitMQ connection loss *(Stage 10 polish)*

> **Status: ✅ COMPLETE** — Agent registers, connects to RabbitMQ, consumes from correct queue,
> publishes events to correct exchange with routing key template. Transcript + session metadata
> persisted to `~/.open-nipper`. Graceful shutdown via SIGTERM confirmed.

---

## Stage 3 — Web Fetch Tool

**Objective:** First tool — HTTP fetch + HTML→text conversion. After this stage, the agent can browse the web.

### 3.1 — Tool Implementation

File: `internal/agent/tools/webfetch.go`

```go
type WebFetchParams struct {
    URL     string            `json:"url"     jsonschema:"description=URL to fetch"`
    Headers map[string]string `json:"headers" jsonschema:"description=Optional HTTP headers"`
}

type WebFetchResult struct {
    StatusCode int    `json:"status_code"`
    Body       string `json:"body"`
    Title      string `json:"title"`
    URL        string `json:"url"`
}
```

Implementation:
- HTTP GET with configurable timeout (30s default)
- Follow redirects (max 5)
- User-Agent header
- HTML → readable text using `github.com/go-shiori/go-readability` or simple HTML stripping
- Truncate body to 100KB
- Return as `WebFetchResult`

Register as Eino tool using `utils.InferTool()`.

### 3.2 — Tool Registration

File: `internal/agent/tools/registry.go`

```go
func BuildTools(ctx context.Context, cfg *config.AgentRuntimeConfig) ([]tool.BaseTool, error)
```

Reads `cfg.Tools` flags and creates enabled tools. Returns slice for `compose.ToolsNodeConfig`.

### 3.3 — Integrate into Agent Loop

Update `internal/agent/runtime.go`:
- Call `tools.BuildTools()` at init
- Pass tools to `react.NewAgent` via `ToolsConfig`

### Completion Checks

- [x] Agent can receive "fetch https://example.com and summarize it"
- [x] Agent calls web_fetch tool, gets page content
- [x] Agent summarizes the content in its response
- [x] Tool call emitted as `NipperEvent{Type: "tool_start"}` and `tool_end`
- [x] HTML is converted to readable text (not raw HTML) via go-readability
- [x] Timeout and error handling work
- [x] Unit test: tool returns content for known URL

> **Status: ✅ COMPLETE** — 8/8 tests pass in `internal/agent/tools`. Tool events emitted via
> Eino `react.BuildAgentCallback` + `ToolCallbackHandler`. `tools.web_fetch: true` in agent.yaml
> enables the tool; policy allow/deny enforced at build time.

---

## Stage 4 — Bash Tool + Container Sandbox

**Objective:** Shell command execution inside a Docker sandbox container.

### 4.1 — Sandbox Manager

File: `internal/agent/sandbox/manager.go`

Wraps `github.com/cloudwego/eino-ext/components/tool/commandline/sandbox`:

```go
type Manager struct {
    sandbox *sandbox.DockerSandbox
    cfg     config.SandboxConfig
}

func NewManager(ctx context.Context, cfg config.SandboxConfig) (*Manager, error)
func (m *Manager) Create(ctx context.Context) error
func (m *Manager) Cleanup(ctx context.Context)
func (m *Manager) Operator() sandbox.Operator
```

Config mapping:
- `cfg.Image` → `sandbox.Config.Image` (default: `ubuntu:noble`)
- `cfg.WorkDir` → `sandbox.Config.WorkDir`
- `cfg.MemoryLimitMB * 1024 * 1024` → `sandbox.Config.MemoryLimit`
- `cfg.CPULimit` → `sandbox.Config.CPULimit`
- `cfg.TimeoutSeconds` → `sandbox.Config.Timeout`
- `cfg.NetworkEnabled` → `sandbox.Config.NetworkEnabled`
- `cfg.VolumeMounts` → `sandbox.Config.VolumeBindings`
- `cfg.Env` → `sandbox.Config.Env`

### 4.2 — Bash Tool

File: `internal/agent/tools/bash.go`

```go
type BashParams struct {
    Command string `json:"command" jsonschema:"description=Bash command to execute"`
    WorkDir string `json:"work_dir,omitempty" jsonschema:"description=Working directory"`
    Timeout int    `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds (default 120)"`
}

type BashResult struct {
    Stdout   string `json:"stdout"`
    Stderr   string `json:"stderr"`
    ExitCode int    `json:"exit_code"`
}
```

- If sandbox enabled → execute via `sandbox.Operator`
- If sandbox disabled → execute via `os/exec` locally (dev mode)
- Truncate stdout/stderr to 50KB each
- Enforce timeout

### 4.3 — Lifecycle Integration

Update `cli/agent.go`:
- If `sandbox.enabled` → create sandbox container on startup, cleanup on shutdown
- Pass `sandbox.Operator` to bash tool

### Completion Checks

- [x] `nipper agent` with `sandbox.enabled: true` creates Docker container
- [x] Agent can execute `ls -la` via bash tool
- [x] Container uses configured image, memory, CPU limits
- [x] Volume mounts work (host dir → container /workspace)
- [x] Sandbox cleanup on agent shutdown
- [x] Without sandbox → local exec works (dev mode)
- [x] Timeout kills long-running commands
- [x] Unit test: bash tool returns expected output
- [x] Command validation: blocklist prevents destructive operations (rm -rf /, mkfs, etc.)
- [x] Security directive in system prompt for defense in depth
- [x] Docker container: read-only root, dropped capabilities, no-new-privileges, pids-limit

> **Status: ✅ COMPLETE** — 20/20 tests pass in `internal/agent/tools` + `internal/agent/sandbox`.
> Bash tool runs commands locally (dev mode) or in Docker sandbox (production mode).
> Command blocklist + security system prompt enforced. Container uses read-only rootfs,
> dropped capabilities, memory/CPU limits, and network isolation by default.

---

## Stage 5 — Web Search Tool (DuckDuckGo + Google)

**Objective:** Search the web and return results.

### 5.1 — DuckDuckGo Search

File: `internal/agent/tools/websearch.go`

```go
type WebSearchParams struct {
    Query   string `json:"query"    jsonschema:"description=Search query"`
    Engine  string `json:"engine"   jsonschema:"description=Search engine: duckduckgo or google (default duckduckgo)"`
    MaxResults int `json:"max_results,omitempty" jsonschema:"description=Max results (default 5)"`
}

type SearchResult struct {
    Title   string `json:"title"`
    URL     string `json:"url"`
    Snippet string `json:"snippet"`
}
```

Implementation:
- DuckDuckGo: use `https://html.duckduckgo.com/html/?q=` + parse results
- Google: use Custom Search JSON API with `${GOOGLE_SEARCH_API_KEY}` and `${GOOGLE_SEARCH_CX}`
- Default to DuckDuckGo (no API key required)

### Completion Checks

- [x] Agent can search DuckDuckGo and return results
- [x] Agent can search Google (when API key configured)
- [x] Results contain title, URL, snippet
- [x] Rate limiting / error handling (query validation, timeout, max results clamping)
- [x] Unit test with mock HTTP responses (18 tests covering DDG, Google, validation, helpers)
- [x] Security directive in system prompt for defense in depth
- [x] Tool policy enforcement (allow/deny) via registry

> **Status: ✅ COMPLETE** — 18 new tests pass in `internal/agent/tools`. Web search supports
> DuckDuckGo (no API key, default) and Google Custom Search JSON API (requires
> `GOOGLE_SEARCH_API_KEY` + `GOOGLE_SEARCH_CX`). Query validation, result capping,
> timeout handling, and system prompt security directive for defense in depth.

---

## Stage 6 — Document Fetcher (HTTP + S3)

**Objective:** Fetch documents from HTTP URLs and S3/Minio buckets. Parse PDF, DOCX, plain text.

### 6.1 — HTTP Document Fetcher

File: `internal/agent/tools/docfetch.go`

```go
type DocFetchParams struct {
    URL      string `json:"url"       jsonschema:"description=HTTP URL or s3:// URI"`
    S3Config *S3Cfg `json:"s3_config" jsonschema:"description=S3 config override (optional)"`
}

type S3Cfg struct {
    Endpoint  string `json:"endpoint"`
    AccessKey string `json:"access_key"`
    SecretKey string `json:"secret_key"`
    Bucket    string `json:"bucket"`
    Region    string `json:"region"`
    UseSSL    bool   `json:"use_ssl"`
}
```

Implementation:
- HTTP: download file, detect MIME type
- S3: use `github.com/minio/minio-go/v7` with configurable endpoint (Minio support)
- Parse:
  - PDF → text (use `github.com/ledongthuc/pdf` or `pdfcpu`)
  - Plain text / Markdown → return as-is
  - HTML → readability extraction (reuse webfetch logic)
- Truncate to 200KB
- S3 config from agent config or per-call override

### 6.2 — Agent Config for S3

Add to `AgentRuntimeConfig`:
```go
type S3DefaultConfig struct {
    Endpoint  string `yaml:"endpoint"   mapstructure:"endpoint"`
    AccessKey string `yaml:"access_key" mapstructure:"access_key"` // ${MINIO_ACCESS_KEY}
    SecretKey string `yaml:"secret_key" mapstructure:"secret_key"` // ${MINIO_SECRET_KEY}
    Region    string `yaml:"region"     mapstructure:"region"`
    UseSSL    bool   `yaml:"use_ssl"    mapstructure:"use_ssl"`
}
```

### 6.3 — WhatsApp Media S3 Integration

File: `internal/agent/convert.go`

Enhanced `NipperMessageToEinoMessage()` to annotate user messages with media context
when content parts have S3/HTTP URLs (e.g. from WhatsApp media uploads via Wuzapi).
The annotation format: `[Attached {type} ({mime}) — available at {url}]`

This enables the LLM to see attached media URLs and call `doc_fetch` to retrieve them.

### Completion Checks

- [x] Fetch PDF from HTTP URL → return text content (via `ledongthuc/pdf`)
- [x] Fetch file from S3/Minio → return content (via `minio-go/v7`)
- [x] Configurable S3 backend (Minio endpoint, credentials from agent config)
- [x] MIME type detection (Content-Type header + file extension fallback)
- [x] Error handling for unsupported formats (returns metadata)
- [x] Binary media (images, audio, video) returns metadata only
- [x] HTML → readability extraction (reuses webfetch logic)
- [x] Plain text, Markdown, JSON, YAML, XML → returned as-is
- [x] Content truncated to 200KB
- [x] URL safety: rejects file://, ftp://, private IPs, localhost, .internal, .local
- [x] S3 safety: validates bucket names, rejects path traversal, read-only access
- [x] S3 credentials from config only, not from LLM params
- [x] System prompt security directive for doc_fetch (defense in depth)
- [x] Tool policy enforcement (allow/deny) via registry
- [x] WhatsApp media context: NipperMessageToEinoMessage annotates S3 URLs from media parts
- [x] 22 unit tests for HTTP fetch, MIME handling, security, S3, policy, truncation
- [x] 12 unit tests for message conversion with media annotations

> **Status: ✅ COMPLETE** — 34 new tests pass across `internal/agent/tools` and `internal/agent`.
> doc_fetch supports HTTP/HTTPS + S3/Minio. PDF text extraction, HTML readability,
> plain text, Markdown, JSON, YAML, XML. Binary media returns metadata only.
> WhatsApp media S3 URLs annotated in user messages for LLM visibility.
> Defense in depth: URL safety validation, S3 read-only, system prompt directive.

---

## Stage 7 — EXIF Parsing Tool

**Status: ❌ CANCELLED — Absorbed by doc_fetch**

The standalone EXIF tool is not needed. The `doc_fetch` tool (Stage 6) already includes full EXIF extraction via `extractEXIF()` in `docfetch.go`, using `github.com/rwcarlsen/goexif/exif`. When `doc_fetch` processes an image, it automatically returns:
- Camera make/model, lens info
- GPS coordinates with Google Maps link
- Date/time, exposure, ISO, focal length
- Image dimensions, orientation, software

The system prompt and `convert.go` media annotations instruct the LLM to call `doc_fetch` on any attached image URL for EXIF data. No separate tool is required.

---

## Stage 8 — MCP Loader

**Objective:** Load tools from external MCP servers (STDIO and SSE transports), configured via JSON similar to Cursor's format.

### 8.1 — MCP Client Manager

File: `internal/agent/mcp/loader.go`

```go
type Loader struct {
    clients []mcpClient
    tools   []tool.BaseTool
}

func NewLoader(ctx context.Context, configs []config.MCPServerConfig) (*Loader, error)
func (l *Loader) Tools() []tool.BaseTool
func (l *Loader) Close()
```

Per config entry:
- `transport: "stdio"` → `client.NewStdioMCPClient(command, env, args...)`
- `transport: "sse"` → `client.NewSSEMCPClient(url)`
  - Apply `headers` for auth
- Initialize client, call `mcp.GetTools(ctx, &mcp.Config{Cli: cli})`
- Merge all tools into flat list

### 8.2 — Config Format

```yaml
mcp:
  - name: "github"
    transport: "stdio"
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-github"]
    env: ["GITHUB_TOKEN=${GITHUB_TOKEN}"]
  - name: "custom-api"
    transport: "sse"
    url: "https://mcp.example.com/sse"
    headers:
      Authorization: "Bearer ${MCP_AUTH_TOKEN}"
```

### 8.3 — Security Directives

System prompt includes MCP-specific safety rules (defense in depth):
- MCP tool output treated as untrusted data
- No following instructions embedded in MCP responses
- No passing credentials/PII to MCP tools
- No destructive operations via MCP tools
- User informed about MCP tool calls

### 8.4 — OAuth2 Documentation

File: `MCP_NEXT_STEPS.md`

Documents phased OAuth2 integration plan:
1. Pre-obtained tokens via env vars (current — works today)
2. Client Credentials flow (automated, machine-to-machine)
3. Device Code flow (interactive, user authorization)
4. Authorization Code + PKCE (web-based, complex)

### Completion Checks

- [x] Load tools from SSE MCP server (via `mark3labs/mcp-go` SSE client)
- [x] Load tools from STDIO MCP server (via `mark3labs/mcp-go` STDIO client)
- [x] Pass env vars to STDIO MCP clients (`env` field with `${VAR}` expansion)
- [x] Pass headers to SSE MCP clients (`headers` field with `${VAR}` expansion)
- [x] `${VAR}` syntax expanded from environment in all MCP config fields (URL, command, args, env, headers)
- [x] Tools available in agent loop via Eino `react.NewAgent` ToolsConfig
- [x] MCP tool names listed in system prompt hints
- [x] MCP security directive in system prompt (defense in depth)
- [x] Graceful cleanup on shutdown (`mcpLoader.Close()` via defer)
- [x] Config validation: name, transport, url/command required fields enforced
- [x] OAuth2 integration documented in `MCP_NEXT_STEPS.md`
- [x] 8 unit tests for config validation, env var expansion, nil safety, error handling
- [x] All existing tests pass (full suite: 0 failures)

> **Status: ✅ COMPLETE** — MCP loader supports SSE and STDIO transports with env var
> expansion in all config fields. Tools from external MCP servers are loaded at startup
> and integrated into the Eino ReAct agent. Auth headers supported for SSE transport.
> Defense-in-depth via MCP security directive in system prompt. OAuth2 phased plan
> documented in MCP_NEXT_STEPS.md. 8 new tests pass in `internal/agent/mcp`.

---

## Stage 9 — Memory, Commands, Usage Tracking, and Security

**Objective:** Implement durable memory (file-based), agent slash commands (initialize sessions, help, usage, persona), context window usage tracking with LLM cost estimation, and defense-in-depth security hardening.

### 9.1 — Durable Memory Store

File: `internal/agent/memory/store.go`

```go
type Store struct {
    memoryDir string // ~/.open-nipper/users/{userId}/memory/
    logger    *zap.Logger
}

func NewStore(basePath, userID string, logger *zap.Logger) *Store
func (s *Store) Write(content string) error                      // append to today's file
func (s *Store) Read(query string, days int) ([]Entry, error)    // search memory
func (s *Store) Inject(days int, maxBytes int) string            // for system prompt injection
```

Memory files are stored as daily Markdown at `{basePath}/users/{userId}/memory/YYYY-MM-DD.md`.
Each entry is timestamped: `- [HH:MM:SSZ] content`.

### 9.2 — Memory Tools

File: `internal/agent/tools/memory.go`

- `memory_write` tool → `Store.Write()` — saves facts, preferences, notes to durable memory
- `memory_read` tool → `Store.Read()` — searches past memories with optional query filter

Both tools registered via `toolutils.InferTool()` in `tools/registry.go`.

### 9.3 — Agent Slash Commands

File: `internal/agent/commands.go`

Commands are intercepted before LLM invocation. They do not consume tokens.

| Command | Action |
|---------|--------|
| `/help` | Print available commands |
| `/new` | Archive session, clear transcript, re-initialize context |
| `/reset` | Alias for `/new` |
| `/usage` | Print context window usage %, token counts, estimated LLM costs |
| `/compact` | Force transcript compaction |
| `/status` | Show session info (model, messages, compactions, etc.) |
| `/persona <text>` | Set/change the agent persona for this session |

### 9.4 — Usage Tracking

File: `internal/agent/usage.go`

Per-session cumulative tracking of:
- Input/output token counts
- Number of LLM requests
- Context window usage percentage
- Estimated cost in USD (from model-specific pricing table)

Cost table covers OpenAI (GPT-4o, GPT-4, GPT-3.5), Anthropic (Claude Sonnet/Haiku/Opus), Google (Gemini), Mistral, and o1/o3 models. Local/unknown models assume zero cost.

### 9.5 — System Prompt Security Hardening

The system prompt now includes:
- **Global Safety Preamble**: 8 mandatory rules preventing destructive operations, credential exfiltration, prompt injection from fetched content, and sandbox escape.
- **Per-tool security directives**: bash, doc_fetch, web_search each have their own safety rules.
- **Commands reference**: so the LLM knows about available slash commands.
- **Memory injection**: recent durable memory entries are included in the system prompt.
- **Per-session persona**: the `/persona` command overrides the base system prompt.

### 9.6 — Transcript Compaction

Compaction was already implemented in `pkg/session/compaction.go` (from Stage 2). The `/compact` command exposes it to users, and it's available for programmatic use. The compactor:
- Archives oldest messages to the archive directory (no data loss)
- Rewrites the active transcript atomically
- Updates session metadata with compaction count

### 9.7 — EXIF Tool Removal

The standalone EXIF tool (Stage 7) was determined to be unnecessary. The `doc_fetch` tool already handles EXIF extraction via `extractEXIF()` in `docfetch.go`. The `EXIF` config flag was removed from `AgentToolsConfig` and replaced with `Memory`.

### Completion Checks

- [x] `memory_write` persists to `~/.open-nipper/users/{userId}/memory/YYYY-MM-DD.md`
- [x] `memory_read` searches across memory files with optional query filter
- [x] Memory injected into system prompt via `Store.Inject()`
- [x] `/help` command lists all available commands
- [x] `/new` archives session transcript and re-initializes context
- [x] `/reset` is an alias for `/new`
- [x] `/usage` prints context window usage %, token counts, and estimated costs
- [x] `/compact` forces transcript compaction
- [x] `/status` shows session information
- [x] `/persona` sets per-session agent persona
- [x] Usage tracker records cumulative tokens and cost per session
- [x] Usage tracker resets on `/new` session
- [x] Cost estimation for common models (GPT-4o, Claude, Gemini, etc.)
- [x] Global safety preamble in system prompt
- [x] Defense-in-depth: per-tool security directives
- [x] EXIF config flag removed; replaced with Memory
- [x] 11 unit tests for memory store (write, read, inject, filter, truncation)
- [x] 12 unit tests for usage tracker (record, reset, format, cost estimation)
- [x] 14 unit tests for commands (help, new, reset, usage, status, persona, compact, unknown, case)
- [x] 6 unit tests for memory tools (write, read, validation)
- [x] All existing tests pass (full suite: 0 failures)

> **Status: ✅ COMPLETE** — Durable memory, slash commands, usage tracking, and security
> hardening implemented. 43 new tests pass. EXIF tool cancelled (absorbed by doc_fetch).
> `/new` command archives and re-initializes sessions. `/usage` shows context window %
> and cumulative LLM costs. Defense-in-depth via global safety preamble + per-tool directives.

---

## Stage 10 — Observability, Events, and Polish

**Objective:** Emit structured NipperEvents for tool calls, thinking, errors. Add OTel tracing/metrics to agent. Final polish.

### 10.1 — Rich Event Emission

Update `handleMessage()` to emit:
- `tool_start` when tool call begins
- `tool_end` when tool call completes
- `thinking` for model reasoning (if available)
- `error` on failures
- `done` with `contextUsage` on completion make sure to add percentage of context used.

### 10.2 — OTel Integration

- Reuse `internal/telemetry` package from gateway
- Span per message processing
- Span per tool call
- Span per LLM Call.
- Metrics: messages_processed, tool_calls, latency, context_usage, contex_usage per user, rabbitmq queue len, LLM API LATENCY AND ERRORS.

### 10.3 — Graceful Error Handling

- LLM API errors → retry with backoff (3 attempts)
- Tool errors → return error to agent as tool result
- RabbitMQ disconnect → reconnect with backoff
- Context overflow → trigger compaction, retry

### 10.4 — Session Commands

Handle `/reset`, `/compact`, `/status`, `/model` in message text before passing to LLM.

### Completion Checks

- [x] All NipperEvent types emitted correctly (tool_start, tool_end, thinking, error, done with contextUsage)
- [x] done event includes contextUsage (inputTokens, outputTokens, contextWindow, usagePercent)
- [x] OTel: agent loads telemetry from config, expands env vars; noop when endpoints not configured (no log noise)
- [x] Retries work for transient LLM errors (3 attempts with backoff)
- [x] Tool errors emit error event and return error as tool result
- [x] RabbitMQ disconnect: CLI re-registers and re-dials in a loop
- [x] `/reset` creates new session; `/status` returns context usage; `/compact` forces compaction
- [x] Global safety preamble strengthened (no destructive behavior even if user insists)

---

## Updated Architecture: Key Changes from AGENT_ARCHITECTURE.md

The following changes must be reflected in `AGENT_ARCHITECTURE.md`:

1. **Eino SDK** — Agent uses `github.com/cloudwego/eino` `react.NewAgent` for the agentic loop, not a custom loop.
2. **Go-only** — This reference agent is Go. The polyglot principle remains (other agents can exist in any language), but the primary implementation is Go+Eino.
3. **Env-var secrets** — Replace 1Password `op` CLI with env-var resolution (`${VAR}` syntax in config). 1Password remains an option for operators who mount `op` in the container, but is not the default.
4. **Container sandbox** — Uses `eino-ext/components/tool/commandline/sandbox.DockerSandbox` for bash execution. Configurable image, mounts, resource limits.
5. **MCP support** — Agent can load external tools from MCP servers (STDIO + SSE), configured via YAML similar to Cursor's JSON format.
6. **Tool framework** — All tools implement Eino's `tool.BaseTool` interface via `utils.InferTool()` for automatic schema generation.

## Agent Startup Sequence (Full)

This is the authoritative startup sequence. The agent auto-registers with the Gateway:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        AGENT STARTUP SEQUENCE                               │
│                                                                             │
│  ENV: NIPPER_GATEWAY_URL + NIPPER_AUTH_TOKEN (required)                     │
│  ENV: OPENAI_API_KEY or equivalent (for inference)                          │
│  OPT: --config agent.yaml (for tools, sandbox, MCP, prompt settings)        │
│                                                                             │
│  1. Load agent.yaml config (if provided)                                    │
│  2. Initialize logger (zap, JSON)                                           │
│  3. Initialize telemetry (noop if not configured)                           │
│                                                                             │
│  4. AUTO-REGISTER WITH GATEWAY                                              │
│     POST {NIPPER_GATEWAY_URL}/agents/register                               │
│     Authorization: Bearer {NIPPER_AUTH_TOKEN}                               │
│     Body: {"agent_type":"eino-go","version":"..."}                          │
│                                                                             │
│     ├── 200 OK → Parse RegistrationResult                                   │
│     │   Contains:                                                           │
│     │     • agent_id, user_id, user_name                                    │
│     │     • rabbitmq.url, .username, .password, .vhost                      │
│     │     • rabbitmq.queues.agent ("nipper-agent-user-01")                  │
│     │     • rabbitmq.queues.control ("nipper-control-user-01")              │
│     │     • rabbitmq.exchanges.events ("nipper.events")                     │
│     │     • rabbitmq.routing_keys.events_publish template                   │
│     │     • user.default_model, user.preferences                            │
│     │     • policies.tools (allow/deny lists)                               │
│     │                                                                       │
│     ├── 401 → FATAL: invalid token or revoked agent                         │
│     ├── 403 → FATAL: user disabled                                          │
│     ├── 429 → RETRY: respect Retry-After header                             │
│     └── 503 → RETRY: Gateway not ready, exponential backoff                 │
│                                                                             │
│  5. Create ChatModel via llm.NewChatModel(ctx, cfg.Inference)               │
│     - Uses cfg.Inference.Provider, .Model, .APIKey, .BaseURL                │
│     - NOT from registration — inference is agent-side config                 │
│                                                                             │
│  6. Build tools via tools.BuildTools(ctx, cfg, reg.Policies.Tools)          │
│     - Tool policy (allow/deny) comes from registration response             │
│     - Tool config (enabled flags) comes from agent.yaml                     │
│                                                                             │
│  7. Load MCP tools via mcp.NewLoader(ctx, cfg.MCP)                          │
│                                                                             │
│  8. Create sandbox (if cfg.Sandbox.Enabled)                                 │
│                                                                             │
│  9. Create SessionStore(cfg.BasePath, reg.User.ID, logger)                  │
│     - base_path defaults to ${HOME}/.open-nipper (expanded at load time)    │
│     - Sessions at {basePath}/users/{reg.User.ID}/sessions/                   │
│     - Memory at {basePath}/users/{reg.User.ID}/memory/                      │
│                                                                             │
│  10. Reconnect loop: register → connect to RabbitMQ → runtime.Run():        │
│      amqp://{reg.RabbitMQ.Username}:{reg.RabbitMQ.Password}                 │
│        @{host from reg.RabbitMQ.URL}/{reg.RabbitMQ.VHost}                   │
│                                                                             │
│  11. Create Runtime(cfg, reg, chatModel, tools, sessionStore, broker)       │
│                                                                             │
│  12. runtime.Run(ctx, conn) — consume loop:                                 │
│      - Consume from reg.RabbitMQ.Queues.Agent with prefetch=1               │
│      - For each message: handleMessage() → Eino ReAct agent → publish event │
│      - Publish to reg.RabbitMQ.Exchanges.Events with routing key template  │
│      - When Run returns (e.g. delivery channel closed): close conn,          │
│        backoff, re-register, reconnect, and loop from step 10               │
│                                                                             │
│  13. SIGINT/SIGTERM → graceful shutdown:                                    │
│      - Stop consuming                                                       │
│      - Cleanup sandbox container                                            │
│      - Close MCP clients                                                    │
│      - Close broker connection                                              │
│      - Flush logger                                                         │
│      - Shutdown telemetry                                                   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## File Structure (New Files)

```
internal/agent/
├── runtime.go              # Core agent loop (Stage 2) + commands/memory/usage integration (Stage 9)
├── convert.go              # Message conversion (Stage 2)
├── publisher.go            # Event publisher (Stage 2)
├── commands.go             # Slash command handler (Stage 9)
├── usage.go                # Usage tracking + cost estimation (Stage 9)
├── multimodal_user.go      # Vision/inline image support (Stage 6)
├── registration/
│   ├── client.go           # Gateway auto-registration client (Stage 0)
│   ├── client_test.go      # Registration client tests (Stage 0)
│   └── types.go            # RegistrationResult, RMQConfig, etc. (Stage 0)
├── llm/
│   ├── factory.go          # ChatModel factory (Stage 1)
│   ├── probe.go            # Model capabilities probe (Stage 1)
│   └── factory_test.go     # Factory tests (Stage 1)
├── secrets/
│   └── resolver.go         # Env-var secret resolver (Stage 0)
├── tools/
│   ├── registry.go         # Tool builder/registry (Stage 3) + memory tools (Stage 9)
│   ├── webfetch.go         # Web fetch (Stage 3)
│   ├── bash.go             # Bash/shell (Stage 4)
│   ├── websearch.go        # Web search (Stage 5)
│   ├── docfetch.go         # Document fetcher + EXIF extraction (Stage 6)
│   └── memory.go           # Memory read/write tools (Stage 9)
├── sandbox/
│   └── manager.go          # Docker sandbox (Stage 4)
├── mcp/
│   ├── loader.go           # MCP tool loader — SSE + STDIO transports (Stage 8)
│   └── loader_test.go      # MCP loader tests (Stage 8)
└── memory/
    └── store.go            # Durable memory store (Stage 9)

cli/
└── agent.go                # CLI subcommand (Stage 0) + memory/usage wiring (Stage 9)
```

---

## Milestone Targets

| Milestone | Stages | What Works | Status |
|-----------|--------|------------|--------|
| **M1: Chat Agent** | 0–2 | Agent consumes from RabbitMQ, chats via LLM, publishes events | ✅ |
| **M2: Web Agent** | +3 | Agent can fetch and summarize web pages | ✅ |
| **M3: Shell Agent** | +4,5 | Agent can run commands in sandbox, search web | ✅ |
| **M4: Full Tools** | +6,8 | Documents, MCP tools (EXIF absorbed by doc_fetch) | ✅ |
| **M5: Production** | +9,10 | Memory, commands, usage tracking, observability | ✅ |
