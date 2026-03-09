# Datastore Architecture

## Overview

Open-Nipper separates configuration into two tiers: **durable system state** stored in a database and **generic operational parameters** stored in config files. The database holds state that changes at runtime through administrative actions (users, allowed identities, access control), while config files hold static operational settings that are defined at deployment time and require a restart to change.

This separation exists because user management is a runtime concern — administrators add, remove, and modify users through the local admin API (see `GATEWAY_ARCHITECTURE.md`) without restarting the system. Generic parameters like queue timeouts, compaction thresholds, and channel adapter settings are deployment concerns that change infrequently and are best managed as version-controlled files.

## Two-Tier Configuration Model

```
┌──────────────────────────────────────────────────────────────────┐
│                     CONFIGURATION TIERS                            │
│                                                                    │
│  TIER 1: DATABASE (Durable System State)                           │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │  What: Data that changes at runtime via admin actions         │  │
│  │  Where: SQLite database (~/.open-nipper/nipper.db)           │  │
│  │  Managed by: Local admin API (POST /admin/...)               │  │
│  │                                                              │  │
│  │  Tables:                                                     │  │
│  │    • users          — registered users and profiles          │  │
│  │    • user_identities — channel identity mappings             │  │
│  │    • allowed_list   — per-channel user allowlist             │  │
│  │    • user_policies  — per-user tool policies and limits      │  │
│  │    • agents         — provisioned agents and token hashes    │  │
│  │    • audit_events   — administrative action log              │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                    │
│  TIER 2: CONFIG FILES (Generic Operational Parameters)             │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │  What: Static settings defined at deployment time            │  │
│  │  Where: ~/.open-nipper/config.yaml (+ env var overrides)     │  │
│  │  Managed by: Editing files, requires restart                 │  │
│  │                                                              │  │
│  │  Sections:                                                   │  │
│  │    • gateway        — bind address, ports, timeouts          │  │
│  │    • channels       — adapter configs (WhatsApp, Slack, etc) │  │
│  │    • queue          — RabbitMQ settings, concurrency limits   │  │
│  │    • agents         — deployment mode, health check settings  │  │
│  │    • observability  — logging, sanitizer rules               │  │
│  │    • security       — sandbox config, rate limits            │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                    │
└──────────────────────────────────────────────────────────────────┘
```

## Tier 1: Database (SQLite)

### Why SQLite

Open-Nipper targets 1–3 users. SQLite is the right choice for this scale:

- **Zero infrastructure** — No separate database server to run, configure, or monitor.
- **Single file** — The entire database is `~/.open-nipper/nipper.db`. Easy to back up (copy one file), easy to inspect (`sqlite3 nipper.db`).
- **ACID transactions** — Full transactional guarantees for concurrent reads and serialized writes (WAL mode).
- **Embeddable** — Works in every language (Go, Python, TypeScript, Rust, Java) via native bindings.
- **Fast enough** — With 1–3 users and low write volume, SQLite handles the workload with microsecond query times.

If the system ever needs to scale beyond a single machine, the database layer can be swapped for PostgreSQL behind the same repository interface (see Migration Path below).

### Database Location

```
~/.open-nipper/nipper.db          # Main database
~/.open-nipper/nipper.db-wal      # Write-ahead log (SQLite WAL mode)
~/.open-nipper/nipper.db-shm      # Shared memory file (SQLite WAL mode)
```

The database file is created on first run if it does not exist. Schema migrations run automatically at startup.

### Schema

#### `users` Table

Stores registered Open-Nipper users. Users are added via the local admin API (see `GATEWAY_ARCHITECTURE.md`).

```sql
CREATE TABLE users (
    id              TEXT PRIMARY KEY,          -- Auto-generated: "usr_" + UUIDv7, e.g. "usr_019539a1-b2c3-7d4e-5f6a-7b8c9d0e1f2a"
    name            TEXT NOT NULL,             -- Display name
    enabled         BOOLEAN NOT NULL DEFAULT 1,-- Whether the user is active
    default_model   TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
    created_at      TEXT NOT NULL,             -- ISO 8601
    updated_at      TEXT NOT NULL,             -- ISO 8601

    -- Preferences (JSON blob for extensibility)
    preferences     TEXT NOT NULL DEFAULT '{}' -- JSON: {"theme": "dark", "timezone": "America/Argentina/Buenos_Aires"}
);
```

