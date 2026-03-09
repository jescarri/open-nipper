# User Management

This document covers the user management model in Open-Nipper: how users, identities, allowlists, and agents relate to each other, and how to operate them via the CLI.

## Concepts

### Users

A **user** is the root entity in Open-Nipper. Every message processed by the system belongs to exactly one user. Users are created by administrators and assigned a server-generated ID with a `usr_` prefix (e.g., `usr_019539a1-b2c3-7d4e-5f6a-7b8c9d0e1f2a`).

Users hold:
- **Name** — display name
- **Enabled flag** — disabled users cannot send or receive messages
- **Default model** — LLM model for the agent (e.g., `claude-sonnet-4-20250514`)
- **Preferences** — extensible JSON blob (timezone, theme, etc.)

### Channel Identities

A **channel identity** maps a channel-native identifier to an Open-Nipper user. When a message arrives on a channel (WhatsApp, Slack, MQTT), the Gateway looks up the sender's channel-native ID in the `user_identities` table to determine which user sent it.

Examples:
- WhatsApp: JID like `1234567890@s.whatsapp.net`
- Slack: Slack user ID like `U07ABC123`
- MQTT: client ID or topic-embedded userId

A user can have **multiple identities** across different channels (e.g., one WhatsApp JID and one Slack ID). Each `(channel_type, channel_identity)` pair is globally unique — no two users can share the same identity.

### Allowlist

The **allowlist** controls which channels a user is permitted to use. Even if a user has a channel identity mapped, messages are **silently discarded** unless the user has an active allowlist entry for that channel.

This is intentional: identity mapping and permission granting are separate concerns. You might map a user's WhatsApp JID for future use but only allow them on Slack today.

The special channel type `*` grants access to all channels.

### Agents

An **agent** is a process that consumes messages from a user's RabbitMQ queue, runs them through an LLM, and publishes response events back to the Gateway. Each agent is bound to exactly one user.

Agents authenticate via a token with an `npr_` prefix. The token is shown **once** at provisioning time and stored as a SHA-256 hash in the database. The agent uses this token to call the Gateway's `/agents/register` endpoint, which provisions RabbitMQ credentials and returns the full connection configuration.

A user can have multiple agents (e.g., one per environment), but typically has one.

### Policies

**Policies** are per-user overrides for tool access and resource limits. They sit in the `user_policies` table and are delivered to the agent at registration time. If a user has no policy, the agent uses the gateway-level defaults from config.

### Cron Jobs

**Cron jobs** are recurring scheduled prompts. They are managed per-user and fire on a cron schedule. When a cron job fires, the prompt is sent to the user's agent and the response is broadcast to the user's configured `notify_channels`.

### At Jobs

**At jobs** are one-shot scheduled prompts. They fire once at a specific time, then auto-delete. They use the same delivery pipeline as cron jobs (broadcast to `notify_channels`), but are designed for reminders and deferred actions:

- "Remind me at 1 PM to call the dentist"
- "At Feb 21 2026 5:10 AM, send the weekly report"

At jobs are stored in the `at_jobs` table with a composite primary key `(user_id, id)`. After firing, the scheduler removes them from memory and the database automatically.

## Data Model

```
users (root)
  ├── user_identities    (channel → user mapping)
  ├── allowed_list       (channel permission grants)
  ├── agents             (agent processes)
  ├── user_policies      (tool/resource overrides)
  ├── cron_jobs          (recurring scheduled prompts)
  └── at_jobs            (one-shot scheduled prompts)
```

All child tables have `ON DELETE CASCADE` foreign keys to `users.id`. Deleting a user removes all associated records.

## Quick Start: Bootstrap

The fastest way to go from nothing to a working user + agent:

```bash
nipper admin bootstrap \
  --name "Alice" \
  --channel whatsapp --identity "1234567890@s.whatsapp.net" \
  --channel slack    --identity "U07ABC123" \
  --agent-label "prod-01"
```

This single command:
1. Creates the user (auto-generates `usr_` ID)
2. Adds channel identities for WhatsApp and Slack
3. Grants allowlist permissions for both channels
4. Provisions an agent and prints the auth token

If any step fails, the entire operation is rolled back (user delete cascades to all child records).

Output:
```
✓ User created:      usr_019539a1-b2c3-7d4e-... (Alice)
✓ Identity added:    whatsapp → 1234567890@s.whatsapp.net
✓ Allowlist granted:  whatsapp
✓ Identity added:    slack → U07ABC123
✓ Allowlist granted:  slack
✓ Agent provisioned: agt-usr_019539a1...-prod-01

┌─────────────────────────────────────────────────────────────────┐
│  Auth Token (save now — shown only once):                       │
│  npr_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ             │
└─────────────────────────────────────────────────────────────────┘

To start the agent:
  export NIPPER_GATEWAY_URL="http://localhost:18789"
  export NIPPER_AUTH_TOKEN="npr_abcdefghijklmnopqrstuvwxyz..."
```

## Adding Channels to an Existing User

To add a new channel to an existing user (identity + allowlist in one step):

```bash
nipper admin user add-channel usr_019539a1... \
  --channel mqtt --identity "sensor-device-42"
```

