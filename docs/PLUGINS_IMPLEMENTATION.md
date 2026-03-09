# Plugins (Skills) Implementation Plan

## Implemented Feature Checklist

| Stage | Status | Notes |
|-------|--------|--------|
| **Stage 1: Skill Loader and Registry** | ✅ Completed | Loader, types, config, agent_loader, unit tests |
| **Stage 2: System Prompt Integration** | ✅ Completed | Runtime skillsLoader, WithSkillsLoader, prompt injection, cli wiring |
| **Stage 3: Secret Provider + Env-Var Provider** | ✅ Completed | SecretProvider, EnvVarProvider, ProviderRegistry, unit tests |
| **Stage 4: Docker Sandbox Integration** | ✅ Completed | Skills mount, ExecWithEnv, staging dir, executor, bash /skills/ hook, CLI wiring |
| **Stage 5: Metrics and Observability** | ✅ Completed | Gauge/counter/histogram, skill events, executor metrics, runtime skill_loaded/start/end |
| Stage 6: CLI Commands (Future) | ⬜ Pending | — |
| **skill bootstrap** | ✅ Completed | `nipper skill bootstrap $name` with `-d`/`--dir` for destination directory |

---

## Executive Summary

Implement the plugin/skills system described in `PLUGINS.md` using a **provider-based secret integration** starting with the **env-var provider**. Skills are loaded from the agent's working directory, their descriptions are injected into the system prompt, and they execute inside the existing Docker sandbox with per-execution secret injection.

**Key architectural decisions:**

- **EINO does not have a "skills" concept.** Skills are a custom layer built on top of EINO's `tool.BaseTool` interface and the ReAct agent pattern.
- **Secrets flow via providers.** The `SecretProvider` interface allows swapping env-var → Vault → 1Password without changing skill code.
- **Docker `exec -e`** is used for per-skill secret injection at execution time (not at container creation time). This avoids exposing secrets from Skill A to Skill B.
- **Skills directory is volume-mounted read-only** into the container at creation time.
- **Claude and Cursor SKILL.md formats are supported interchangeably** — both are markdown files with natural-language descriptions; the loader reads them uniformly.

---

## Pre-Implementation: Answered Questions

### 1. Does EINO have the concept of skills?

**No.** EINO provides:
- `tool.BaseTool` interface for tool definitions
- `toolutils.InferTool()` for automatic JSON schema generation from Go functions
- `react.NewAgent()` for the ReAct (Reason + Act) agent loop
- `compose.ToolsNodeConfig` for passing tools to the agent
- MCP tool loading via `eino-ext/components/tool/mcp`

Skills must be implemented as a **custom layer** that:
1. Scans directories for `SKILL.md` files
2. Parses `config.yaml` for metadata (runtime, secrets, timeout, etc.)
3. Injects skill descriptions into the system prompt
4. Executes skill scripts via the existing Docker sandbox

### 2. How can you dynamically load skills when needed?

Follow the **MCP Loader pattern** already established in the codebase:

```
MCP Loader pattern (existing):
  agentmcp.NewLoader() → loads MCP tools at startup
  r.mcpLoader.Tools() → returns current tool list (refreshed after reconnect)
  r.currentTools()    → merges base tools + MCP tools on each message

Skills Loader pattern (proposed):
  skills.NewLoader() → scans skills directory at startup, parses SKILL.md + config.yaml
  r.skillsLoader.Skills() → returns loaded skill definitions
  r.buildSystemPrompt() → injects skill descriptions into system prompt
  Skill execution → via sandbox.Manager.Exec() with per-exec env vars
```

Skills are **not registered as EINO tools**. They are:
1. **Injected into the system prompt** as natural-language descriptions (the model reads them and decides which to invoke)
2. **Executed via the existing `bash` tool** — the model generates a tool call like `bash(command="/skills/deploy/scripts/run.sh --env=staging")`
3. **Pre-execution hooks** resolve secrets and inject them as env vars into the Docker exec call

### 3. Docker runtime considerations

The Docker sandbox uses `--read-only` with tmpfs overlays. Key constraints:

| Constraint | Solution |
|-----------|----------|
| Read-only rootfs | Skills directory mounted read-only via `-v` at container creation |
| Env vars fixed at `docker run` | Extend `sandbox.Manager.Exec()` to accept per-execution env vars via `docker exec -e` |
| No new mounts after creation | Skills dir path is known at startup; mount during `Create()` |
| Secret isolation between skills | Per-execution `-e` flags scope secrets to that exec process only |
| Large/multi-line secrets (SSH keys) | Write temp file on host in a pre-mounted staging dir; pass path as env var |

