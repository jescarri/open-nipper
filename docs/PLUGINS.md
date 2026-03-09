# Plugin Architecture

## Overview

Open-Nipper's plugin system is modeled after **Claude Skills** (Cursor's `SKILL.md` pattern) and **OpenClaw's workspace skills** (`~/.openclaw/workspace/skills/`). Plugins are self-contained units of capability that agents can invoke. They are described in natural language, injected into the system prompt, and selected by the AI model itself based on intent matching — no routing logic, no classifiers, no regex.

This is the same mechanism OpenClaw uses: the model reads skill descriptions alongside the user's message and decides which one fits.

## Core Concepts

### What Is a Plugin?

A plugin is a directory containing:

```
$HOME/.open-nipper/plugins/{plugin-name}/
├── SKILL.md              # Natural language description (injected into system prompt)
├── scripts/              # Executable scripts the agent can invoke
│   ├── run.sh            # Main entry point
│   └── helpers/
│       └── ...
├── templates/            # Prompt templates, output templates
│   └── ...
├── config.yaml           # Plugin configuration
└── package.json          # Optional: Node.js dependencies
```

### `SKILL.md` — The Plugin Contract

The `SKILL.md` file is the **only** thing the AI model sees. It describes what the plugin does, when to use it, and how to invoke it. The model selects plugins by matching user intent to these descriptions.

Example `SKILL.md`:

```markdown
# Deploy Application

Deploy the application to the specified environment.

## When to Use

Use this skill when the user asks to:
- Deploy the app, service, or application
- Push to production, staging, or development
- Release a new version

## Parameters

- `environment`: Required. One of: "production", "staging", "dev"
- `version`: Optional. Specific version tag. Defaults to latest commit.
- `dry_run`: Optional. If true, show what would be deployed without executing.

## Usage

Run the deploy script:
```bash
/plugins/deploy/scripts/run.sh --env=$environment --version=$version --dry-run=$dry_run
```

## Notes
- Production deploys require confirmation
- Rollback available via `rollback` skill
```

### Plugin Types

| Type       | Location                                      | Lifecycle              |
|------------|-----------------------------------------------|------------------------|
| Managed    | Installed via `command /plugins install zip file url`         | User-installed         |
| Workspace  | `~/.open-nipper/plugins/{name}/`              | User-created           |

**Per-User Plugins** are only loaded for the owning user's sessions. User A cannot see or invoke User B's per-user plugins. This enforces the multi-user isolation requirement.

## Plugin Configuration (`config.yaml`)

```yaml
name: "deploy"
version: "1.0.0"
description: "Deploy application to environments"

# Execution
runtime: "bash"                    # "bash" | "node" | "python" | "binary"
entrypoint: "scripts/run.sh"
timeout: 300                       # Max execution time in seconds
workingDirectory: "workspace"      # Run in user's workspace directory

# Security
requireConfirmation: true          # Ask user before executing (for destructive actions)
sandbox:
  network: true                    # Plugin needs network access
  filesystem: "read-write"         # "none" | "read" | "read-write"
  secrets:                         # 1Password references (resolved by agent via op CLI)
    - "op://vault/deploy/ssh-key"
    - "op://vault/deploy/kubeconfig"

# Dependencies
dependencies:
  system: ["kubectl", "docker"]    # Required system binaries
  node: []                         # npm packages (installed from package.json)
  python: []                       # pip packages

# Filtering
channels: ["whatsapp", "slack"]    # Allowed channel types from GATEWAY_ARCHITECTURE.md (empty = all)
```

### Skill type: `script` vs `mcp`

In the agent implementation, each skill has a **type** in `config.yaml`:

| type     | Meaning | Behaviour |
|----------|--------|-----------|
| `script` | (default) | Skill has an entrypoint script; use `skill_exec` or bash to run it. |
| `mcp`    | MCP-only  | No script. The skill is description-only; the model should use **MCP tools** (from your MCP servers) as described in SKILL.md. Do not call `skill_exec` for this skill — invoke the relevant MCP tools directly. |

**Example: MCP-only skill** (e.g. plant-care that uses Home Assistant MCP):

```yaml
# skills/plant-care/config.yaml
name: "plant-care"
version: "1.0.0"
type: "mcp"   # No script; use GetLiveContext, Hass* tools, etc. as described in SKILL.md
```