#### `user_identities` Table

Maps channel-native identifiers (WhatsApp JIDs, Slack user IDs, MQTT client IDs) to Open-Nipper users. A user can have multiple identities across different channels. This is the table the Gateway queries to resolve inbound messages to users.

```sql
CREATE TABLE user_identities (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type    TEXT NOT NULL,             -- "whatsapp" | "slack" | "mqtt" | "rabbitmq"
    channel_identity TEXT NOT NULL,            -- Channel-native ID: JID, Slack user ID, etc.
    verified        BOOLEAN NOT NULL DEFAULT 0,-- Whether identity has been verified via pairing
    created_at      TEXT NOT NULL,             -- ISO 8601

    UNIQUE(channel_type, channel_identity)    -- One identity per channel per user
);

CREATE INDEX idx_identity_lookup ON user_identities(channel_type, channel_identity);
```

#### `allowed_list` Table

Controls which users are permitted to send messages on each channel. Messages from identities not in the allowed list are **discarded and logged** (see `GATEWAY_ARCHITECTURE.md`). This is the enforcement point for the user allowlist.

```sql
CREATE TABLE allowed_list (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type    TEXT NOT NULL,             -- "whatsapp" | "slack" | "mqtt" | "rabbitmq" | "*"
    enabled         BOOLEAN NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,             -- ISO 8601
    created_by      TEXT NOT NULL,             -- Admin user or "system"

    UNIQUE(user_id, channel_type)
);

CREATE INDEX idx_allowed_lookup ON allowed_list(channel_type, user_id);
```

A `channel_type` of `"*"` means the user is allowed on all channels. If a user has no entry in `allowed_list` for a given channel, messages from that user on that channel are rejected.

#### `user_policies` Table

Per-user tool policies and resource limits. Overrides the defaults from config files.

```sql
CREATE TABLE user_policies (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    policy_type     TEXT NOT NULL,             -- "tools" | "rate_limit" | "skills" | "models"
    policy_data     TEXT NOT NULL,             -- JSON blob
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,

    UNIQUE(user_id, policy_type)
);
```

Example `policy_data` for `policy_type = "tools"`:

```json
{
  "allow": ["read", "write", "edit", "exec", "memory_*"],
  "deny": ["session_spawn", "message"],
  "require_confirmation": ["exec", "write"]
}
```

Example `policy_data` for `policy_type = "rate_limit"`:

```json
{
  "messages_per_minute": 20,
  "messages_per_hour": 200,
  "tokens_per_minute": 100000,
  "tokens_per_hour": 1000000
}
```

#### `agents` Table

Stores provisioned agents and their authentication token hashes. Each agent is bound to exactly one user. Agents are provisioned via the admin API (see `GATEWAY_ARCHITECTURE.md`) and authenticate via the auto-registration endpoint using a bearer token.

```sql
CREATE TABLE agents (
    id              TEXT PRIMARY KEY,           -- "agt-alice-01"
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label           TEXT NOT NULL DEFAULT '',    -- Human-readable label ("anthropic-primary")
    token_hash      TEXT NOT NULL,              -- SHA-256 hash of auth token
    token_prefix    TEXT NOT NULL,              -- First 8 chars of token (for identification in logs/CLI)
    rmq_username    TEXT,                       -- RabbitMQ username (set on first registration)
    status          TEXT NOT NULL DEFAULT 'provisioned', -- "provisioned" | "registered" | "revoked"
    last_registered_at TEXT,                    -- ISO 8601, last successful registration
    last_registered_ip TEXT,                    -- IP of last registration request
    created_at      TEXT NOT NULL,              -- ISO 8601
    updated_at      TEXT NOT NULL,              -- ISO 8601

    UNIQUE(user_id, label)
);

CREATE INDEX idx_agents_user ON agents(user_id);
CREATE INDEX idx_agents_token_prefix ON agents(token_prefix);
```

**Status lifecycle:**

| Status | Meaning |
|--------|---------|
| `provisioned` | Agent created, token issued, agent has not yet called `/agents/register` |
| `registered` | Agent has successfully called `/agents/register` at least once |
| `revoked` | Token revoked — agent can no longer register or connect |