Supports multiple pairs:

```bash
nipper admin user add-channel usr_019539a1... \
  --channel mqtt    --identity "sensor-device-42" \
  --channel slack   --identity "U08XYZ456"
```

## CLI Reference

### User Commands

| Command | Description |
|---------|-------------|
| `nipper admin user add --name NAME [--model MODEL]` | Create a new user |
| `nipper admin user list` | List all users |
| `nipper admin user get <userId>` | Get user details |
| `nipper admin user update <userId> [--name] [--model] [--enable] [--disable]` | Update a user |
| `nipper admin user delete <userId>` | Delete a user (cascades) |
| `nipper admin user add-channel <userId> --channel CH --identity ID` | Add identity + allowlist in one step |

### Identity Commands

| Command | Description |
|---------|-------------|
| `nipper admin identity add <userId> --channel CH --identity ID` | Add a channel identity |
| `nipper admin identity list <userId>` | List identities for a user |
| `nipper admin identity remove <userId> --id ROW_ID` | Remove an identity |

### Allowlist Commands

| Command | Description |
|---------|-------------|
| `nipper admin allow <userId> --channel CH` | Grant channel permission |
| `nipper admin deny <userId> --channel CH` | Revoke channel permission |
| `nipper admin allowlist show [--channel CH]` | View all allowlist entries |
| `nipper admin allowlist remove <userId> --channel CH` | Remove an allowlist entry |

### Agent Commands

| Command | Description |
|---------|-------------|
| `nipper admin agent provision --user UID --label LABEL` | Provision a new agent |
| `nipper admin agent list [--user UID]` | List agents |
| `nipper admin agent get <agentId>` | Get agent details |
| `nipper admin agent rotate-token <agentId>` | Rotate auth token |
| `nipper admin agent revoke <agentId>` | Revoke agent (keep record) |
| `nipper admin agent delete <agentId>` | Delete agent |

### Cron Job Commands

| Command | Description |
|---------|-------------|
| `nipper admin cron list [--user UID]` | List cron jobs (optionally filtered by user) |

Cron jobs are primarily managed by agents via the `/agents/me/cron/jobs` API or defined in the agent's config file. The admin CLI provides read-only visibility.

### At Job Commands

| Command | Description |
|---------|-------------|
| `nipper admin at list [--user UID]` | List at jobs (optionally filtered by user) |
| `nipper admin at add <userId> --id ID --at TIME --prompt TEXT [--notify CH,...]` | Schedule a one-shot prompt |
| `nipper admin at remove <userId> --id ID` | Remove a pending at job |

The `--at` flag accepts RFC 3339 timestamps (e.g., `2026-03-07T13:00:00-06:00`). Jobs scheduled in the past are rejected.

### Bootstrap Command

| Command | Description |
|---------|-------------|
| `nipper admin bootstrap --name NAME --channel CH --identity ID --agent-label LABEL [--model MODEL]` | Full onboarding in one step |

## Common Workflows

### Onboarding a new user (one command)

```bash
nipper admin bootstrap \
  --name "Bob" \
  --channel whatsapp --identity "9876543210@s.whatsapp.net" \
  --agent-label "bobs-agent"
```

### Onboarding a new user (step by step)

```bash
# 1. Create user
nipper admin user add --name "Bob"
# → usr_01abc...

# 2. Add identity
nipper admin identity add usr_01abc... --channel whatsapp --identity "9876543210@s.whatsapp.net"

# 3. Grant allowlist
nipper admin allow usr_01abc... --channel whatsapp

# 4. Provision agent
nipper admin agent provision --user usr_01abc... --label "bobs-agent"
# → npr_xyz... (save this!)
```

### Adding a channel to an existing user

```bash
nipper admin user add-channel usr_01abc... \
  --channel slack --identity "U07XYZ"
```

### Temporarily disabling a user

```bash
nipper admin user update usr_01abc... --disable
```

Messages from the user are silently discarded while disabled. Re-enable with:

```bash
nipper admin user update usr_01abc... --enable
```

### Revoking channel access without removing identity

```bash
nipper admin deny usr_01abc... --channel whatsapp
```

The identity mapping remains (for potential re-enabling), but messages are discarded.

### Rotating an agent token

```bash
nipper admin agent rotate-token agt-usr_01abc...-bobs-agent
# → new npr_... token (update agent environment)
```

### Scheduling a one-shot reminder (at job)

```bash
nipper admin at add usr_01abc... \
  --id "reminder-dentist" \
  --at "2026-03-07T13:00:00-06:00" \
  --prompt "Remind me to call the dentist" \
  --notify whatsapp
```

The job fires once at the specified time, sends the prompt to the agent, broadcasts the response to the user's WhatsApp, then auto-deletes.

### Listing and removing at jobs

```bash
# List all at jobs
nipper admin at list

# List at jobs for a specific user
nipper admin at list --user usr_01abc...

# Remove a pending at job
nipper admin at remove usr_01abc... --id "reminder-dentist"
```

### Decommissioning a user

```bash
nipper admin user delete usr_01abc...
```

This cascades: removes all identities, allowlist entries, agents, policies, cron jobs, and at jobs.