SKILL.md describes when and how to use the MCP tools (e.g. GetLiveContext for soil sensors, HassTurnOn for irrigation). The agent loads the skill into the prompt and tags it as MCP-only so the model uses MCP tools directly.

## Plugin Discovery and Loading

### Startup Sequence

```
1. Scan managed plugins (~/.open-nipper/plugins/)
3. For each user:
   a. Apply user's skillFilter (if configured)
   b. Build skill prompt for this user's sessions
```

**IMPORTANT** Report via http-metrics number of skills present per agent

### Skill Prompt Building

Adapted from OpenClaw's `buildWorkspaceSkillsPrompt()`:

```typescript
function buildSkillPrompt(plugins: Plugin[]): string {
  const sections = plugins.map(p => {
    const skillMd = readFile(`${p.path}/SKILL.md`);
    return `<skill name="${p.name}">\n${skillMd}\n</skill>`;
  });

  return `## Available Skills

You have the following skills available. When the user's request matches a skill,
use the exec tool to run the skill's script with the appropriate parameters.

${sections.join("\n\n")}`;
}
```

This prompt is injected into the system prompt at the start of every agent run. The AI model reads all skill descriptions and decides which (if any) to invoke based on the user's message.

### Skill Filtering

Per-user skill filtering is managed via the `user_policies` table in the datastore (see `DATASTORE.md`) with `policy_type = "skills"`. This allows administrators to manage skill access at runtime via the admin API.

Example policy data:

```json
{
  "include": ["deploy", "search-docs", "run-tests"],
  "exclude": []
}
```

```json
{
  "include": ["*"],
  "exclude": ["deploy"]
}
```

## Plugin Execution

### Flow

```
User: "Deploy the app to staging"
        │
        ▼
Agent reads system prompt (includes all skill descriptions)
        │
        ▼
Model matches intent to "deploy" skill
        │
        ▼
Model generates tool call:
  { tool: "exec", params: { cmd: "/plugins/deploy/scripts/run.sh --env=staging" } }
        │
        ▼
Tool execution layer intercepts
        │
        ▼
Plugin policy check:
  ├── Is plugin allowed for this user? (skillFilter check)
  ├── Is plugin allowed on this channel type? (channelType check per GATEWAY_ARCHITECTURE.md)
  ├── Does plugin require confirmation? → Send confirmation request to user
  └── All checks pass → Execute
        │
        ▼
Resolve plugin secrets via agent's op CLI (host-side):
  op read "op://vault/deploy/ssh-key" → inject as env var
        │
        ▼
Execute in Docker container:
  docker exec {container} /plugins/deploy/scripts/run.sh --env=staging
        │
        ▼
Stream stdout/stderr back as tool_result
        │
        ▼
Agent processes result, generates response
```

### Plugin Execution Environment

Plugins execute inside the agent's Docker container (see `AGENT_ARCHITECTURE.md`). The container provides:

| Resource               | Access                                           |
|------------------------|--------------------------------------------------|
| Filesystem             | Plugin's own directory (read-only) + workspace (read-write) |
| Network                | Based on plugin `config.yaml` sandbox settings   |
| Secrets                | Injected as env vars via agent's `op` CLI (host-side) |
| System binaries        | Validated at startup from `dependencies.system`  |
| User workspace         | `~/.open-nipper/users/{userId}/workspace/`       |

### Plugin Environment Variables

```bash
# Injected automatically for every plugin execution
NIPPER_PLUGIN_NAME="deploy"
NIPPER_PLUGIN_DIR="/plugins/deploy"
NIPPER_USER_ID="user-01"
NIPPER_SESSION_ID="01956a3c-..."
NIPPER_SESSION_KEY="user:user-01:channel:slack:session:01956a3c"
NIPPER_CHANNEL_TYPE="slack"
NIPPER_WORKSPACE="/workspace"

# Injected from 1Password via agent's op CLI (based on config.yaml secrets)
DEPLOY_SSH_KEY="-----BEGIN OPENSSH PRIVATE KEY-----..."
DEPLOY_KUBECONFIG="apiVersion: v1..."
```

## Plugin Lifecycle Hooks