The `token_hash` stores `SHA-256(auth_token)`. The plaintext token is returned once at provisioning time and never stored. The `token_prefix` (first 8 characters, e.g., `npr_a1b2`) allows identifying tokens in logs and CLI output without exposing the full value — same pattern as `ghp_` (GitHub), `sk-` (OpenAI).

#### `cron_jobs` Table

Stores scheduled cron jobs (prompts) that the gateway runs at configured intervals. Jobs are added at runtime via the agent cron API (`POST /agents/me/cron/jobs`) or seeded from config at startup when the DB is empty. **Cron jobs are prompts only** — each job has a `prompt` field (natural language instruction to the agent); there is no command or script field.

```sql
CREATE TABLE cron_jobs (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    schedule          TEXT NOT NULL,           -- 6-field cron expression (with seconds)
    prompt            TEXT NOT NULL,           -- Message sent to the agent when the job fires
    notify_channels   TEXT NOT NULL DEFAULT '[]',  -- JSON array, e.g. ["whatsapp", "slack"]
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX idx_cron_jobs_user ON cron_jobs(user_id);
```

When a user is deleted, the `ON DELETE CASCADE` removes all associated agent rows. The Gateway also deletes the corresponding RabbitMQ users via the Management API during user deletion.

#### `admin_audit` Table

Logs all administrative actions performed via the admin API. Immutable, append-only.

```sql
CREATE TABLE admin_audit (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       TEXT NOT NULL,             -- ISO 8601
    action          TEXT NOT NULL,             -- "user.created" | "user.updated" | "user.deleted" | "identity.added" | "allowlist.updated" | "agent.provisioned" | "agent.registered" | "agent.revoked" | "agent.deprovisioned" | "agent.token_rotated" | ...
    actor           TEXT NOT NULL,             -- Who performed the action ("admin", "system", "api")
    target_user_id  TEXT,                      -- Affected user (if applicable)
    details         TEXT NOT NULL,             -- JSON: action-specific details
    ip_address      TEXT                       -- Source IP of admin request
);

CREATE INDEX idx_audit_time ON admin_audit(timestamp);
CREATE INDEX idx_audit_user ON admin_audit(target_user_id);
```

### Repository Interface

The datastore is accessed through a repository interface that abstracts the storage backend:

```typescript
interface UserRepository {
  // Users
  createUser(user: CreateUserRequest): Promise<User>;
  getUser(userId: string): Promise<User | null>;
  updateUser(userId: string, updates: Partial<User>): Promise<User>;
  deleteUser(userId: string): Promise<void>;
  listUsers(): Promise<User[]>;
  isUserEnabled(userId: string): Promise<boolean>;

  // Identities
  addIdentity(userId: string, channelType: string, channelIdentity: string): Promise<void>;
  removeIdentity(userId: string, channelType: string): Promise<void>;
  resolveIdentity(channelType: string, channelIdentity: string): Promise<string | null>; // Returns userId

  // Allowed list
  isAllowed(userId: string, channelType: string): Promise<boolean>;
  setAllowed(userId: string, channelType: string, enabled: boolean): Promise<void>;
  getAllowedUsers(channelType: string): Promise<string[]>;

  // Policies
  getUserPolicy(userId: string, policyType: string): Promise<PolicyData | null>;
  setUserPolicy(userId: string, policyType: string, data: PolicyData): Promise<void>;

  // Agents
  provisionAgent(userId: string, label: string, tokenHash: string, tokenPrefix: string): Promise<Agent>;
  getAgent(agentId: string): Promise<Agent | null>;
  getAgentByTokenHash(tokenHash: string): Promise<Agent | null>;
  listAgents(userId?: string): Promise<Agent[]>;
  updateAgentStatus(agentId: string, status: AgentStatus, meta?: AgentRegistrationMeta): Promise<void>;
  setAgentRmqUsername(agentId: string, rmqUsername: string): Promise<void>;
  rotateAgentToken(agentId: string, newTokenHash: string, newTokenPrefix: string): Promise<void>;
  deleteAgent(agentId: string): Promise<void>;

  // Audit
  logAdminAction(entry: AdminAuditEntry): Promise<void>;
  queryAuditLog(filters: AuditQueryFilters): Promise<AdminAuditEntry[]>;
}
```