---

## Implementation Stages

### Stage 1: Skill Loader and Registry

**Goal:** Scan the skills directory, parse `SKILL.md` and `config.yaml`, build an in-memory registry of available skills.

**Files to create/modify:**

| File | Action |
|------|--------|
| `internal/agent/skills/loader.go` | **New** — Skill directory scanner and parser |
| `internal/agent/skills/types.go` | **New** — Skill, SkillConfig, SkillSecret types |
| `internal/config/config.go` | **Modify** — Add `SkillsConfig` to `AgentRuntimeConfig` |
| `internal/config/agent_loader.go` | **Modify** — Load and resolve skills config |

**Skill directory structure (supports Claude + Cursor interchangeably):**

```
{base_path}/skills/
├── deploy/
│   ├── SKILL.md              # Natural language description (Claude/Cursor format)
│   ├── config.yaml           # Optional: metadata, secrets, timeout
│   └── scripts/
│       └── run.sh            # Entry point
├── search-docs/
│   ├── SKILL.md
│   └── scripts/
│       └── run.sh
```

The loader treats `SKILL.md` as the **only required file**. If `config.yaml` is missing, the skill is still loadable (description-only, no secrets, no scripts — model uses it as context guidance only, like Cursor's SKILL.md pattern).

**Types:**

```go
// internal/agent/skills/types.go

type Skill struct {
    Name        string       // directory name
    Path        string       // absolute path on host
    Description string       // contents of SKILL.md
    Config      *SkillConfig // parsed config.yaml (nil if absent)
}

type SkillConfig struct {
    Name        string            `yaml:"name"`
    Version     string            `yaml:"version"`
    Description string            `yaml:"description"`
    Runtime     string            `yaml:"runtime"`      // "bash" | "node" | "python"
    Entrypoint  string            `yaml:"entrypoint"`   // e.g. "scripts/run.sh"
    Timeout     int               `yaml:"timeout"`      // seconds
    Secrets     []SkillSecretRef  `yaml:"secrets"`      // secret references
    Network     bool              `yaml:"network"`      // needs network access
    Confirm     bool              `yaml:"require_confirmation"`
    Channels    []string          `yaml:"channels"`     // allowed channel types
    Deps        SkillDeps         `yaml:"dependencies"`
}

type SkillSecretRef struct {
    Name     string `yaml:"name"`     // logical name (e.g. "deploy_ssh_key")
    EnvVar   string `yaml:"env_var"`  // env var name inside container
    Provider string `yaml:"provider"` // "env" (default) | "vault" | "op" (future)
    Ref      string `yaml:"ref"`      // provider-specific ref (env var name on host, vault path, etc.)
}

type SkillDeps struct {
    System []string `yaml:"system"` // required binaries
}
```

**Loader logic:**

```go
// internal/agent/skills/loader.go

type Loader struct {
    skills []Skill
    logger *zap.Logger
}

func NewLoader(basePath string, logger *zap.Logger) (*Loader, error)
func (l *Loader) Skills() []Skill
func (l *Loader) SkillByName(name string) (*Skill, bool)
func (l *Loader) BuildPromptSection() string  // renders <skill> tags for system prompt
```

The loader:
1. Walks `{basePath}/skills/` looking for subdirectories
2. For each subdirectory, reads `SKILL.md` (required) and `config.yaml` (optional)
3. Builds `[]Skill` sorted by name
4. Logs skill count and names at INFO level

**Config addition:**

```go
// Added to AgentRuntimeConfig
type SkillsConfig struct {
    Enabled  bool   `yaml:"enabled"   mapstructure:"enabled"`
    Path     string `yaml:"path"      mapstructure:"path"`     // override; default: {base_path}/skills
}
```

**Verification checklist:**

- [ ] Loader finds skills in `{base_path}/skills/`
- [ ] Loader reads SKILL.md content
- [ ] Loader parses config.yaml when present
- [ ] Loader gracefully handles missing config.yaml (description-only skills)
- [ ] Loader skips directories without SKILL.md (with warning log)
- [ ] Loader logs skill count and names at startup
- [ ] Unit tests: empty dir, one skill, multiple skills, missing SKILL.md, malformed config.yaml

---

### Stage 2: System Prompt Integration

**Goal:** Inject loaded skill descriptions into the system prompt so the model can select them by intent.

**Files to modify:**

| File | Action |
|------|--------|
| `internal/agent/runtime.go` | **Modify** — Add `skillsLoader` field, inject skills into `buildSystemPrompt()` |
| `internal/agent/runtime.go` | **Modify** — Add `WithSkillsLoader()` runtime option |
| `cli/agent.go` | **Modify** — Initialize skills loader, pass to runtime |

**Prompt injection format (adapted from PLUGINS.md):**

```
## Available Skills

You have the following skills available. When the user's request matches a skill,
use the bash tool to run the skill's script with the appropriate parameters.

<skill name="deploy">
# Deploy Application

Deploy the application to the specified environment.
...
</skill>

<skill name="search-docs">
# Search Documentation
...
</skill>
```

This section is appended **before** the tool hints section in `buildSystemPrompt()`, so the model sees skills alongside tools.

**Compaction-aware injection:**

| Compaction Level | Skill Injection |
|-----------------|-----------------|
| `full` | All skills, full SKILL.md content |
| `standard` | All skills, first paragraph of SKILL.md only |
| `compact` | Top 3 skills by name, title + one-liner |
| `minimal` | No skills injected |

(Compaction is future work; Stage 2 injects all skills fully.)

**Verification checklist:**

- [ ] Skills appear in system prompt when loader has skills
- [ ] No skills section when loader is empty/nil
- [ ] Model can identify and select a skill from the prompt
- [ ] Skills prompt section does not duplicate tool hints
- [ ] Integration test: model selects correct skill for a matching user intent

---

### Stage 3: Secret Provider Interface + Env-Var Provider

**Goal:** Define a `SecretProvider` interface and implement the env-var provider. Resolve per-skill secrets before execution.

**Files to create/modify:**

| File | Action |
|------|--------|
| `internal/agent/skills/secrets.go` | **New** — SecretProvider interface + env-var implementation |
| `internal/agent/skills/types.go` | **Modify** — Add provider references to types |
| `internal/config/config.go` | **Modify** — Add secret provider config |

**Interface:**

```go
// internal/agent/skills/secrets.go

// SecretProvider resolves secret references to plaintext values.
// Implementations: EnvVarProvider (Stage 3), VaultProvider (future), OPProvider (future).
type SecretProvider interface {
    // Name returns the provider identifier (e.g. "env", "vault", "op").
    Name() string

    // Resolve takes a list of secret references and returns a map of env_var → value.
    // References are provider-specific (env var names, vault paths, op:// URIs).
    Resolve(refs []SkillSecretRef) (map[string]string, error)
}
```

**Env-var provider:**

```go
type EnvVarProvider struct {
    logger *zap.Logger
}

func NewEnvVarProvider(logger *zap.Logger) *EnvVarProvider

func (p *EnvVarProvider) Name() string { return "env" }

func (p *EnvVarProvider) Resolve(refs []SkillSecretRef) (map[string]string, error) {
    result := make(map[string]string, len(refs))
    for _, ref := range refs {
        if ref.Provider != "" && ref.Provider != "env" {
            continue // skip non-env refs
        }
        val := os.Getenv(ref.Ref) // ref.Ref is the host env var name
        if val == "" {
            p.logger.Warn("skill secret not found in environment",
                zap.String("name", ref.Name),
                zap.String("envVar", ref.Ref),
            )
            continue
        }
        result[ref.EnvVar] = val // ref.EnvVar is the container env var name
        p.logger.Debug("skill secret resolved",
            zap.String("name", ref.Name),
            zap.String("containerEnvVar", ref.EnvVar),
        )
    }
    return result, nil
}
```

**Provider registry:**

```go
type ProviderRegistry struct {
    providers map[string]SecretProvider
}

func NewProviderRegistry() *ProviderRegistry
func (r *ProviderRegistry) Register(p SecretProvider)
func (r *ProviderRegistry) Resolve(refs []SkillSecretRef) (map[string]string, error)
```

The registry routes each `SkillSecretRef` to the correct provider based on `ref.Provider` field. Defaults to `"env"` when empty.

**Example config.yaml with env-var secrets:**

```yaml
name: "deploy"
version: "1.0.0"
runtime: "bash"
entrypoint: "scripts/run.sh"
timeout: 300
secrets:
  - name: "ssh_key"
    env_var: "DEPLOY_SSH_KEY"        # name inside the container
    provider: "env"                   # which provider resolves this
    ref: "DEPLOY_SSH_KEY"            # host env var to read from
  - name: "kubeconfig"
    env_var: "DEPLOY_KUBECONFIG"
    provider: "env"
    ref: "KUBE_CONFIG_B64"
```

**Verification checklist:**

- [ ] EnvVarProvider reads from host process environment
- [ ] Provider returns map of containerEnvVar → value
- [ ] Missing env vars logged as warnings, not errors (non-fatal)
- [ ] ProviderRegistry correctly routes refs to providers
- [ ] Unit tests: all refs resolved, partial resolution, unknown provider

---

### Stage 4: Docker Sandbox Integration — Skill Execution

**Goal:** Mount the skills directory into the container and execute skill scripts with per-execution secret injection.

**Files to modify:**

| File | Action |
|------|--------|
| `internal/agent/sandbox/manager.go` | **Modify** — Add skills mount to `Create()`, add env vars to `Exec()` |
| `internal/agent/skills/executor.go` | **New** — Skill execution coordinator |
| `internal/agent/tools/registry.go` | **Modify** — Add skill-aware pre-execution hook to bash tool |
| `internal/agent/tools/bash.go` | **Modify** — Support per-exec env vars via sandbox |
| `cli/agent.go` | **Modify** — Wire skills loader, secret providers, sandbox mounts |

#### 4a. Extend `sandbox.Manager.Exec()` for per-execution env vars

Current signature:
```go
func (m *Manager) Exec(ctx context.Context, command, workDir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
```

New signature:
```go
func (m *Manager) Exec(ctx context.Context, command, workDir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)

func (m *Manager) ExecWithEnv(ctx context.Context, command, workDir string, timeout time.Duration, env map[string]string) (stdout, stderr string, exitCode int, err error)
```

The `ExecWithEnv` method builds `docker exec -e KEY=VALUE ... containerID bash -c "command"`.

**Important:** The existing `Exec()` method remains unchanged (backward compatible). `ExecWithEnv` is the new method used by skill execution.

#### 4b. Mount skills directory at container creation

In `sandbox.Manager.Create()`, if a skills path is configured, add a read-only volume mount:

```
-v /host/path/to/skills:/skills:ro
```

This is done via a new field on `SandboxConfig`:

```go
type SandboxConfig struct {
    // ... existing fields ...
    SkillsPath string `yaml:"skills_path" mapstructure:"skills_path"` // host path to skills dir; mounted at /skills in container
}
```

#### 4c. Temp file staging for large secrets

For secrets that are too large for `docker exec -e` (e.g., multi-line SSH keys, kubeconfigs), we use a **staging directory** that is already volume-mounted:

1. At container creation, mount a host-side temp dir to `/tmp/secrets` inside the container:
   ```
   -v /tmp/nipper-secrets-{containerName}:/tmp/secrets:ro
   ```
2. Before skill execution, write large secrets as files in the host staging dir
3. Pass the in-container path as an env var: `DEPLOY_SSH_KEY_FILE=/tmp/secrets/deploy_ssh_key`
4. After execution, delete the host-side temp files

A secret is considered "large" if `len(value) > 4096` bytes or if it contains newlines.

#### 4d. Skill executor

```go
// internal/agent/skills/executor.go

type Executor struct {
    sandbox   *sandbox.Manager
    providers *ProviderRegistry
    logger    *zap.Logger
}

func NewExecutor(sandbox *sandbox.Manager, providers *ProviderRegistry, logger *zap.Logger) *Executor

// Execute runs a skill's entrypoint inside the Docker sandbox with resolved secrets.
func (e *Executor) Execute(ctx context.Context, skill *Skill, args string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
    // 1. Resolve secrets via provider registry
    // 2. Separate small secrets (env vars) from large secrets (temp files)
    // 3. Write large secrets to staging dir
    // 4. Build command: /skills/{name}/{entrypoint} {args}
    // 5. Call sandbox.ExecWithEnv() with all env vars
    // 6. Clean up staged files
    // 7. Return output
}
```

#### 4e. Pre-execution hook in bash tool

When the model generates a bash command targeting `/skills/*`, the bash tool's pre-execution logic:
1. Detects the skill path prefix (`/skills/{name}/...`)
2. Looks up the skill in the registry
3. Resolves secrets via the executor
4. Adds standard env vars: `NIPPER_PLUGIN_NAME`, `NIPPER_PLUGIN_DIR`, `NIPPER_USER_ID`, `NIPPER_SESSION_ID`, `NIPPER_WORKSPACE`
5. Delegates to `sandbox.ExecWithEnv()` instead of `sandbox.Exec()`

This approach is **transparent to the model** — it just calls bash with a skill script path, and the runtime handles secret injection.

**Alternative approach (cleaner):** Register a dedicated `skill_exec` EINO tool that wraps the executor. The model calls `skill_exec(name="deploy", args="--env=staging")` instead of `bash(command="/skills/deploy/scripts/run.sh --env=staging")`. This is cleaner but requires changing the system prompt to reference `skill_exec`. **Recommended: implement both** — the bash pre-exec hook for backward compatibility and `skill_exec` as the preferred path.

**Verification checklist:**

- [ ] Skills directory mounted read-only at `/skills` inside container
- [ ] `ExecWithEnv()` passes `-e` flags to `docker exec`
- [ ] Small secrets injected as env vars
- [ ] Large secrets written to staging dir, path passed as env var, cleaned up after exec
- [ ] Skill execution returns stdout/stderr/exitCode
- [ ] Standard env vars (`NIPPER_PLUGIN_NAME`, etc.) always injected
- [ ] bash tool detects `/skills/` prefix and injects secrets
- [ ] Integration test: skill with env-var secrets executes correctly in Docker sandbox
- [ ] Security: secrets never written to container's read-only filesystem
- [ ] Security: secrets from Skill A not visible during Skill B execution

---

### Stage 5: Metrics, Observability, and Telemetry

**Goal:** Report skill metrics and emit execution events.

**Files to modify:**

| File | Action |
|------|--------|
| `internal/telemetry/instrument.go` | **Modify** — Add skill-related metrics |
| `internal/agent/runtime.go` | **Modify** — Emit skill execution events via publisher |
| `internal/agent/skills/executor.go` | **Modify** — Record execution duration and outcome |

**Metrics:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nipper_agent_skills_loaded` | Gauge | `agent_id` | Number of skills loaded at startup |
| `nipper_agent_skill_executions_total` | Counter | `skill_name`, `exit_code` | Total skill executions |
| `nipper_agent_skill_execution_duration_seconds` | Histogram | `skill_name` | Skill execution duration |
| `nipper_agent_skill_secrets_resolved_total` | Counter | `provider` | Total secrets resolved |

**Events (via existing NipperEvent publisher):**

| Event | Fields |
|-------|--------|
| `skill_loaded` | skill_name, has_config, has_entrypoint |
| `skill_execution_start` | skill_name, args (sanitized) |
| `skill_execution_end` | skill_name, exit_code, duration_ms |
| `skill_secret_resolved` | skill_name, secret_name, provider (never the value) |

**Verification checklist:**

- [ ] `nipper_agent_skills_loaded` gauge reports correct count at startup
- [ ] Skill execution increments counter and records histogram
- [ ] Events emitted for skill start/end
- [ ] Secret values never appear in metrics or events

---

### Stage 6: CLI Commands and User-Facing API (Future)

**Goal:** CLI commands for skill management.

```bash
# List loaded skills
nipper skills list

# Validate a skill directory
nipper skills validate ./my-skill

# Test a skill with parameters
nipper skills test deploy --params '{"env": "staging"}'

# Create a skill scaffold
nipper skills create my-skill
```

This stage is deferred — it depends on Stages 1–5 being complete.

---

## Implementation Order and Dependencies

```
Stage 1: Skill Loader + Registry
    │
    ├──▶ Stage 2: System Prompt Integration
    │       │
    │       └──▶ (Model can now see and select skills)
    │
    └──▶ Stage 3: Secret Provider Interface + Env-Var Provider
            │
            └──▶ Stage 4: Docker Sandbox Integration
                    │
                    ├──▶ (Skills execute in Docker with secrets)
                    │
                    └──▶ Stage 5: Metrics + Observability
                            │
                            └──▶ Stage 6: CLI Commands (future)
```

Stages 2 and 3 can be developed **in parallel** after Stage 1. Stage 4 depends on both.

---

## Example: End-to-End Skill Execution Flow

```
1. Agent starts → skills.NewLoader("{base_path}/skills/") scans directory
   Found: deploy (SKILL.md + config.yaml + scripts/run.sh)
   Found: search-docs (SKILL.md only — no config.yaml)

2. Sandbox created → skills dir mounted:
   docker run --read-only -v {base_path}/skills:/skills:ro ...

3. User sends: "Deploy the app to staging"

4. buildSystemPrompt() injects:
   <skill name="deploy">
   # Deploy Application
   Deploy the application to the specified environment.
   ## Usage
   /skills/deploy/scripts/run.sh --env=$environment
   </skill>

5. Model generates tool call:
   bash(command="/skills/deploy/scripts/run.sh --env=staging")

6. Bash tool pre-exec hook detects /skills/deploy/ prefix:
   a. Looks up "deploy" skill → has config.yaml with secrets
   b. Resolves secrets via EnvVarProvider:
      DEPLOY_SSH_KEY ← os.Getenv("DEPLOY_SSH_KEY") on host
   c. Calls sandbox.ExecWithEnv():
      docker exec \
        -e NIPPER_PLUGIN_NAME=deploy \
        -e NIPPER_PLUGIN_DIR=/skills/deploy \
        -e NIPPER_USER_ID=alice \
        -e DEPLOY_SSH_KEY=<resolved_value> \
        -w /workspace \
        {containerID} bash -c "/skills/deploy/scripts/run.sh --env=staging"

7. Script runs inside container with:
   - Read-only /skills/deploy/ (mounted volume)
   - Read-write /workspace (tmpfs)
   - DEPLOY_SSH_KEY available as env var
   - No other skill's secrets visible

8. Output streamed back → model processes result → response sent to user
```

---

## Security Considerations

| Concern | Mitigation |
|---------|-----------|
| Secret leakage between skills | Per-execution `docker exec -e` scopes secrets to that process |
| Secrets on container filesystem | Read-only FS; large secrets in tmpfs-backed staging dir, cleaned after exec |
| Skill code modification | Skills mounted read-only (`-v ...:/skills:ro`) |
| Untrusted skill scripts | Same sandbox restrictions as bash tool: no network (unless configured), dropped capabilities, PID limits |
| Secret values in logs | Provider logs names only, never values; sanitization pipeline scrubs resolved values |
| Prompt injection via SKILL.md | Skills are loaded from the operator's filesystem, not user-controlled; the model treats them as system instructions |

---

## Config Example

```yaml
agent:
  base_path: "${HOME}/.open-nipper"

  skills:
    enabled: true
    path: ""  # default: {base_path}/skills

  sandbox:
    enabled: true
    image: "ubuntu:noble"
    work_dir: "/workspace"
    memory_limit_mb: 512
    cpu_limit: 1.0
    timeout_seconds: 120
    network_enabled: false
    # skills_path is auto-set from skills.path; no manual config needed

  secrets:
    env_map:
      openai_key: "OPENAI_API_KEY"
      deploy_ssh_key: "DEPLOY_SSH_KEY"
      kube_config: "KUBE_CONFIG_B64"
```

Skill-level secret references in `config.yaml`:

```yaml
# skills/deploy/config.yaml
name: "deploy"
version: "1.0.0"
runtime: "bash"
entrypoint: "scripts/run.sh"
timeout: 300
secrets:
  - name: "ssh_key"
    env_var: "DEPLOY_SSH_KEY"
    provider: "env"
    ref: "DEPLOY_SSH_KEY"
  - name: "kubeconfig"
    env_var: "KUBECONFIG_DATA"
    provider: "env"
    ref: "KUBE_CONFIG_B64"
network: true
require_confirmation: true
channels: ["slack"]
```

---

## Testing Strategy

| Stage | Test Type | What |
|-------|-----------|------|
| 1 | Unit | Loader: scan dirs, parse SKILL.md, parse config.yaml, handle missing files |
| 2 | Unit + Integration | Prompt building with skills section; model selects correct skill |
| 3 | Unit | EnvVarProvider: resolve, partial resolve, missing vars |
| 3 | Unit | ProviderRegistry: routing, unknown provider handling |
| 4 | Integration | Sandbox ExecWithEnv: env vars visible inside container |
| 4 | Integration | Full skill execution: mount, resolve, exec, output |
| 4 | Integration | Large secret staging: write, mount, exec, cleanup |
| 5 | Unit | Metrics registration and increment |
| E2E | Integration | User intent → skill selection → execution → response |