Plugins can define hooks that execute at specific points in the agent lifecycle. Adapted from OpenClaw's `before_model_resolve`, `before_prompt`, and `after_prompt` hooks:

```yaml
hooks:
  before_prompt:
    script: "scripts/hooks/before-prompt.sh"
    description: "Inject deployment status into context"

  after_response:
    script: "scripts/hooks/after-response.sh"
    description: "Log deployment metrics"

  on_error:
    script: "scripts/hooks/on-error.sh"
    description: "Send alert on deployment failure"
```

### Available Hooks

| Hook                | Trigger                                  | Can Modify          |
|---------------------|------------------------------------------|---------------------|
| `before_prompt`     | Before the system prompt is sent to model | System prompt, context |
| `after_response`    | After the model generates a response     | Nothing (read-only) |
| `before_tool`       | Before a tool executes                   | Tool parameters     |
| `after_tool`        | After a tool returns                     | Nothing (read-only) |
| `on_error`          | When an error occurs                     | Nothing (read-only) |
| `on_compaction`     | Before context compaction                | Compaction strategy  |
| `before_model_resolve` | Before model selection                | Model choice        |

## Plugin Registry

### Installation

```bash
# Install from a registry (future: NipperHub)
nipper plugins install deploy

# Install from a git repository
nipper plugins install https://github.com/user/nipper-plugin-deploy.git

# Install from a local directory
nipper plugins install ./my-plugin

# List installed plugins
nipper plugins list

# Remove a plugin
nipper plugins remove deploy
```

### Plugin Manifest (for registry)

```json
{
  "name": "deploy",
  "version": "1.0.0",
  "description": "Deploy application to various environments",
  "author": "alice",
  "license": "MIT",
  "runtime": "bash",
  "dependencies": {
    "system": ["kubectl", "docker"]
  },
  "channels": ["whatsapp", "slack"],
  "tags": ["devops", "deployment"]
}
```

## Bundled Plugins

Open-Nipper ships with these plugins out of the box:

| Plugin          | Description                                           |
|-----------------|-------------------------------------------------------|
| `read`          | Read files from workspace                             |
| `write`         | Write files to workspace                              |
| `edit`          | Find-and-replace editing in files                     |
| `exec`          | Execute shell commands                                |
| `process`       | Manage background processes                           |
| `web-search`    | Search the web                                        |
| `web-fetch`     | Fetch and parse web pages                             |
| `message`       | Send messages to other channels                       |
| `session-spawn` | Create sub-agent sessions                             |

These follow the same `SKILL.md` + `config.yaml` structure as user plugins. They can be overridden by workspace plugins with the same name.

## Plugin Security

See `SECURITY.md` for full details. Key points:

- Plugins run inside Docker containers — no host access.
- Secrets are resolved from 1Password by the agent's host-side runtime and injected as ephemeral environment variables. Never written to disk inside the container.
- Plugin network access is explicitly opt-in (`sandbox.network: true`).
- `requireConfirmation: true` forces user approval before destructive actions.
- Per-user plugin scoping prevents cross-user access.
- Plugin code is not editable by the agent — SKILL.md is read-only inside the container.

## Plugin Development Guide

### Creating a New Plugin

```bash
# Scaffold a new plugin
nipper plugins create my-plugin

# This creates:
# ~/.open-nipper/plugins/my-plugin/
# ├── SKILL.md          (template)
# ├── scripts/run.sh    (template)
# ├── config.yaml       (template)
# └── README.md         (template)
```

### Testing a Plugin

```bash
# Test plugin execution locally
nipper plugins test my-plugin --params '{"env": "staging"}'

# Test with a specific user context
nipper plugins test my-plugin --user user-01 --channel slack

# Validate plugin structure
nipper plugins validate my-plugin
```

### Plugin Best Practices

1. **Keep SKILL.md concise** — The description is injected into every prompt. Longer descriptions consume context tokens.
2. **Use specific trigger phrases** — Help the model match intent accurately.
3. **Return structured output** — JSON output from scripts is easier for the model to process.
4. **Handle errors gracefully** — Return non-zero exit codes with stderr messages. The agent will see the error and can explain it to the user.
5. **Minimize secrets** — Only request the secrets the plugin needs. Principle of least privilege.
6. **Set appropriate timeouts** — Long-running plugins should use the `process` tool for background execution.