This interface can be backed by SQLite (default), PostgreSQL, or any other storage engine. The Gateway calls these methods; the storage implementation is pluggable.

### Caching

User and identity lookups are extremely hot paths — every inbound message triggers a `resolveIdentity()` call. The datastore uses an in-memory cache with short TTL:

| Query | Cache TTL | Invalidation |
|-------|----------|-------------|
| `resolveIdentity()` | 60 seconds | On `addIdentity()` / `removeIdentity()` |
| `isAllowed()` | 60 seconds | On `setAllowed()` |
| `isUserEnabled()` | 60 seconds | On `updateUser()` / `deleteUser()` |
| `getUserPolicy()` | 300 seconds | On `setUserPolicy()` |
| `getAgentByTokenHash()` | 60 seconds | On `rotateAgentToken()` / `updateAgentStatus()` / `deleteAgent()` |

With 1–3 users, the entire user table fits in memory. The cache is a `Map<string, CacheEntry>` — no external cache infrastructure needed.

### Schema Migrations

Migrations are embedded in the application binary and run automatically at startup:

```
migrations/
├── 001_initial_schema.sql
├── 002_add_allowed_list.sql
├── 003_add_user_policies.sql
├── 004_add_agents.sql
└── ...
```

Each migration is idempotent. A `schema_migrations` table tracks which migrations have been applied:

```sql
CREATE TABLE schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT NOT NULL
);
```

### Backup and Recovery

SQLite backup is straightforward:

```bash
# Hot backup (while system is running, WAL mode)
sqlite3 ~/.open-nipper/nipper.db ".backup ~/.open-nipper/backups/nipper-$(date +%Y%m%d).db"

# Or just copy the file (safe with WAL mode if no writers are active)
cp ~/.open-nipper/nipper.db ~/.open-nipper/backups/
```

The Gateway can also expose a backup endpoint on the admin API:

```
POST /admin/backup
→ Creates a backup of nipper.db to the configured backup directory
```

## Tier 2: Config Files (Generic Parameters)

### Config File Location

```
~/.open-nipper/config.yaml          # Primary config file
~/.open-nipper/config.local.yaml    # Local overrides (gitignored)
```

Environment variables override config file values. The precedence order is:

```
Environment variables > config.local.yaml > config.yaml > defaults
```

### What Belongs in Config Files

Config files hold **static operational parameters** that are defined at deployment time and rarely change. They do **not** hold user data — that lives in the database.

| Setting | Config File | Database | Why |
|---------|------------|----------|-----|
| Gateway bind address/port | Yes | No | Infrastructure, requires restart |
| Channel adapter settings (Wuzapi URL, MQTT broker) | Yes | No | Infrastructure, requires restart |
| RabbitMQ connection settings | Yes | No | Infrastructure, requires restart |
| Queue concurrency limits | Yes | No | Operational tuning |
| Agent health check settings | Yes | No | Operational tuning |
| Compaction thresholds | Yes | No | Operational tuning |
| Observability settings | Yes | No | Operational tuning |
| Security sandbox settings | Yes | No | Security policy, requires restart |
| Cron job schedules | Yes | No | Operational, requires restart |
| Rate limit defaults | Yes | No | Defaults, can be overridden per-user in DB |
| Default tool policies | Yes | No | Defaults, can be overridden per-user in DB |
| **User accounts** | No | **Yes** | Runtime-managed via admin API |
| **User identities** | No | **Yes** | Runtime-managed via admin API |
| **User allowlist** | No | **Yes** | Runtime-managed via admin API |
| **Per-user policies** | No | **Yes** | Runtime-managed via admin API |
| **Agent provisioning** | No | **Yes** | Runtime-managed via admin API |
| Agent registration settings | Yes | No | Operational tuning (rate limits, token rotation) |
| RabbitMQ Management API settings | Yes | No | Infrastructure, required for agent provisioning |

### Config File Structure

```yaml
# ~/.open-nipper/config.yaml

gateway:
  bind: "127.0.0.1"
  port: 18789
  admin:
    bind: "127.0.0.1"
    port: 18790                      # Separate port for admin API

channels:
  whatsapp:
    enabled: true
    adapter: "whatsapp"
    config:
      wuzapiBaseUrl: "http://localhost:8080"
      wuzapiToken: "${WUZAPI_USER_TOKEN}"
      wuzapiHmacKey: "${WUZAPI_HMAC_KEY}"
      # ... (see GATEWAY_ARCHITECTURE.md)

  slack:
    enabled: true
    adapter: "slack"
    config:
      appToken: "${SLACK_APP_TOKEN}"
      botToken: "${SLACK_BOT_TOKEN}"
      signingSecret: "${SLACK_SIGNING_SECRET}"

  mqtt:
    enabled: true
    adapter: "mqtt"
    config:
      broker: "mqtt://localhost:1883"
      # ...

  rabbitmq:
    enabled: true
    adapter: "rabbitmq"
    config:
      url: "amqp://localhost:5672"
      # ...

  cron:
    enabled: true
    adapter: "cron"
    jobs:
      - id: "daily-report"
        schedule: "0 9 * * *"
        userId: "user-02"
        prompt: "Check server logs and report anomalies"
        notifyChannel: "slack:C0789GHI"

queue:
  transport: "rabbitmq"
  rabbitmq:
    url: "amqp://localhost:5672"
    username: "${RABBITMQ_USERNAME}"
    password: "${RABBITMQ_PASSWORD}"
    vhost: "/nipper"
    # ... (see QUEUE_ARCHITECTURE.md)

  defaultMode: "steer"

agents:
  health_check_interval_seconds: 30
  consumer_timeout_seconds: 60       # Mark degraded if no consumer on user queue

  registration:
    enabled: true                     # Enable the /agents/register endpoint
    rate_limit: 10                    # Max registrations per minute per token
    token_rotation_on_register: true  # Rotate RMQ password on each registration

  rabbitmq_management:
    url: "http://localhost:15672"
    username: "${RABBITMQ_MGMT_USERNAME}"
    password: "${RABBITMQ_MGMT_PASSWORD}"

observability:
  enabled: true
  events:
    include: ["*"]
    exclude: []
  sanitizer:
    piiRedaction: true
    credentialDetection: true
    secretScrubbing: true

security:
  rateLimit:
    perUser:
      messagesPerMinute: 20
      messagesPerHour: 200
  tools:
    policy:
      allow: ["read", "write", "edit", "exec", "memory_read", "memory_write"]
      deny: ["session_spawn", "message"]

datastore:
  path: "~/.open-nipper/nipper.db"
  wal_mode: true
  busy_timeout_ms: 5000
  backup:
    enabled: true
    schedule: "0 2 * * *"           # Daily at 2 AM
    retention_days: 30
    path: "~/.open-nipper/backups/"
```

### Environment Variable Override Convention

Any config value can be overridden by an environment variable using the `NIPPER_` prefix and underscore-separated path:

```bash
NIPPER_GATEWAY_PORT=19000                   # Overrides gateway.port
NIPPER_AGENTS_HEALTH_CHECK_INTERVAL_SECONDS=60  # Overrides agents.health_check_interval_seconds
NIPPER_DATASTORE_PATH=/data/nipper.db       # Overrides datastore.path
```

Secrets are **always** provided via environment variables, never in config files:

```bash
WUZAPI_USER_TOKEN=mytoken
SLACK_BOT_TOKEN=xoxb-...
RABBITMQ_USERNAME=nipper
RABBITMQ_PASSWORD=secret
```

## Startup Sequence

```
1. Load config.yaml
2. Apply config.local.yaml overrides
3. Apply environment variable overrides
4. Open SQLite database (~/.open-nipper/nipper.db)
5. Run schema migrations (if needed)
6. Load users and identities into memory cache
7. Load allowed list into memory cache
8. Validate: at least one user exists
   └── If no users and first run → log warning, system starts but rejects all messages
9. Continue with Gateway startup (see GATEWAY_ARCHITECTURE.md)
```

## Migration Path

If Open-Nipper needs to scale beyond a single machine, the database can be migrated from SQLite to PostgreSQL:

1. The `UserRepository` interface remains the same
2. Swap the SQLite implementation for a PostgreSQL implementation
3. Migrate data using `sqlite3 .dump | psql`
4. Update `datastore` config to point to PostgreSQL connection string

The config files remain unchanged — they are read by whatever process is running, regardless of the database backend.
