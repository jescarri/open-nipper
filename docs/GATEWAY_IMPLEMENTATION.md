# Gateway Implementation Plan

## Overview

This document is the complete implementation plan for the Open-Nipper Gateway and CLI. The Gateway is a single Go binary that acts as the control plane for the entire platform. It handles all inbound channel traffic, routes messages to RabbitMQ agent queues, delivers agent responses back to channels, exposes an admin REST API, and provides a WebSocket interface for internal clients.

**Stack:** Go 1.23+, SQLite (via `mattn/go-sqlite3`), RabbitMQ (AMQP), Zap (structured logging), OpenTelemetry (tracing + metrics), Cobra (CLI).

---

## Part 1 — Project Bootstrap

### 1.1 Module and Directory Structure

```
open-nipper/
├── cmd/
│   └── nipper/
│       └── main.go                    # CLI entry point
├── pkg/
│   └── session/                       # Importable session library for Go agents
│       ├── types.go                   # Session, SessionMetadata, TranscriptLine, etc.
│       ├── key.go                     # BuildSessionKey, ParseSessionKey, SanitizeSessionID
│       ├── store.go                   # Filesystem-backed SessionStore
│       ├── lock.go                    # FileLock (hard-link technique)
│       └── compaction.go              # Transcript compactor
├── internal/
│   ├── config/                        # Config loading, validation, env-var overrides
│   │   ├── config.go
│   │   ├── loader.go
│   │   └── defaults.go
│   ├── logger/                        # Zap logger initialization, context helpers
│   │   └── logger.go
│   ├── telemetry/                     # OpenTelemetry tracing + metrics setup
│   │   ├── tracer.go
│   │   ├── metrics.go
│   │   └── noop.go
│   ├── datastore/                     # Repository interface + SQLite implementation
│   │   ├── interface.go
│   │   ├── sqlite/
│   │   │   ├── store.go
│   │   │   ├── users.go
│   │   │   ├── identities.go
│   │   │   ├── allowlist.go
│   │   │   ├── policies.go
│   │   │   ├── agents.go
│   │   │   └── audit.go
│   │   ├── cache.go
│   │   └── migrations/
│   │       ├── 001_initial_schema.sql
│   │       ├── 002_add_allowed_list.sql
│   │       ├── 003_add_user_policies.sql
│   │       └── 004_add_agents.sql
│   ├── models/                        # Canonical data model types
│   │   ├── message.go                 # NipperMessage, NipperEvent, NipperResponse
│   │   ├── channel.go                 # ChannelType, ChannelMeta, DeliveryContext
│   │   ├── queue.go                   # QueueItem, QueueMode, ControlMessage
│   │   ├── session.go                 # Session, SessionMetadata, ContextUsage
│   │   └── user.go                    # User, Identity, AllowlistEntry, Agent, Policy
│   ├── channels/                      # Channel adapters
│   │   ├── adapter.go                 # ChannelAdapter interface
│   │   ├── whatsapp/
│   │   │   ├── adapter.go
│   │   │   ├── normalizer.go
│   │   │   ├── delivery.go
│   │   │   └── hmac.go
│   │   ├── slack/
│   │   │   ├── adapter.go
│   │   │   ├── normalizer.go
│   │   │   ├── delivery.go
│   │   │   └── hmac.go
│   │   ├── cron/
│   │   │   ├── adapter.go
│   │   │   └── scheduler.go
│   │   ├── mqtt/
│   │   │   ├── adapter.go
│   │   │   ├── normalizer.go
│   │   │   └── delivery.go
│   │   └── rabbitmq/
│   │       ├── adapter.go
│   │       ├── normalizer.go
│   │       └── delivery.go
│   ├── queue/                         # Internal RabbitMQ queue system (Gateway↔Agent)
│   │   ├── broker.go                  # Connection management, topology declaration
│   │   ├── publisher.go               # Gateway → nipper.sessions exchange
│   │   ├── consumer.go                # Gateway consumes nipper-events-gateway
│   │   ├── control.go                 # Control message publisher
│   │   ├── management.go              # RabbitMQ Management API client
│   │   └── topology.go                # Exchange, queue, binding declarations
│   ├── allowlist/                     # Allowlist guard
│   │   └── guard.go
│   ├── security/                      # Startup audit, runtime checks
│   │   ├── audit.go
│   │   └── runtime.go
│   ├── gateway/                       # HTTP server, WebSocket, event routing
│   │   ├── server.go                  # Main HTTP server (:18789)
│   │   ├── webhook.go                 # /webhook/whatsapp, /webhook/slack handlers
│   │   ├── register.go                # /agents/register handler
│   │   ├── websocket.go               # /ws WebSocket handler + JSON-RPC router
│   │   ├── router.go                  # Message pipeline orchestrator
│   │   ├── resolver.go                # Session key derivation (pure computation)
│   │   ├── registry.go                # In-memory DeliveryContext tracking
│   │   ├── dispatcher.go              # Event dispatch → channel adapters
│   │   └── dedup.go                   # In-memory deduplication cache
│   ├── admin/                         # Admin API server (:18790)
│   │   ├── server.go
│   │   ├── users.go
│   │   ├── identities.go
│   │   ├── allowlist.go
│   │   ├── policies.go
│   │   ├── agents.go
│   │   └── system.go
│   └── ratelimit/                     # Per-user rate limiting
│       └── limiter.go
├── cli/                               # Cobra command definitions
│   ├── root.go
│   ├── serve.go
│   ├── admin/
│   │   ├── root.go
│   │   ├── user.go
│   │   ├── identity.go
│   │   ├── allowlist.go
│   │   ├── agent.go
│   │   └── backup.go
│   ├── logs/
│   │   └── tail.go
│   └── plugins/
│       ├── install.go
│       ├── list.go
│       ├── remove.go
│       ├── create.go
│       ├── test.go
│       └── validate.go
├── go.mod
├── go.sum
└── config.example.yaml
```

### 1.2 Go Module Dependencies

```go
// go.mod (key dependencies)
module github.com/jescarri/open-nipper

go 1.23

require (
    // Logging
    go.uber.org/zap v1.27.0

    // OpenTelemetry
    go.opentelemetry.io/otel v1.33.0
    go.opentelemetry.io/otel/sdk v1.33.0
    go.opentelemetry.io/otel/sdk/metric v1.33.0
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.33.0
    go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.33.0
    go.opentelemetry.io/otel/exporters/prometheus v0.55.0

    // Database
    github.com/mattn/go-sqlite3 v1.14.24

    // RabbitMQ
    github.com/rabbitmq/amqp091-go v1.10.0

    // MQTT
    github.com/eclipse/paho.mqtt.golang v1.5.0

    // HTTP / WebSocket
    github.com/gorilla/mux v1.8.1
    github.com/gorilla/websocket v1.5.3

    // CLI
    github.com/spf13/cobra v1.8.1
    github.com/spf13/viper v1.19.0

    // Config / YAML
    gopkg.in/yaml.v3 v3.0.1

    // Utilities
    github.com/google/uuid v1.6.0
    github.com/robfig/cron/v3 v3.0.1        // Cron scheduler
    github.com/patrickmn/go-cache v2.1.0    // In-memory cache
    golang.org/x/crypto v0.31.0             // For token generation
)
```

---

## ✅ CHECKPOINT: Phase 1 Complete

**Implemented:**
- `go.mod` with all dependencies pinned to Go 1.21-compatible versions
- `internal/config/` — Config struct, YAML loader, viper-based env overrides (`NIPPER_` prefix), `${ENV_VAR}` placeholder resolution, config validation, `~` path expansion
- `internal/logger/` — Zap JSON logger with `NIPPER_LOG_LEVEL` env control, `WithMessage` helper
- `internal/telemetry/` — Noop providers (default), OTLP trace + metrics exporters, Prometheus exporter, `GaugeValue` for observable gauges
- `internal/models/` — All canonical types: `NipperMessage`, `NipperEvent`, `NipperResponse`, `ChannelType`, `ChannelMeta` variants, `QueueItem`, `ControlMessage`, `User`, `Agent`, `Session`, etc.
- `config.example.yaml` — Complete example configuration

**All unit tests pass** (`internal/config`, `internal/logger`, `internal/telemetry`).

---

## Part 2 — Configuration System

### 2.1 Config File Structure (`config.example.yaml`)

The complete config file mirrors the architecture documents exactly:

```yaml
gateway:
  bind: "127.0.0.1"
  port: 18789
  read_timeout_seconds: 30
  write_timeout_seconds: 30
  admin:
    enabled: true
    bind: "127.0.0.1"
    port: 18790
    auth:
      enabled: false
      token: "${ADMIN_API_TOKEN}"

channels:
  whatsapp:
    enabled: false
    config:
      wuzapi_base_url: "http://localhost:8080"
      wuzapi_token: "${WUZAPI_USER_TOKEN}"
      wuzapi_hmac_key: "${WUZAPI_HMAC_KEY}"
      wuzapi_instance_name: "nipper-wa"
      webhook_path: "/webhook/whatsapp"
      events: ["Message", "ReadReceipt", "ChatPresence", "Connected", "Disconnected"]
      delivery:
        mark_as_read: true
        show_typing: true
        quote_original: true

  slack:
    enabled: false
    config:
      app_token: "${SLACK_APP_TOKEN}"
      bot_token: "${SLACK_BOT_TOKEN}"
      signing_secret: "${SLACK_SIGNING_SECRET}"
      webhook_path: "/webhook/slack"

  cron:
    enabled: false
    jobs: []

  mqtt:
    enabled: false
    config:
      broker: "mqtt://localhost:1883"
      client_id: "open-nipper-gateway"
      username: "${MQTT_USERNAME}"
      password: "${MQTT_PASSWORD}"
      topic_prefix: "nipper"
      qos: 1
      clean_session: false
      keep_alive: 60
      reconnect:
        enabled: true
        initial_delay_ms: 1000
        max_delay_ms: 30000

  rabbitmq_channel:
    enabled: false
    config:
      url: "amqp://localhost:5672"
      username: "${RABBITMQ_USERNAME}"
      password: "${RABBITMQ_PASSWORD}"
      vhost: "/nipper"
      exchange_inbound: "nipper.inbound"
      exchange_outbound: "nipper.outbound"
      exchange_dlx: "nipper.dlx"
      prefetch: 1
      heartbeat: 60
      reconnect:
        enabled: true
        initial_delay_ms: 1000
        max_delay_ms: 30000

queue:
  transport: "rabbitmq"
  rabbitmq:
    url: "amqp://localhost:5672"
    username: "${RABBITMQ_USERNAME}"
    password: "${RABBITMQ_PASSWORD}"
    vhost: "/nipper"
    heartbeat: 60
    reconnect:
      enabled: true
      initial_delay_ms: 1000
      max_delay_ms: 30000
  default_mode: "steer"
  per_channel:
    whatsapp:
      mode: "collect"
      debounce_ms: 2000
      collect_cap: 5
    slack:
      mode: "steer"
      debounce_ms: 500
    cron:
      mode: "queue"
      priority: -1
    mqtt:
      mode: "queue"
    rabbitmq:
      mode: "queue"

agents:
  health_check_interval_seconds: 30
  consumer_timeout_seconds: 60
  registration:
    enabled: true
    rate_limit: 10
    token_rotation_on_register: true
  rabbitmq_management:
    url: "http://localhost:15672"
    username: "${RABBITMQ_MGMT_USERNAME}"
    password: "${RABBITMQ_MGMT_PASSWORD}"

observability:
  enabled: true
  sanitizer:
    pii_redaction: true
    credential_detection: true
    secret_scrubbing: true

security:
  rate_limit:
    per_user:
      messages_per_minute: 20
      messages_per_hour: 200
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
    schedule: "0 2 * * *"
    retention_days: 30
    path: "~/.open-nipper/backups/"

telemetry:
  tracing:
    enabled: false
    exporter: "otlp"             # "otlp" | "stdout" | "none"
    endpoint: "${OTEL_EXPORTER_OTLP_ENDPOINT}"
    service_name: "open-nipper-gateway"
    sample_rate: 1.0
  metrics:
    enabled: false
    exporter: "prometheus"        # "prometheus" | "otlp" | "none"
    prometheus_port: 9090
    endpoint: "${OTEL_EXPORTER_OTLP_METRICS_ENDPOINT}"
```

### 2.2 Config Loading Logic

- Load `~/.open-nipper/config.yaml` as base
- Overlay `~/.open-nipper/config.local.yaml` if it exists
- Apply environment variable overrides using the `NIPPER_` prefix + underscore-separated path
  - Example: `NIPPER_GATEWAY_PORT=19000` overrides `gateway.port`
  - Example: `NIPPER_DATASTORE_PATH=/data/nipper.db` overrides `datastore.path`
- Inline `${ENV_VAR}` references in config values are resolved from the process environment at load time
- Config struct is populated once at startup and passed as a pointer throughout the process
- If the config file does not exist on first run, defaults are used and a warning is logged; the process does not fail

### 2.3 Config Validation

Validate the following after loading:
- `gateway.bind` must not be `"0.0.0.0"` (warn if it is; the security audit also checks this)
- `gateway.port` and `gateway.admin.port` must be different
- If any channel is enabled, its required config fields must be present
- `queue.rabbitmq.url` must be present
- `datastore.path` must be resolvable (expand `~`)
- `telemetry.tracing.enabled` and `telemetry.metrics.enabled` independently guard their sections; missing OTEL config with telemetry disabled must not produce any errors or log noise

---

## ✅ CHECKPOINT: Phase 2 Complete

**Implemented:**
- `internal/datastore/interface.go` — `Repository` interface with all methods
- `internal/datastore/sqlite/` — Full SQLite implementation: users, identities, allowlist, policies, agents, audit, backup
- `internal/datastore/sqlite/migrations/` — 4 SQL migrations (initial schema, allowed_list, user_policies, agents + audit)
- `internal/datastore/cache.go` — `CachedRepository` wrapping `Repository` with TTL cache (go-cache); cache-invalidating writes
- All canonical model types in `internal/models/` (message, channel, queue, user, session)

**All unit tests pass** (`internal/datastore/sqlite`: 13 tests passing).

---

## ✅ CHECKPOINT: Phase 3 Complete

**Implemented:** `internal/queue/` — RabbitMQ broker connection, topology declaration, publisher, event consumer, and Management API client.

### Files created

| File | Purpose |
|------|---------|
| `internal/queue/channel.go` | `AMQPChannel` interface (subset of `*amqp.Channel`) enabling mock-based testing without a live broker |
| `internal/queue/broker.go` | `Broker` — two AMQP connections (publish + consume), exponential-backoff reconnect watchdogs, `OnReconnect` callbacks, `buildAMQPURL` helper with correct `RawPath` encoding for vhosts containing `/` |
| `internal/queue/topology.go` | `DeclareTopology` (4 exchanges, 2 static queues, 2 bindings), `DeclareUserQueues` (per-user agent + control queues with DLX/TTL/overflow args), `UserAgentQueue`/`UserControlQueue` name helpers |
| `internal/queue/publisher.go` | `Publisher` interface + `RabbitMQPublisher` — `PublishMessage` (routing key `nipper.sessions.{userId}.{sessionId}`, Persistent delivery, typed AMQP headers), `PublishControl` (routing key `nipper.control.{userId}`), channel auto-reconnect on error |
| `internal/queue/consumer.go` | `EventConsumer` interface + `RabbitMQConsumer` — consumes `nipper-events-gateway` with prefetch 50, JSON-decodes `NipperEvent`, manual ack on handler success, nack-without-requeue on handler error or malformed body, graceful `Stop()` |
| `internal/queue/management.go` | `ManagementClient` interface + `HTTPManagementClient` — HTTP Basic Auth client for RabbitMQ Management API: `CreateUser`, `DeleteUser`, `SetVhostPermissions`, `GetQueueInfo`, `ListQueues`; 10-second timeout; correct `%2F` vhost encoding in URL paths |

### Unit tests (57 passing, 0 failing)

- **topology_test.go** — 15 tests: all 4 exchanges declared with correct kinds/durable, static queues, bindings, per-user queue args/bindings, error propagation
- **publisher_test.go** — 16 tests: routing key format, Persistent delivery mode, AMQP headers, MessageId, ContentType, body round-trip, nil guards, channel error recovery, control message routing; AMQP URL building (basic, credentials, vhost, slash-vhost, empty)
- **consumer_test.go** — 11 tests: handler dispatch, ack on success, nack on handler error, nack on malformed JSON, no-requeue guarantee, handler-required guard, idempotent Stop, sequential dispatch
- **management_test.go** — 15 tests: `CreateUser` PUT + Basic Auth, 4xx error, `DeleteUser` DELETE, 404 error, `SetVhostPermissions` with `%2F` vhost, `GetQueueInfo` response parsing, 404 error, `ListQueues` response parsing, 500 error, `percentEncodeVHost` unit tests, interface compliance, unreachable server

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`).

---

## ✅ CHECKPOINT: Phase 4 Complete

**Implemented:** Admin API server (`:18790`), all admin endpoints, CLI `admin` subcommands, and the `nipper serve` entry point.

### Files created

| File | Purpose |
|------|---------|
| `internal/admin/server.go` | `Server` struct, gorilla/mux router, `Start`/`Stop`/`Handler`, JSON response helpers (`writeOK`, `writeCreated`, `writeError`), logging middleware, optional Bearer token auth middleware, audit helper |
| `internal/admin/users.go` | `POST/GET/PUT/DELETE /admin/users` — full user CRUD; `DELETE` cleans up RMQ users for all agents |
| `internal/admin/identities.go` | `POST/GET/DELETE /admin/users/{userId}/identities` — identity CRUD |
| `internal/admin/allowlist.go` | `POST/GET/PUT/DELETE /admin/allowlist` — allowlist management; wildcard `*` channel type supported |
| `internal/admin/policies.go` | `GET/PUT/DELETE /admin/users/{userId}/policies/{type}` — policy management |
| `internal/admin/agents.go` | `POST/GET/DELETE /admin/agents`, rotate/revoke endpoints; `generateToken()` — 48-byte crypto-random base62, `npr_` prefix, SHA-256 hash stored |
| `internal/admin/system.go` | `GET /admin/health`, `GET /admin/audit`, `POST /admin/backup`, `GET /admin/config` (secrets redacted); `buildAuditEntry` constructor |
| `internal/admin/admin_test.go` | 30 tests covering all endpoints, auth middleware, token uniqueness |
| `cmd/nipper/main.go` | Binary entry point |
| `cli/root.go` | Root `nipper` command with global `--config`, `--log-level`, `--admin-url` flags |
| `cli/serve.go` | `nipper serve` — full startup sequence: config load → logger → telemetry → SQLite → RMQ management → admin server → signal wait |
| `cli/admin/root.go` | `nipper admin` parent command |
| `cli/admin/client.go` | Shared HTTP client + `doRequest` helper for CLI → admin API calls |
| `cli/admin/user.go` | `nipper admin user [add\|list\|get\|update\|delete]` |
| `cli/admin/identity.go` | `nipper admin identity [add\|list\|remove]` |
| `cli/admin/allowlist.go` | `nipper admin [allow\|deny]`, `nipper admin allowlist [show\|remove]` |
| `cli/admin/agent.go` | `nipper admin agent [provision\|list\|get\|rotate-token\|revoke\|delete]` |
| `cli/admin/backup.go` | `nipper admin [backup\|health\|audit]` |

### Unit tests (30 passing, 0 failing)

- **Users**: create (happy + missing id + missing name), list, get, get-not-found, update, delete
- **Identities**: full CRUD cycle, missing field validation
- **Allowlist**: full CRUD, by-channel filter, wildcard `*` channel
- **Policies**: set/list/delete cycle
- **Agents**: provision (happy + user-not-found + missing-label), list, get, rotate-token (new ≠ old), revoke (status=revoked), delete
- **System**: health endpoint (datastore=ok), audit log (populated after user create), audit action filter, config endpoint
- **Auth middleware**: rejects without token, rejects wrong token, allows correct token
- **Token uniqueness**: 5 tokens provisioned — all unique, all `npr_` prefixed

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests).

---

## Part 3 — Logger

### 3.1 Initialization

- Initialize a single `*zap.Logger` in `main.go` using `zap.NewProduction()` for JSON output
- Log level controlled by the `NIPPER_LOG_LEVEL` environment variable (`debug`, `info`, `warn`, `error`); default is `info`
- The logger is passed as a field in a context struct that propagates to all subsystems — never use a global logger variable; always accept `*zap.Logger` as a parameter
- All log entries include: `service: "open-nipper-gateway"`, `env: $NIPPER_ENV`

### 3.2 Structured Fields (required across all packages)

Every log call that relates to a message MUST include:
- `userId` (when resolved)
- `sessionKey` (when available)
- `channelType`
- `traceId` (from OpenTelemetry span context, when telemetry is enabled)

Example pattern:
```go
logger.Info("message published to queue",
    zap.String("userId", msg.UserID),
    zap.String("sessionKey", msg.SessionKey),
    zap.String("channelType", string(msg.ChannelType)),
    zap.String("queueName", queueName),
)
```

### 3.3 Log Levels

| Level | Usage |
|-------|-------|
| `debug` | Per-message trace, queue operations, cache hits/misses |
| `info`  | Server start/stop, channel connect/disconnect, user actions |
| `warn`  | Allowlist rejections, degraded agent status, auth failures, non-critical config issues |
| `error` | Datastore failures, RabbitMQ disconnects, channel delivery failures |

---

## Part 4 — OpenTelemetry

### 4.1 Tracing

**Initialization:**
- If `telemetry.tracing.enabled: false` (default), install a `noop` TracerProvider; the rest of the code calls `otel.Tracer()` without checking — no errors, no log noise
- If `telemetry.tracing.enabled: true`, configure the OTLP HTTP exporter using `telemetry.tracing.endpoint` (falls back to `OTEL_EXPORTER_OTLP_ENDPOINT`)
- SDK sampler: `tracesdk.ParentBased(tracesdk.TraceIDRatioBased(sampleRate))`
- Resource attributes: `service.name`, `service.version`, `deployment.environment`
- If the OTLP endpoint is unreachable at startup, log a single `warn` and continue with the noop provider

**Instrumentation points:**
- HTTP handlers: start a span on every incoming request (wrap with `otelhttp.NewHandler`)
- RabbitMQ publish: inject W3C trace context into AMQP message headers
- RabbitMQ consume: extract trace context from AMQP message headers
- Datastore queries: create child spans for every SQL operation
- Channel adapter outbound calls: span for each Wuzapi/Slack API call

### 4.2 Metrics

**Initialization:**
- If `telemetry.metrics.enabled: false` (default), install a noop MeterProvider
- If `telemetry.metrics.enabled: true` and `exporter: "prometheus"`, start a Prometheus HTTP handler on `telemetry.metrics.prometheus_port`
- If `exporter: "otlp"`, configure the OTLP metric exporter

**Metric instruments (all with `nipper_` prefix):**

| Metric | Type | Labels |
|--------|------|--------|
| `nipper_messages_received_total` | Counter | `channel_type`, `user_id` |
| `nipper_messages_rejected_total` | Counter | `channel_type`, `reason` |
| `nipper_messages_published_total` | Counter | `channel_type`, `user_id` |
| `nipper_events_consumed_total` | Counter | `event_type`, `user_id` |
| `nipper_responses_delivered_total` | Counter | `channel_type`, `user_id` |
| `nipper_queue_depth` | Gauge | `user_id` |
| `nipper_agent_consumer_count` | Gauge | `user_id` |
| `nipper_http_request_duration_seconds` | Histogram | `method`, `path`, `status` |
| `nipper_allowlist_cache_hit_total` | Counter | (none) |
| `nipper_allowlist_cache_miss_total` | Counter | (none) |
| `nipper_rmq_publish_errors_total` | Counter | `queue` |

---

## ✅ CHECKPOINT: Phase 5 Complete (Revised — Distributed Architecture)

**Implemented:** Allowlist guard, session key resolver (gateway-only, pure computation), in-memory DeliveryContext registry, and agent-side session library (`pkg/session/`).

> **Architecture note:** Phase 5 was originally implemented with the session store,
> file locking, and compaction inside the gateway (`internal/session/`).  This was
> refactored to align with the distributed agent architecture: agents own all
> filesystem session operations, the gateway only derives session keys and tracks
> delivery context in memory.  The session code was moved to `pkg/session/` so Go
> agents can import it directly.

### Files created

**Gateway (internal/)**

| File | Purpose |
|------|---------|
| `internal/allowlist/guard.go` | `Guard` struct — `Check(ctx, userID, channelType, channelIdentity)` enforces three-step policy: unknown identity → reject, user disabled → reject, not in allowlist (channel or wildcard) → reject. All rejections return `(false, nil)` so HTTP callers always return 200. Every rejection writes a `message.rejected` audit entry with `[REDACTED]` channel identity. |
| `internal/allowlist/guard_test.go` | 12 tests covering all rejection paths, wildcard fallback, datastore errors, audit redaction, and log-failure safety. |
| `internal/gateway/resolver.go` | `Resolver` — pure session key derivation from `NipperMessage` channel metadata. No store dependency, no auto-creation. WhatsApp → chatJID; Slack → channelID [+threadTS]; Cron → jobID; MQTT → clientID; RabbitMQ → correlationID or routingKey. Uses `pkg/session.SanitizeSessionID` and `pkg/session.BuildSessionKey`. |
| `internal/gateway/resolver_test.go` | 16 tests: per-channel resolution rules, determinism, senderJID vs chatJID, thread isolation, error on empty userID, special character sanitization. |
| `internal/gateway/registry.go` | `Registry` — thread-safe in-memory `sessionKey → DeliveryContext` map. `Register`, `Lookup`, `Remove`, `EvictOlderThan` for stale entry housekeeping. Used by router (register) and dispatcher (lookup). |
| `internal/gateway/registry_test.go` | 7 tests: register/lookup, missing key, remove, overwrite, length, eviction by age. |

**Agent session library (pkg/session/) — importable by Go agents**

| File | Purpose |
|------|---------|
| `pkg/session/types.go` | Standalone types: `Session`, `SessionMetadata`, `TranscriptLine`, `ContextUsage`, `CreateSessionRequest`, `SessionStatus`. No dependency on `internal/models`. |
| `pkg/session/key.go` | `BuildSessionKey`, `ParseSessionKey`, `SanitizeSessionID` — session key utilities shared between gateway and agents. |
| `pkg/session/key_test.go` | 9 tests: parse/build round-trips, invalid keys, sanitize special chars and empty input. |
| `pkg/session/store.go` | `Store` / `SessionStore` interface — filesystem-backed session store. `CreateSession`, `GetSession`, `ListSessions`, `LoadTranscript` (buffered JSONL), `AppendTranscript` (under FileLock), `UpdateMeta` (atomic temp+rename), `ArchiveSession`, `ResetSession`. 45s TTL index cache. |
| `pkg/session/store_test.go` | 25 tests: CRUD lifecycle, concurrent appends (20 goroutines), archive, reset, index rebuild. |
| `pkg/session/lock.go` | `FileLock` — hard-link-based file locking. Stale lock detection (> 30 min). Exponential backoff (100ms → 800ms, max 10s). `CleanStaleLocks(dir)`. |
| `pkg/session/lock_test.go` | 9 tests: acquire/release, idempotent unlock, contested lock, stale/malformed override, context cancellation, concurrent serialization. |
| `pkg/session/compaction.go` | `Compactor` — archives older transcript lines atomically, rewrites active transcript under file lock. `ShouldCompact` utility. |
| `pkg/session/compaction_test.go` | 8 tests: no-op below threshold, trim counts, double-compact, metadata updated, append after compact. |

### Unit tests (81 new tests passing, 0 failing)

- **internal/allowlist**: 12 tests
- **internal/gateway** (resolver + registry): 23 tests
- **pkg/session** (key + lock + store + compaction): 51 tests

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests).

**Total passing tests across all packages: 168+, 0 failing.**

---

## ✅ CHECKPOINT: Phase 6 Complete

**Implemented:** Gateway main HTTP server (`:18789`), webhook handlers (WhatsApp + Slack), message pipeline router, deduplication cache, event dispatcher, and `ChannelAdapter` interface.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/adapter.go` | `ChannelAdapter` interface — `ChannelType()`, `Start`, `Stop`, `HealthCheck`, `NormalizeInbound`, `DeliverResponse`, `DeliverEvent`. All channel adapters implement this contract. |
| `internal/gateway/dedup.go` | `Deduplicator` — in-memory cache keyed by `(userId, strategy, rawKey)` with configurable TTL window. Strategies: `message-id` (origin message ID), `prompt` (SHA-256 of text), `none`. Background cleanup goroutine every 30s. `PromptHash` helper. |
| `internal/gateway/dedup_test.go` | 10 tests: none strategy, empty key, message-id strategy, prompt strategy, different-users independence, expiry after window, len, deterministic hash, evict-expired, stop idempotent. |
| `internal/gateway/router.go` | `Router` — central message pipeline orchestrator. Steps: normalise → assign UUID → resolve user identity (via `ResolveIdentity`) → allowlist guard → session key resolution → registry register → deduplication → queue mode selection → publish (or collect-buffer). Supports all 4 queue modes: `queue`, `steer`, `collect` (with debounce timer + cap), `interrupt` (sends control signal). |
| `internal/gateway/router_test.go` | 12 tests: happy path, nil message ignored, normalisation error, allowlist reject, deduplication suppression, publish error propagation, interrupt mode sends control, default queue mode, session key + registry populated, user resolution, collect-mode batching, QueueItem serialisability. |
| `internal/gateway/dispatcher.go` | `Dispatcher` — consumes `NipperEvent`s and routes them to the correct adapter via the `Registry`. Buffered mode (WhatsApp, MQTT, RabbitMQ): accumulates delta text, delivers assembled `NipperResponse` on `done`. Streaming mode (Slack, WebSocket): forwards events in real-time, delivers final response on `done`. Broadcast mode (cron): routes to all `notifyChannels`. WebSocket fan-out: `SubscribeWebSocket` / `UnsubscribeWebSocket`. Stale accumulator cleanup every 60s (5min timeout). |
| `internal/gateway/dispatcher_test.go` | 11 tests: buffered delta→done assembly, streaming event forwarding, error event, no-delivery-context handling, nil event, broadcast delivery to multiple channels, WebSocket fan-out, done removes registry, accumulator cleared on done, stop idempotent. |
| `internal/gateway/server.go` | `Server` — main HTTP server with gorilla/mux router. Routes: `POST /webhook/whatsapp`, `POST /webhook/slack`, `GET /health`. Configurable bind address, port, read/write timeouts. Logging middleware. Graceful shutdown with 30s deadline. `StartOnListener` for test support. |
| `internal/gateway/webhook.go` | `handleWebhookWhatsApp` — reads body (10MB limit), verifies HMAC-SHA256 (`X-Hmac-Signature`), filters non-Message events, routes to pipeline, always returns 200. `handleWebhookSlack` — reads body, verifies Slack signature (replay-safe, 5min window), handles `url_verification` challenge, filters non-message events, routes to pipeline, always returns 200. |
| `internal/gateway/webhook_test.go` | 15 tests: HMAC valid/invalid/empty-sig/empty-key/malformed-hex/no-prefix, Slack signature valid/replay-attack/missing-headers/bad-timestamp, health endpoint, WhatsApp webhook 200/non-message/bad-JSON, Slack URL verification, Slack message event, Slack non-message, 404 on unknown route. |

### Unit tests (48 new tests passing, 0 failing)

- **Deduplication**: 10 tests
- **Router**: 12 tests
- **Dispatcher**: 11 tests
- **Webhook + Server**: 15 tests

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests, `internal/allowlist`: 12 tests, `internal/gateway` resolver+registry: 23 tests, `pkg/session`: 51 tests).

**Total passing tests across all packages: 231+, 0 failing.**

---

## ✅ CHECKPOINT: Phase 7 Complete

**Implemented:** WhatsApp channel adapter (inbound + outbound), the first end-to-end channel. Full Wuzapi-backed adapter implementing the `ChannelAdapter` interface.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/whatsapp/adapter.go` | `Adapter` struct implementing `ChannelAdapter` — `ChannelType()`, `Start` (registers Wuzapi webhook), `Stop`, `HealthCheck` (pings Wuzapi `/session/status`), `NormalizeInbound` (delegates to normalizer), `DeliverResponse` (delegates to WuzapiClient), `DeliverEvent` (no-op — WhatsApp doesn't support streaming). `Validate()` config checker. |
| `internal/channels/whatsapp/normalizer.go` | `NormalizeInbound` — parses Wuzapi webhook JSON into `NipperMessage`. Handles all message types: `Conversation`, `ExtendedTextMessage`, `ImageMessage`, `AudioMessage`, `VideoMessage`, `DocumentMessage`, `StickerMessage`, `LocationMessage`, `ContactMessage`. Filters `IsFromMe: true` (self-messages) and non-Message events. Extracts `ContextInfo` for quoted messages. Sets `DeliveryContext` with WhatsApp capability matrix. S3 URL handling for media. |
| `internal/channels/whatsapp/delivery.go` | `WuzapiClient` — HTTP client for Wuzapi REST API. `DeliverResponse` orchestrates: typing indicator → send text/image/document → mark as read → clear typing. `sendText` with optional `ContextInfo` quoting. `sendImage`, `sendDocument`. `RegisterWebhook` for startup. `HealthCheck` via `/session/status`. Retry logic: 3 attempts with 1s backoff for transient errors. All requests include `Token` header. |
| `internal/channels/whatsapp/hmac.go` | `VerifyHMAC` — HMAC-SHA256 verification for Wuzapi webhook signatures. Tolerates `sha256=` prefix or bare hex. Uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks. |
| `internal/models/channel.go` | Enhanced `WhatsAppMeta` — added `WuzapiUserID`, `WuzapiInstanceName`, `WuzapiBaseURL`, `IsFromMe`, `HasMedia`, `MediaType`, `S3URL`, `MediaKey`, `QuotedMessageID`, `QuotedParticipant`. |

### Unit tests (56 new tests passing, 0 failing)

- **HMAC**: 8 tests — valid (with/without prefix), invalid, empty sig/key, malformed hex, wrong body, different key
- **Normalizer**: 19 tests — text conversation, self-message filtered, non-Message filtered, image with S3, image without caption, audio, video, document, sticker, location, contact, extended text with quoting, group message, invalid JSON, invalid message JSON, UUID generation, delivery context capabilities, timestamp parsing, serialisability
- **Delivery**: 14 tests — full delivery sequence (typing + text + markread + paused), quoting, auth token, disabled typing/markread/quoting, missing meta, image part, document part, retry on server error, webhook registration, health check OK/fail, phoneFromJID
- **Adapter**: 15 tests — channel type, interface compliance, start registers webhook, start with unreachable Wuzapi (warns but continues), stop no-op, health check OK/degraded, normalize text/self/non-message, deliver event no-op, deliver response nil-safe, config validation (missing URL/token/valid)

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests, `internal/allowlist`: 12 tests, `internal/gateway`: 48 tests, `pkg/session`: 51 tests).

**Total passing tests across all packages: 303, 0 failing.**

---

## ✅ CHECKPOINT: Phase 8 Complete

**Implemented:** Slack channel adapter (inbound + outbound with streaming support), the second end-to-end channel. Full Events API + Web API adapter implementing the `ChannelAdapter` interface.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/slack/adapter.go` | `Adapter` struct implementing `ChannelAdapter` — `ChannelType()`, `Start` (no-op, Slack pushes via webhook), `Stop`, `HealthCheck` (calls `auth.test`), `NormalizeInbound` (delegates to normalizer), `DeliverResponse` (delegates to SlackClient), `DeliverEvent` (streaming: create + update messages). `Validate()` config checker. |
| `internal/channels/slack/normalizer.go` | `NormalizeInbound` — parses Slack Events API JSON into `NipperMessage`. Handles `event_callback` with `message` events. Filters bot messages (`bot_id` present), filtered subtypes (`message_changed`, `message_deleted`, `channel_join`, etc.). Detects thread context: `thread_ts` → `replyMode: "thread"`. Supports file attachments (image, audio, video, document). Sets `DeliveryContext` with Slack capability matrix (streaming, markdown, threads, reactions, message edits). |
| `internal/channels/slack/delivery.go` | `SlackClient` — HTTP client for Slack Web API. `DeliverResponse` orchestrates: post message via `chat.postMessage` (with thread_ts), or update existing streaming message via `chat.update`. `DeliverEvent` handles streaming: creates message on first delta, updates on subsequent deltas, final update on done. `AddReaction` for emoji reactions. `AuthTest` for health checks. Retry logic: 3 attempts with 1s backoff. Per-session streaming state tracking. |
| `internal/channels/slack/hmac.go` | `VerifySignature` — Slack request signature verification per official spec. Validates `X-Slack-Request-Timestamp` (reject >5min for replay prevention). Computes `HMAC-SHA256("v0:" + timestamp + ":" + body, signingSecret)`. Compares with `X-Slack-Signature` header. Uses `crypto/subtle.ConstantTimeCompare`. |

### Model updates

| File | Change |
|------|--------|
| `internal/models/channel.go` | Enhanced `SlackMeta` — renamed `UserID` to `SlackUserID`, added `AppID`, `BotID`, `BotToken` (json:"-" for security). Removed `WorkspaceID` (redundant with `TeamID`). |

### Unit tests (56 new tests passing, 0 failing)

- **HMAC**: 8 tests — valid signature, invalid secret, replay attack (>5min), missing timestamp, missing signature, empty secret, bad timestamp, tampered body
- **Normalizer**: 17 tests — text message, threaded message, bot message filtered, message_changed filtered, url_verification ignored, non-message event, invalid JSON, empty user, SlackMeta fields, delivery context capabilities, file attachment, UUID generation, serialisability, channel_join filtered, non-event_callback, empty event, filtered subtypes coverage
- **Delivery**: 14 tests — postMessage (happy path), update existing stream message, missing meta, missing channel, delta creates new message, delta updates existing, nil event, auth.test success/failure, add reaction, already_reacted tolerance, clear stream state
- **Adapter**: 17 tests — channel type, interface compliance, start no-op, stop no-op, health check OK/failure/no-token, normalize text/bot/non-message, deliver response nil-safe, deliver event nil-safe, deliver response postMessage, deliver event ignores unknown types, validate valid/missing-token/missing-secret, SlackClient ref, error event posts warning

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests, `internal/allowlist`: 12 tests, `internal/gateway`: 48 tests, `internal/channels/whatsapp`: 56 tests, `pkg/session`: 51 tests).

**Total passing tests across all packages: 359, 0 failing.**

---

## ✅ CHECKPOINT: Phase 9 Complete

**Implemented:** Agent registration endpoint (`POST /agents/register`), per-key sliding window rate limiter, full registration flow with RabbitMQ credential provisioning.

### Files created

| File | Purpose |
|------|---------|
| `internal/gateway/register.go` | `RegisterHandler` — full agent auto-registration endpoint. Flow: extract Bearer token → SHA-256 hash lookup → validate agent not revoked → load user + check enabled → per-token-prefix rate limiting → generate RMQ password (32-byte crypto-random, base64url) → create/update RMQ user via Management API → set vhost permissions (configure/write/read scoped to user's queues and events exchange) → update agent status to "registered" → set RMQ username in datastore → audit log → return full config blob (RMQ credentials, queue names, exchanges, routing keys, user info, tool policies). Intentionally vague 401 errors for all auth failures. |
| `internal/gateway/register_test.go` | 27 tests covering all paths: happy path (full response validation), missing auth, malformed auth (Basic instead of Bearer), invalid token, revoked agent, user not found, user disabled (403), rate limiting (429 + Retry-After header), management API unavailable (503), RMQ user creation failure, RMQ permission setting failure, datastore update failure, user policies in response, fallback to default tools policy, exchange names, routing keys, user details, empty body OK, password uniqueness, password length, token hashing, Bearer token extraction (6 sub-tests), client IP extraction (4 sub-tests), vhost permission regex format, Content-Type, whitespace tolerance, audit on all failure paths (4 sub-tests), RMQ username format. |
| `internal/ratelimit/limiter.go` | `Limiter` — in-memory per-key sliding window rate limiter. `Allow(key)` records event + checks count, returns `(bool, retryAfter)`. `Reset`, `Count`, `Cleanup` for housekeeping. Thread-safe. |
| `internal/ratelimit/limiter_test.go` | 11 tests: under limit, exceeds limit, independent keys, window expiry, retry-after accuracy, reset, count, cleanup, concurrent access (20 goroutines × 50 events), zero-max edge case, cleanup preserves active entries. |

### Server wiring

| File | Change |
|------|--------|
| `internal/gateway/server.go` | Added `RegisterHandler` field to `Server` and `ServerDeps`. Route `POST /agents/register` registered when handler is provided. |

### Registration response format

```json
{
  "ok": true,
  "result": {
    "agent_id": "agt-user01-01",
    "user_id": "user-01",
    "user_name": "Alice",
    "rabbitmq": {
      "url": "amqp://...",
      "username": "agent-agt-user01-01",
      "password": "<generated>",
      "vhost": "/nipper",
      "queues": { "agent": "nipper-agent-user-01", "control": "nipper-control-user-01" },
      "exchanges": { "sessions": "nipper.sessions", "events": "nipper.events", "control": "nipper.control", "logs": "nipper.logs" },
      "routing_keys": { "events_publish": "nipper.events.user-01.{sessionId}", "logs_publish": "nipper.logs.user-01.{eventType}" }
    },
    "user": { "id": "user-01", "name": "Alice", "default_model": "claude-sonnet-4-20250514", "preferences": {} },
    "policies": { "tools": { "allow": [...], "deny": [...] } }
  }
}
```

### Error responses

| Scenario | HTTP Status | Response |
|----------|-------------|----------|
| Missing/malformed Authorization | 401 | `{"ok": false, "error": "unauthorized"}` |
| Invalid token (hash mismatch) | 401 | `{"ok": false, "error": "unauthorized"}` |
| Revoked agent | 401 | `{"ok": false, "error": "unauthorized"}` |
| User not found (cascade deleted) | 401 | `{"ok": false, "error": "unauthorized"}` |
| User disabled | 403 | `{"ok": false, "error": "user_disabled"}` |
| Rate limited | 429 | `{"ok": false, "error": "rate_limited", "retry_after": N}` |
| Management API unreachable | 503 | `{"ok": false, "error": "service_unavailable"}` |

### Unit tests (38 new tests passing, 0 failing)

- **Rate limiter** (`internal/ratelimit`): 11 tests
- **Registration handler** (`internal/gateway`): 27 tests (including sub-tests: 6 for Bearer token extraction, 4 for client IP, 4 for audit on failure paths)

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests, `internal/allowlist`: 12 tests, `internal/gateway`: 48 tests + 27 new, `internal/channels/whatsapp`: 56 tests, `internal/channels/slack`: 56 tests, `pkg/session`: 51 tests).

**Total passing tests across all packages: 397+, 0 failing.**

---

## ✅ CHECKPOINT: Phase 10 Complete

**Implemented:** Agent health monitor — background goroutine that periodically queries RabbitMQ Management API for per-user agent queue health, caches results in memory, and exposes them via the admin health endpoint.

### Files created

| File | Purpose |
|------|---------|
| `internal/gateway/healthmon.go` | `HealthMonitor` — background goroutine running every `agents.health_check_interval_seconds` (default 30s). Lists all users with non-revoked agents from datastore, queries `ManagementClient.GetQueueInfo` for each user's agent queue, derives status (`processing`/`idle`/`degraded`/`offline`), tracks degraded duration for timeout detection, updates `nipper_agent_consumer_count` metric gauge, caches results in thread-safe in-memory map. Exports `CheckAll` for test support. `GetStatus(userID)` returns a copy. `GetAllStatuses()` returns `[]models.AgentHealthInfo` for JSON serialization. |
| `internal/gateway/healthmon_test.go` | 25 tests covering all status derivation paths, multi-user scenarios, revoked-agent filtering, management API errors, stale entry removal, degraded persistence and recovery, metrics update, start/stop lifecycle, copy semantics, config-driven vhost. |

### Files modified

| File | Change |
|------|--------|
| `internal/models/user.go` | Added `AgentHealthInfo` struct — shared type for per-user agent queue health data consumed by admin health endpoint and WebSocket API. |
| `internal/admin/server.go` | Added `AgentHealthProvider` interface (`GetAllStatuses() []models.AgentHealthInfo`), `agentHealth` field on `Server`, `SetAgentHealthProvider` setter. |
| `internal/admin/system.go` | Updated `healthResult` to include optional `Agents []models.AgentHealthInfo` array. Health handler populates it when `AgentHealthProvider` is set. |
| `internal/admin/admin_test.go` | Added 2 tests: `TestHealthEndpoint_WithAgentHealth` (verifies agent data in response), `TestHealthEndpoint_WithoutAgentHealth` (verifies no agents field when provider is nil). |

### Agent status derivation

| Condition | Status |
|-----------|--------|
| `consumers > 0` and `messagesUnacknowledged > 0` | `processing` |
| `consumers > 0` and `messagesUnacknowledged == 0` | `idle` |
| `consumers == 0` and `messagesReady > 0` | `degraded` |
| `consumers == 0` and `messagesReady == 0` | `offline` |

### Unit tests (27 new tests passing, 0 failing)

- **Health monitor** (`internal/gateway`): 25 tests — status derivation (5), CheckAll scenarios (14: happy path, processing, degraded, offline, skip revoked, multiple users, mgmt error, list users error, list agents error, no users, no agents, mixed statuses, all revoked, default vhost), degraded persistence (2), stale removal (1), copy semantics (1), nil user (1), GetAllStatuses (2), metrics update (1), start/stop (2), config vhost (1)
- **Admin health** (`internal/admin`): 2 tests — with and without agent health provider

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 30 tests + 2 new, `internal/allowlist`: 12 tests, `internal/gateway`: 75 tests + 25 new, `internal/channels/whatsapp`: 56 tests, `internal/channels/slack`: 56 tests, `internal/ratelimit`: 11 tests, `pkg/session`: 51 tests).

**Total passing tests across all packages: 424+, 0 failing.**

---

## ✅ CHECKPOINT: Phase 11 Complete

**Implemented:** Cron adapter and scheduler — internal `robfig/cron` scheduler that fires NipperMessages into the message pipeline at configured intervals. Each cron job is validated against the datastore at startup (user existence/enabled check). Responses are delivered via broadcast to the job's `notifyChannels` by the dispatcher — the cron adapter itself never delivers outbound. The adapter implements the full `ChannelAdapter` interface. `NormalizeInbound` is available for manual/admin-triggered job invocations.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/cron/adapter.go` | `Adapter` — implements `channels.ChannelAdapter` for the cron channel. Constructor accepts `CronChannelConfig`, logger, and `UserValidator`. `Start` loads jobs and starts scheduler. `Stop` gracefully stops scheduler. `HealthCheck` detects when all configured jobs failed validation. `NormalizeInbound` converts JSON payload to `NipperMessage` for manual triggering. `DeliverResponse` and `DeliverEvent` are no-ops (headless channel; dispatcher broadcasts via `notifyChannels`). `SetHandler` wires the message pipeline handler. `Validate` checks config for missing fields and duplicate IDs. |
| `internal/channels/cron/scheduler.go` | `Scheduler` — wraps `robfig/cron/v3` (with seconds support). `LoadJobs` validates each job (ID, schedule, userID, prompt) and optionally verifies user existence via `UserValidator` callback. Invalid jobs are logged and skipped. `fireJob` constructs a `NipperMessage` with `channelType: cron`, `replyMode: broadcast`, `CronMeta`, and calls the registered handler with a 30-second context timeout. `Start`/`Stop` manage the cron goroutine lifecycle. `Jobs`/`JobCount` expose loaded job state (copy semantics). |
| `internal/channels/cron/adapter_test.go` | 36 tests covering: adapter interface compliance, `ChannelType`, `Start` with valid/no jobs, `HealthCheck` (ok when no jobs; error when all fail), `DeliverResponse`/`DeliverEvent` no-ops, `NormalizeInbound` (valid, unknown job, missing fields, invalid JSON), scheduler `LoadJobs` (valid, invalid schedule, missing fields, user validation fail/error, nil validator, partial failure), `Jobs` copy semantics, `fireJob` (handler called with correct fields, no handler, handler error), `StartStop` lifecycle, actual firing (timing test: ≥1 firing in 2.5s), `Validate` (valid, empty, duplicate ID, missing fields), `CronCapabilities`. |

### Files modified

| File | Change |
|------|--------|
| `internal/models/channel.go` | Added `CronCapabilities()` function returning the capability matrix for the cron channel (all capabilities `false` — headless, no streaming, no media). |

### Cron message flow

```
Config declares:
  channels.cron.jobs:
    - id: daily-report
      schedule: "0 0 9 * * *"
      user_id: alice
      prompt: "Generate daily server report"
      notify_channels: ["slack:C0789GHI"]
        │
        ▼
Scheduler validates userId against datastore at startup
        │
        ▼
Scheduler fires at 09:00:00 daily
        │
        ▼
Scheduler constructs NipperMessage:
  channelType: cron
  userId: alice
  sessionKey: user:alice:channel:cron:session:daily-report
  deliveryContext:
    replyMode: broadcast
    notifyChannels: [slack:C0789GHI]
  meta: { jobId: daily-report, scheduleAt: ... }
        │
        ▼
Handler calls Router.HandleMessage pipeline:
  resolve user → allowlist → session key → dedup → publish to RabbitMQ
        │
        ▼
Agent processes message, publishes NipperEvent (done) to nipper.events
        │
        ▼
Dispatcher sees replyMode: broadcast → delivers to all notifyChannels
```

### Unit tests (36 new tests passing, 0 failing)

- **Adapter** (`internal/channels/cron`): 36 tests — interface compliance (2), channelType (1), start/stop (3: no jobs, valid jobs, lifecycle), healthCheck (2: no jobs ok, all failed error), deliverResponse/deliverEvent no-ops (2), normalizeInbound (5: valid, unknown job, missing fields ×3, invalid JSON), scheduler loadJobs (10: valid, invalid schedule, missing ID/schedule/userId/prompt, user validation fail/error, nil validator, partial failure), jobs copy (1), fireJob (5: handler called, no handler, handler error, message fields, actual firing), validate (7: valid, empty, duplicate ID, missing ID/schedule/userId/prompt), cronCapabilities (1)

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 32 tests, `internal/allowlist`: 12 tests, `internal/gateway`: 100 tests, `internal/channels/whatsapp`: 56 tests, `internal/channels/slack`: 56 tests, `internal/ratelimit`: 11 tests, `pkg/session`: 51 tests, `internal/channels/cron`: 36 new tests).

**Total passing tests across all packages: 467, 0 failing.**

---

## ✅ CHECKPOINT: Phase 12 Complete

**Implemented:** MQTT channel adapter (inbound + outbound) — the fourth channel. Full `paho.mqtt.golang`-backed adapter implementing the `ChannelAdapter` interface with persistent connection, per-user topic subscriptions, auto-reconnect with resubscription, and JSON-based message delivery.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/mqtt/adapter.go` | `Adapter` — implements `channels.ChannelAdapter` for MQTT. Constructor accepts `MQTTConfig`, logger, `UserLister`, and optional `ClientFactory` (test seam). `Start` connects to MQTT broker via paho client, subscribes to per-user inbox topics. `Stop` disconnects gracefully. `HealthCheck` verifies broker connection. `NormalizeInbound` accepts wrapper JSON with `_topic` and `_payload` for manual/admin triggering. `DeliverResponse` delegates to `DeliveryClient`. `DeliverEvent` is no-op (non-streaming channel). `SetHandler` wires the message pipeline callback. `SubscribeUser`/`UnsubscribeUser` for dynamic subscription management. `onConnect` handler auto-resubscribes all users on reconnect. `onMessage` normalizes and dispatches to handler. `Validate` checks required config fields. `RedactBrokerURL` for safe logging. |
| `internal/channels/mqtt/normalizer.go` | `NormalizeInbound` — parses MQTT JSON payload into `NipperMessage`. Extracts `userId` from topic path (`{topicPrefix}/{userId}/inbox`). Populates `MqttMeta` with broker, topic, QoS, clientID, correlationID, and responseTopic. Generates UUID messageId. Returns `(nil, nil)` for empty/whitespace text. `ExtractUserIDFromTopic`, `OutboxTopic`, `InboxTopic` helper functions. |
| `internal/channels/mqtt/delivery.go` | `DeliveryClient` — publishes `NipperResponse` to MQTT topics. Resolves outbound topic: uses `responseTopic` from `MqttMeta` if set, otherwise falls back to `{topicPrefix}/{userId}/outbox`. Resolves QoS from inbound meta or defaults. `MQTTPublisher` interface for testability. `MQTTResponsePayload` struct with responseId, sessionKey, userId, text, parts, correlationId, timestamp. |
| `internal/channels/mqtt/normalizer_test.go` | 19 tests: text message (full field validation), responseTopic, custom messageId, empty text returns nil, whitespace returns nil, invalid JSON, invalid topic (wrong prefix, no userId), UUID uniqueness, capabilities matrix, serialisability, custom topic prefix, ExtractUserIDFromTopic (8 subtests), OutboxTopic, InboxTopic, auto-generated origin messageId. |
| `internal/channels/mqtt/delivery_test.go` | 13 tests: happy path (topic, QoS, payload validation), responseTopic override, fallback outbox without meta, nil response, nil publisher, not connected, publish error, payload with parts, QoS from meta, invalid QoS fallback, constructor QoS clamping, timestamp format. |
| `internal/channels/mqtt/adapter_test.go` | 27 tests: interface compliance (2), channelType (1), start connects and subscribes (1), connect error warns but continues (1), stop disconnects (1), healthCheck connected/not-connected/nil-client (3), normalizeInbound valid/missing-topic/missing-payload/invalid-JSON (4), deliverEvent no-op (1), deliverResponse nil-safe/publishes/not-started (3), onMessage handler-called/no-handler/normalize-error/empty-text (4), subscriptions copy semantics (1), subscriberCount (1), inbox/outbox topics (1), no-user-lister skips subscriptions (1), validate valid/missing-broker/missing-clientId/missing-topicPrefix (4), RedactBrokerURL (1), onConnect resubscribes all (1), MQTT capabilities (1). |

### MQTT message flow

```
Device publishes to MQTT broker:
  Topic: nipper/{userId}/inbox
  Payload: {"text": "sensor status", "correlationId": "corr-42", "responseTopic": "devices/sensor/response"}
        │
        ▼
MQTT Adapter (paho subscriber) receives message
        │
        ▼
NormalizeInbound extracts userId from topic, parses JSON payload
  → NipperMessage with channelType: mqtt, MqttMeta populated
        │
        ▼
Handler calls Router.HandleMessage pipeline:
  resolve user → allowlist → session key → dedup → publish to RabbitMQ
        │
        ▼
Agent processes message, publishes NipperEvent (done) to nipper.events
        │
        ▼
Dispatcher assembles NipperResponse, calls adapter.DeliverResponse
        │
        ▼
DeliveryClient publishes to:
  - MqttMeta.ResponseTopic (if set): "devices/sensor/response"
  - Otherwise: "nipper/{userId}/outbox"
```

### Unit tests (59 new tests passing, 0 failing)

- **Normalizer** (`internal/channels/mqtt`): 19 tests — text message, responseTopic, custom messageId, empty/whitespace text, invalid JSON, invalid topic (2), UUID uniqueness, capabilities, serialisability, custom prefix, ExtractUserIDFromTopic (8 subtests), OutboxTopic, InboxTopic, auto-generated origin ID
- **Delivery** (`internal/channels/mqtt`): 13 tests — happy path, responseTopic, fallback outbox, nil response, nil publisher, not connected, publish error, parts, QoS from meta, invalid QoS, constructor clamping, timestamp format
- **Adapter** (`internal/channels/mqtt`): 27 tests — interface compliance (2), channelType, start/stop, connect error, healthCheck (3), normalizeInbound (4), deliverEvent, deliverResponse (3), onMessage (4), subscriptions copy, subscriberCount, topics, no-user-lister, validate (4), RedactBrokerURL, reconnect resubscribe, capabilities

**All existing tests remain green** (`internal/config`, `internal/logger`, `internal/telemetry`, `internal/datastore/sqlite`, `internal/queue`: 57 tests, `internal/admin`: 32 tests, `internal/allowlist`: 12 tests, `internal/gateway`: 100 tests, `internal/channels/whatsapp`: 56 tests, `internal/channels/slack`: 56 tests, `internal/ratelimit`: 11 tests, `pkg/session`: 51 tests, `internal/channels/cron`: 36 tests).

**Total passing tests across all packages: 526, 0 failing.**

---

## ✅ CHECKPOINT: Phase 13 Complete

**Implemented:** RabbitMQ channel adapter (inbound + outbound) — the fifth and final channel. Full `amqp091-go`-backed adapter implementing the `ChannelAdapter` interface with persistent AMQP connection, per-user queue consumption, exchange topology declaration, dead-letter exchange support, auto-reconnect with exponential backoff, and JSON-based response delivery with reply-to and correlation-id support.

### Files created

| File | Purpose |
|------|---------|
| `internal/channels/rabbitmq/adapter.go` | `Adapter` — implements `channels.ChannelAdapter` for RabbitMQ service-to-service messaging. Constructor accepts `RabbitMQChanConfig`, logger, `UserLister`, and optional `ConnectionFactory` (test seam). `Start` connects to AMQP broker, declares inbound/outbound/DLX exchanges, creates per-user inbound queues with DLX binding, and starts consuming. `Stop` closes channel and connection. `HealthCheck` verifies connection. `NormalizeInbound` accepts wrapper JSON with `_body` and `_meta`. `DeliverResponse` delegates to `DeliveryClient`. `DeliverEvent` is no-op (non-streaming channel). `SetHandler` wires message pipeline. Auto-reconnect with exponential backoff and full topology re-setup. Proper message ack/nack handling. `Validate` checks required config fields. |
| `internal/channels/rabbitmq/normalizer.go` | `NormalizeInbound` — parses AMQP JSON payload into `NipperMessage`. Resolves userId from: (1) `x-nipper-user` header, (2) routing key `nipper.{userId}.inbox`, (3) queue name `nipper-{userId}-inbox`. Populates `RabbitMqMeta` with exchange, routingKey, correlationId, replyTo, messageId. Supports reply-to mode (direct queue publish) and direct mode (outbound exchange). Helper functions: `OutboundRoutingKey`, `InboundRoutingKey`, `InboundQueueName`, `OutboundQueueName`. |
| `internal/channels/rabbitmq/delivery.go` | `DeliveryClient` — publishes `NipperResponse` to RabbitMQ. Resolves target: uses `replyTo` from `RabbitMqMeta` for direct queue publish (empty exchange), otherwise publishes to outbound exchange with routing key `nipper.{userId}.outbox`. Sets AMQP headers: `x-nipper-response-id`, `content-type`, `x-correlation-id`. `AMQPPublisher` interface for testability. `RabbitMQResponsePayload` struct with responseId, inReplyTo, sessionKey, userId, text, parts, correlationId, timestamp. |
| `internal/channels/rabbitmq/normalizer_test.go` | 22 tests: text message, correlationId (payload and header, payload priority), replyTo routing, direct mode fallback, userId from header/queue/routing-key, empty/whitespace text, invalid JSON, no userId error, unique UUIDs, auto-generated origin ID, custom payload messageId, capabilities matrix, serialisability, meta fields, extractUserIDFromRoutingKey (6 subtests), extractUserIDFromQueue (5 subtests), helper functions. |
| `internal/channels/rabbitmq/delivery_test.go` | 9 tests: basic text delivery, replyTo direct publish, correlationId header, nil response, nil publisher, not connected, publish error, headers always present, originMessageId/inReplyTo. |
| `internal/channels/rabbitmq/adapter_test.go` | 11 tests: constructor, channelType, healthCheck not connected, isConnected initial, consumerCount initial, deliverEvent no-op, deliverResponse nil/no-client, setHandler, normalizeInbound valid/missing-body/invalid-JSON, validate (4 subtests), buildURL (4 subtests). |

### RabbitMQ channel message flow

```
Service publishes to AMQP exchange:
  Exchange: nipper.inbound
  Routing key: nipper.{userId}.inbox
  Headers: x-nipper-user, x-correlation-id
  Body: {"text": "analyze test results", "correlationId": "workflow-42"}
        │
        ▼
RabbitMQ Channel Adapter consumes from nipper-{userId}-inbox queue
        │
        ▼
NormalizeInbound resolves userId (header > routing key > queue name),
parses JSON payload → NipperMessage with channelType: rabbitmq, RabbitMqMeta populated
        │
        ▼
Handler calls Router.HandleMessage pipeline:
  resolve user → allowlist → session key → dedup → publish to internal RabbitMQ
        │
        ▼
Agent processes message, publishes NipperEvent (done) to nipper.events
        │
        ▼
Dispatcher assembles NipperResponse, calls adapter.DeliverResponse
        │
        ▼
DeliveryClient publishes to:
  - RabbitMqMeta.ReplyTo (if set): direct queue publish (empty exchange)
  - Otherwise: nipper.outbound exchange with key nipper.{userId}.outbox
```

### Unit tests (42 new tests passing, 0 failing)

- **Normalizer** (`internal/channels/rabbitmq`): 22 tests — text message, correlationId (payload/header/priority), replyTo, direct mode, userId from header/queue/routing-key, empty/whitespace text, invalid JSON, no userId, unique UUIDs, auto-generated origin ID, custom messageId, capabilities, serialisability, meta fields, extractUserIDFromRoutingKey (6), extractUserIDFromQueue (5), helpers
- **Delivery** (`internal/channels/rabbitmq`): 9 tests — basic text, replyTo, correlationId header, nil response, nil publisher, not connected, publish error, headers, inReplyTo
- **Adapter** (`internal/channels/rabbitmq`): 11 tests — constructor, channelType, healthCheck, isConnected, consumerCount, deliverEvent, deliverResponse (2), setHandler, normalizeInbound (3), validate (4), buildURL (4)

---

## ✅ CHECKPOINT: Phase 15 Complete

**Implemented:** Security package with startup audit and runtime monitoring. The startup audit runs before the gateway accepts connections and checks gateway/admin bind addresses, filesystem permissions, literal secrets in config, user directory symlinks, RabbitMQ TLS, and audit log writability. The runtime monitor runs background goroutines checking symlinks (every 5m), datastore/queue connectivity (every 60s), and failed registration rates (every 5m).

### Files created

| File | Purpose |
|------|---------|
| `internal/security/audit.go` | `RunStartupAudit` — runs all startup security checks before accepting connections. Checks: `gateway-bind` (critical: must be 127.0.0.1), `admin-bind` (critical: must be localhost), `filesystem-permissions` (warn: ~/.open-nipper/ must be 0700), `no-secrets-in-config` (critical: regex detection of literal tokens/passwords/API keys — covers GitHub, Slack, AWS, OpenAI, nipper tokens), `user-directory-isolation` (critical: no symlinks in users/), `rabbitmq-tls` (warn: non-localhost must use amqps://), `audit-log-writable` (warn: logs dir must be writable). Returns `[]AuditFinding` with CheckID, Severity, Description, Remediation. |
| `internal/security/runtime.go` | `RuntimeMonitor` — background goroutines for continuous security checks. `symlinksLoop` (every 5m): detects and removes symlinks in user directories. `connectivityLoop` (every 60s): pings datastore and RabbitMQ; sets health flags used by message pipeline. `registrationFailureLoop` (every 5m): alerts on >10 failed agent registrations per window. `HealthChecker` interface for pluggable subsystem health checks. Thread-safe findings with cap at 100. |
| `internal/security/audit_test.go` | 18 tests: gateway bind localhost/exposed, admin bind localhost/exposed/disabled, no-secrets clean/literal-GitHub/empty/Slack/AWS/nipper-token, RabbitMQ TLS localhost/remote-no-TLS/remote-with-TLS/empty, full startup audit clean/multiple-issues, countBySeverity. |
| `internal/security/runtime_test.go` | 10 tests: constructor, initial state, record failed registration, start/stop lifecycle, connectivity datastore-down/up, queue-down, both-up, findings capped at 100, findings returns copy (not reference), nil checkers remain healthy. |

### Unit tests (28 new tests passing, 0 failing)

- **Audit** (`internal/security`): 18 tests — gateway/admin bind checks (5), secrets detection (6), RabbitMQ TLS (4), full audit (2), helper (1)
- **Runtime** (`internal/security`): 10 tests — constructor, initial state, registration tracking, lifecycle, connectivity (5), findings (2), nil safety

---

## ✅ CHECKPOINT: Phase 16 Complete

**Implemented:** OpenTelemetry instrumentation with HTTP middleware, pipeline span helpers, and metric recording functions. The middleware creates spans for every HTTP request with semantic conventions and records request duration histograms. Pipeline helpers provide `StartSpan`, `RecordMessageReceived/Rejected/Published`, `RecordEventConsumed`, `RecordResponseDelivered`, `RecordPublishError`, `SpanError`, and `SpanOK` — all nil-safe for graceful degradation when telemetry is disabled.

### Files created

| File | Purpose |
|------|---------|
| `internal/telemetry/middleware.go` | `HTTPMiddleware` — creates OpenTelemetry spans for every HTTP request with semantic conventions (`http.method`, `http.target`, `http.status_code`, `http.remote_addr`). Records `nipper_http_request_duration_seconds` histogram with method, route, and status code attributes. `statusRecorder` response writer wrapper for capturing status codes. `Tracer()` helper returns the named gateway tracer. |
| `internal/telemetry/instrument.go` | Pipeline instrumentation helpers. `StartSpan` creates child spans with custom attributes. `RecordMessageReceived/Rejected/Published` increment counters with channel_type/reason/queue_mode attributes. `RecordEventConsumed` tracks event types. `RecordResponseDelivered` tracks channel delivery. `RecordPublishError` tracks RabbitMQ publish failures. `SpanError`/`SpanOK` set span status codes. All functions are nil-safe — they gracefully no-op when metrics/spans are nil. |
| `internal/telemetry/middleware_test.go` | 5 tests: status code recording, default 200, nil metrics safety, 500 error, Tracer non-nil. |
| `internal/telemetry/instrument_test.go` | 15 tests: RecordMessageReceived nil/with-metrics, RecordMessageRejected, RecordMessagePublished, RecordEventConsumed, RecordResponseDelivered, RecordPublishError nil/with-metrics, StartSpan returns span, SpanError nil-span/nil-error/with-error, SpanOK nil/with-span, all-nil-safe batch. |

### Unit tests (20 new tests passing, 0 failing)

- **Middleware** (`internal/telemetry`): 5 tests — HTTP status code recording, default 200, nil metrics, 500 error, tracer non-nil
- **Instrument** (`internal/telemetry`): 15 tests — all metric recording functions with nil safety, span creation, span error/ok status

---

## ✅ CHECKPOINT: Phase 18 Complete

**Implemented:** Lifecycle manager for ordered graceful shutdown and delivery retry with backoff in the dispatcher. The lifecycle manager replaces the previous defer-based shutdown with explicit phase-ordered shutdown (HTTP → Adapters → Drain → Consumers → Publishers → Brokers → Datastore → Telemetry → Logger). Dispatcher now retries failed deliveries 3 times with 1-second backoff before logging failure — matching the spec: "Channel delivery failure: Retry 3 times with 1s backoff; log error if all fail; do not nack the agent event."

### Files created

| File | Purpose |
|------|---------|
| `internal/lifecycle/lifecycle.go` | `Manager` — coordinates ordered startup/shutdown of gateway components. Components register with a `Phase` (priority). On `Shutdown()`, components stop in phase order (lowest first); same-phase components stop concurrently. Phase constants: `PhaseHTTP(10)`, `PhaseAdapters(20)`, `PhaseDrain(30)`, `PhaseConsumers(40)`, `PhasePublishers(50)`, `PhaseBrokers(60)`, `PhaseDatastore(70)`, `PhaseTelemetry(80)`, `PhaseLogger(90)`. Per-phase timeout (default 30s). `RegisterStop` convenience method. |
| `internal/lifecycle/lifecycle_test.go` | 14 tests: default timeout, register/count, shutdown order verification, same-phase concurrency, stop error propagation, nil StopFn, empty shutdown, timeout handling, cancelled context, error-in-middle continues, RegisterStop convenience, groupByPhase empty/single/multiple. |

### Files modified

| File | Changes |
|------|---------|
| `internal/gateway/dispatcher.go` | Added `deliverWithRetry` method — retries `DeliverResponse` up to 3 times with 1s delay. Wired into all delivery paths: buffered done, buffered error, streaming done, and broadcast delivery. Respects context cancellation during retries. |
| `internal/gateway/dispatcher_test.go` | Added `retryCountAdapter` mock and 3 new tests: successful retry after 2 failures, all retries fail, context cancelled during retry. |
| `cli/serve.go` | Replaced defer-based shutdown with `lifecycle.Manager`. All components register with correct shutdown phases. Shutdown triggered on SIGINT/SIGTERM with 60s overall timeout. |

### Unit tests (14 new lifecycle + 3 new dispatcher = 17 new tests, 0 failing)

- **Lifecycle** (`internal/lifecycle`): 14 tests — default timeout, register/count, ordered shutdown, concurrent same-phase, stop errors, nil StopFn, empty shutdown, timeout, cancelled context, error-in-middle, RegisterStop, groupByPhase (3)
- **Dispatcher retry** (`internal/gateway`): 3 tests — successful retry after failures, all retries fail, context cancellation during retry

### Shutdown order (as implemented)

1. **Phase 10 (HTTP)**: Stop main HTTP server + admin server (drain in-flight requests, 30s deadline)
2. **Phase 20 (Adapters)**: Stop all channel adapters (WhatsApp, Slack, MQTT, RabbitMQ channel, Cron)
3. **Phase 40 (Consumers)**: Stop dispatcher + deduplicator
4. **Phase 50 (Publishers)**: Close RabbitMQ publisher
5. **Phase 60 (Brokers)**: Close RabbitMQ broker connections
6. **Phase 70 (Datastore)**: Close SQLite database
7. **Phase 80 (Telemetry)**: Flush OpenTelemetry tracing + metrics
8. **Phase 90 (Logger)**: Flush Zap logger

---

## Part 5 — Datastore

### 5.1 Repository Interface

Defined in `internal/datastore/interface.go`. This is the only abstraction layer — SQLite implements it; a future PostgreSQL driver would also implement it.

```go
type Repository interface {
    // Users
    CreateUser(ctx context.Context, user CreateUserRequest) (*User, error)
    GetUser(ctx context.Context, userID string) (*User, error)
    UpdateUser(ctx context.Context, userID string, updates UpdateUserRequest) (*User, error)
    DeleteUser(ctx context.Context, userID string) error
    ListUsers(ctx context.Context) ([]*User, error)
    IsUserEnabled(ctx context.Context, userID string) (bool, error)

    // Identities
    AddIdentity(ctx context.Context, userID, channelType, channelIdentity string) error
    RemoveIdentity(ctx context.Context, id int64) error
    ListIdentities(ctx context.Context, userID string) ([]*Identity, error)
    ResolveIdentity(ctx context.Context, channelType, channelIdentity string) (string, error)

    // Allowlist
    IsAllowed(ctx context.Context, userID, channelType string) (bool, error)
    SetAllowed(ctx context.Context, userID, channelType string, enabled bool, createdBy string) error
    RemoveAllowed(ctx context.Context, userID, channelType string) error
    ListAllowed(ctx context.Context, channelType string) ([]*AllowlistEntry, error)

    // Policies
    GetUserPolicy(ctx context.Context, userID, policyType string) (*PolicyData, error)
    SetUserPolicy(ctx context.Context, userID, policyType string, data *PolicyData) error
    DeleteUserPolicy(ctx context.Context, userID, policyType string) error
    ListUserPolicies(ctx context.Context, userID string) ([]*UserPolicy, error)

    // Agents
    ProvisionAgent(ctx context.Context, req ProvisionAgentRequest) (*Agent, error)
    GetAgent(ctx context.Context, agentID string) (*Agent, error)
    GetAgentByTokenHash(ctx context.Context, tokenHash string) (*Agent, error)
    ListAgents(ctx context.Context, userID string) ([]*Agent, error)
    UpdateAgentStatus(ctx context.Context, agentID string, status AgentStatus, meta *AgentRegistrationMeta) error
    SetAgentRMQUsername(ctx context.Context, agentID, rmqUsername string) error
    RotateAgentToken(ctx context.Context, agentID, newTokenHash, newTokenPrefix string) error
    RevokeAgent(ctx context.Context, agentID string) error
    DeleteAgent(ctx context.Context, agentID string) error

    // Audit
    LogAdminAction(ctx context.Context, entry AdminAuditEntry) error
    QueryAuditLog(ctx context.Context, filters AuditQueryFilters) ([]*AdminAuditEntry, error)

    // Backup
    Backup(ctx context.Context, destPath string) error

    // Lifecycle
    Close() error
    Ping(ctx context.Context) error
}
```

### 5.2 SQLite Implementation

**File:** `internal/datastore/sqlite/store.go`

- Open with WAL mode: `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`
- The `go-sqlite3` driver requires CGO; use build tags if needed
- Migrations run at startup from embedded SQL files (use `//go:embed migrations/*.sql`)
- Migration runner reads the `schema_migrations` table, applies any unapplied migrations in order
- All writes are wrapped in transactions
- All queries use prepared statements

### 5.3 Schema (SQL Migrations)

**`001_initial_schema.sql`:**
```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT 1,
    default_model   TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    preferences     TEXT NOT NULL DEFAULT '{}'
);
```

**`002_add_allowed_list.sql`:**
```sql
CREATE TABLE IF NOT EXISTS user_identities (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type     TEXT NOT NULL,
    channel_identity TEXT NOT NULL,
    verified         BOOLEAN NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,
    UNIQUE(channel_type, channel_identity)
);
CREATE INDEX IF NOT EXISTS idx_identity_lookup ON user_identities(channel_type, channel_identity);

CREATE TABLE IF NOT EXISTS allowed_list (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    UNIQUE(user_id, channel_type)
);
CREATE INDEX IF NOT EXISTS idx_allowed_lookup ON allowed_list(channel_type, user_id);
```

**`003_add_user_policies.sql`:**
```sql
CREATE TABLE IF NOT EXISTS user_policies (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    policy_type TEXT NOT NULL,
    policy_data TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(user_id, policy_type)
);
```

**`004_add_agents.sql`:**
```sql
CREATE TABLE IF NOT EXISTS agents (
    id                   TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label                TEXT NOT NULL DEFAULT '',
    token_hash           TEXT NOT NULL,
    token_prefix         TEXT NOT NULL,
    rmq_username         TEXT,
    status               TEXT NOT NULL DEFAULT 'provisioned',
    last_registered_at   TEXT,
    last_registered_ip   TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE(user_id, label)
);
CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id);
CREATE INDEX IF NOT EXISTS idx_agents_token_prefix ON agents(token_prefix);

CREATE TABLE IF NOT EXISTS admin_audit (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp      TEXT NOT NULL,
    action         TEXT NOT NULL,
    actor          TEXT NOT NULL,
    target_user_id TEXT,
    details        TEXT NOT NULL,
    ip_address     TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON admin_audit(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_user ON admin_audit(target_user_id);
```

### 5.4 In-Memory Cache

**File:** `internal/datastore/cache.go`

A simple TTL cache wraps the SQLite repository:

| Query | TTL | Invalidated on |
|-------|-----|----------------|
| `ResolveIdentity` | 60s | `AddIdentity`, `RemoveIdentity` |
| `IsAllowed` | 60s | `SetAllowed`, `RemoveAllowed` |
| `IsUserEnabled` | 60s | `UpdateUser`, `DeleteUser` |
| `GetUserPolicy` | 300s | `SetUserPolicy`, `DeleteUserPolicy` |
| `GetAgentByTokenHash` | 60s | `RotateAgentToken`, `UpdateAgentStatus`, `DeleteAgent` |

Use `patrickmn/go-cache` for TTL storage. Cache keys are deterministic strings (e.g., `identity:whatsapp:5491155553935@s.whatsapp.net`).

The cache wraps the repository via the same `Repository` interface — a `CachedRepository` struct that delegates to the underlying `Repository` and applies cache read/write logic.

---

## Part 6 — Canonical Data Model

### 6.1 Types (`internal/models/`)

Implement all types from `GATEWAY_ARCHITECTURE.md` as Go structs:

**`message.go`:**
- `NipperMessage` — full inbound message with all fields
- `MessageContent` with `ContentPart` slice for multimodal
- `NipperEvent` — streaming events from agents (`delta`, `tool_start`, `tool_progress`, `tool_end`, `thinking`, `error`, `done`)
- `NipperResponse` — assembled outbound response

**`channel.go`:**
- `ChannelType` as `string` type with constants
- `ChannelMeta` as interface with concrete implementations: `WhatsAppMeta`, `SlackMeta`, `CronMeta`, `MqttMeta`, `RabbitMqMeta`
- `DeliveryContext` with `ChannelCapabilities`
- Channel capability constants (the matrix from the architecture doc is codified in adapter constructors)

**`queue.go`:**
- `QueueItem` — wraps `NipperMessage` + queue metadata
- `QueueMode` as `string` type with constants
- `ControlMessage` — `interrupt` | `abort`

**`user.go`:**
- `User`, `CreateUserRequest`, `UpdateUserRequest`
- `Identity`
- `AllowlistEntry`
- `Agent`, `AgentStatus`, `ProvisionAgentRequest`, `AgentRegistrationMeta`
- `UserPolicy`, `PolicyData`
- `AdminAuditEntry`, `AuditQueryFilters`

**`session.go`:**
- `Session`, `SessionMetadata`, `SessionStatus`
- `ContextUsage`
- `TranscriptLine`

---

## Part 7 — Channel Adapters

### 7.1 Adapter Interface

```go
type ChannelAdapter interface {
    ChannelType() models.ChannelType
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    HealthCheck(ctx context.Context) error
    NormalizeInbound(ctx context.Context, raw []byte) (*models.NipperMessage, error)
    DeliverResponse(ctx context.Context, resp *models.NipperResponse) error
    DeliverEvent(ctx context.Context, event *models.NipperEvent) error
    ResolveUser(ctx context.Context, channelUserID string) (string, error)
}
```

### 7.2 WhatsApp Adapter (`internal/channels/whatsapp/`)

**Inbound normalization (`normalizer.go`):**
- Parse Wuzapi webhook JSON into the `WhatsAppMeta` and `NipperMessage` structs
- Handle all Wuzapi message types: `Conversation`, `ImageMessage`, `AudioMessage`, `VideoMessage`, `DocumentMessage`, `StickerMessage`, `ContactMessage`, `LocationMessage`
- For media messages: use `s3.url` if present; otherwise set `url` to empty and flag for agent-side download
- Filter `IsFromMe: true` messages (return `nil, nil` to signal "ignore")
- Filter non-Message events (return `nil, nil`)
- Extract `senderJid` from `Info.MessageSource.Sender` for user resolution
- Assign `messageId` as UUIDv7, set `originMessageId` from `Info.ID`
- Set `sessionKey` after user resolution (delegated to session resolver)
- Build full `DeliveryContext` with WhatsApp capability matrix

**HMAC verification (`hmac.go`):**
- Extract `X-Hmac-Signature` header (format: `sha256=<hex>`)
- Compute `HMAC-SHA256(requestBody, wuzapiHmacKey)`
- Use `crypto/subtle.ConstantTimeCompare` for comparison
- Return `false` if header is missing or malformed

**Outbound delivery (`delivery.go`):**
- `DeliverResponse`: build Wuzapi REST API calls
  - Show typing indicator: `POST /chat/presence { "Phone": ..., "State": "composing" }`
  - Send text: `POST /chat/send/text` with `ContextInfo` to quote original
  - Send image: `POST /chat/send/image`
  - Send document: `POST /chat/send/document`
  - Mark as read: `POST /chat/markread`
  - Clear typing: `POST /chat/presence { "State": "paused" }`
- `DeliverEvent`: for streaming events, buffer `delta` events; send assembled response on `done`
- Retry logic: up to 3 attempts with 1s backoff on transient errors
- All HTTP calls use a shared `http.Client` with timeout

**Startup:**
- Register webhook URL with Wuzapi: `POST /webhook { "WebhookUrl": ..., "Events": [...] }`
- Validate Wuzapi is reachable; log warning if not (gateway continues to start)

### 7.3 Slack Adapter (`internal/channels/slack/`)

**Inbound normalization (`normalizer.go`):**
- Parse Slack Events API JSON
- Handle `url_verification` challenge (return challenge value immediately, do not enqueue)
- Filter bot messages (`bot_id` present), app mentions from self, message edits with `subtype: "message_changed"`
- Extract `user` (Slack user ID) for identity resolution
- Detect thread context: populate `threadId` and `replyMode: "thread"` if `thread_ts` is present
- Build full `DeliveryContext` with Slack capability matrix

**Slack signature verification (`hmac.go`):**
- Validate `X-Slack-Request-Timestamp` (reject if > 5 minutes old to prevent replay)
- Compute `HMAC-SHA256("v0:" + timestamp + ":" + body, signingSecret)`
- Compare with `X-Slack-Signature` header (format: `v0=<hex>`)

**Outbound delivery (`delivery.go`):**
- `DeliverResponse`: use Slack Web API
  - Create initial reply with `chat.postMessage` in thread
  - For streaming: update message with `chat.update` as `delta` events arrive
  - Final update on `done` event
  - Show typing indicator: `reactions.add` with "eyes" on original message (optional)
- Use Bot Token from channel config (injected from environment)

### 7.4 Cron Adapter (`internal/channels/cron/`)

**Scheduler (`scheduler.go`):**
- Use `robfig/cron/v3` for cron expression parsing and scheduling
- Load all jobs from `channels.cron.jobs` config at startup
- Validate all job `userId` values exist in the datastore at startup; disable invalid jobs and log error
- Each job fires → `normalizeInbound` → enqueue to the message pipeline
- Cron `sessionKey` format: `user:{userId}:channel:cron:session:{jobId}`
- `deliveryContext.replyMode = "broadcast"`, `notifyChannels` from job config

### 7.5 MQTT Adapter (`internal/channels/mqtt/`)

**Connection management:**
- Use `paho.mqtt.golang` client with persistent session (`CleanSession: false`)
- Subscribe to `{topicPrefix}/{userId}/inbox` for all known users
- Re-subscribe on reconnect
- Auto-reconnect with exponential backoff

**Inbound normalization:**
- Parse JSON payload from MQTT message
- Extract `userId` from topic path: `nipper/{userId}/inbox`
- Validate `userId` against datastore
- Populate `MqttMeta` with `broker`, `topic`, `qos`, `retain`, `clientId`, `correlationId`, `responseTopic`

**Outbound delivery:**
- Publish to `responseTopic` from `MqttMeta` if set; otherwise publish to `{topicPrefix}/{userId}/outbox`
- Serialize `NipperResponse` as JSON
- Use same QoS as inbound message

### 7.6 RabbitMQ Channel Adapter (`internal/channels/rabbitmq/`)

Note: This is the **channel adapter** for service-to-service messages, separate from the internal queue system (Gateway↔Agent).

**Exchange/queue setup:**
- Declare `nipper.inbound` (topic exchange) and `nipper.outbound` (topic exchange) and `nipper.dlx`
- Declare per-user inbound queues: `nipper-{userId}-inbox` with DLX binding
- Bind: `nipper.{userId}.inbox → nipper-{userId}-inbox`

**Inbound consume:**
- Consume from each per-user inbound queue with `prefetch: 1`
- Parse JSON body + AMQP headers
- Extract `userId` from routing key or `x-nipper-user` header
- Populate `RabbitMqMeta`

**Outbound delivery:**
- Publish to `nipper.outbound` exchange
- Set `correlationId` from original message if present
- Add response headers: `x-nipper-response-id`, `content-type: application/json`
- Ack original message only after successful delivery

---

## Part 8 — Internal Queue System (Gateway↔Agent)

### 8.1 RabbitMQ Broker Connection (`internal/queue/broker.go`)

- Manage two separate AMQP connections: one for publishing (Gateway → agents), one for consuming (agents → Gateway)
- Reconnect loop with exponential backoff; log `warn` on disconnect, `info` on reconnect
- Heartbeat: 60 seconds
- Connection-level topology declaration on first connect (idempotent)

### 8.2 Topology Declaration (`internal/queue/topology.go`)

Declare on startup (all idempotent with `passive=false`):

```
Exchanges:
  - nipper.sessions  (topic, durable)
  - nipper.events    (topic, durable)
  - nipper.control   (topic, durable)
  - nipper.sessions.dlx (fanout, durable)

Queues:
  - nipper-events-gateway (durable, prefetch: 50)
    Binding: nipper.events.# → nipper-events-gateway

  - nipper-sessions-dlq (durable, TTL: 24h)
    Binding: nipper.sessions.dlx → nipper-sessions-dlq
```

Per-user queues are declared during agent provisioning (not at gateway start), but the gateway **ensures** they exist for all provisioned users that have agents during the startup sequence.

**Per-user queue properties:**
```
durable: true
x-dead-letter-exchange: nipper.sessions.dlx
x-dead-letter-routing-key: nipper.sessions.dlq
x-message-ttl: 300000
x-max-length: 50
x-overflow: reject-publish
```

**Per-user control queue:**
```
nipper-control-{userId}
durable: true
Binding: nipper.control.{userId} → nipper-control-{userId}
```

### 8.3 Publisher (`internal/queue/publisher.go`)

```go
type Publisher interface {
    PublishMessage(ctx context.Context, item *models.QueueItem) error
    PublishControl(ctx context.Context, userID string, msg *models.ControlMessage) error
}
```

- `PublishMessage`: serialize `QueueItem` as JSON, publish to `nipper.sessions` exchange
  - Routing key: `nipper.sessions.{userId}.{sessionId}`
  - Delivery mode: `Persistent` (2)
  - Headers: `x-nipper-user-id`, `x-nipper-session-key`, `x-nipper-queue-mode`, `x-nipper-priority`
  - MessageID: `QueueItem.ID`
  - If publish fails with `nack` (queue full / `reject-publish`), return backpressure error to caller
- Handle channel errors and reconnect transparently

### 8.4 Event Consumer (`internal/queue/consumer.go`)

```go
type EventConsumer interface {
    Start(ctx context.Context) error
    Stop() error
    SetHandler(fn func(ctx context.Context, event *models.NipperEvent) error)
}
```

- Consume from `nipper-events-gateway` with `prefetch: 50`
- Deserialize JSON body into `NipperEvent`
- Call the registered handler (the gateway dispatcher)
- Ack on successful dispatch; nack with requeue=false on unrecoverable errors

### 8.5 RabbitMQ Management API Client (`internal/queue/management.go`)

Used by the agent auto-registration endpoint:

```go
type ManagementClient interface {
    CreateUser(ctx context.Context, username, password string) error
    DeleteUser(ctx context.Context, username string) error
    SetVhostPermissions(ctx context.Context, vhost, username string, perms VhostPermissions) error
    GetQueueInfo(ctx context.Context, vhost, queueName string) (*QueueInfo, error)
    ListQueues(ctx context.Context, vhost string) ([]*QueueInfo, error)
}

type VhostPermissions struct {
    Configure string `json:"configure"`
    Write     string `json:"write"`
    Read      string `json:"read"`
}

type QueueInfo struct {
    Name                  string `json:"name"`
    Messages              int    `json:"messages"`
    MessagesReady         int    `json:"messages_ready"`
    MessagesUnacknowledged int   `json:"messages_unacknowledged"`
    Consumers             int    `json:"consumers"`
}
```

- HTTP client targeting the Management API at `agents.rabbitmq_management.url`
- Uses HTTP Basic Auth with management credentials
- All calls have a 10-second timeout
- If management API is unreachable, the `/agents/register` endpoint returns `503`

---

## Part 9 — Session Management (Distributed Architecture)

> **Architecture boundary:** In the distributed design, the gateway **never** touches
> session files (transcripts, metadata, compaction). Session file management is an
> **agent-only** responsibility because agents can run anywhere and there is an
> agent-to-user mapping. The gateway only performs pure session-key computation
> and in-memory DeliveryContext tracking.

### 9.1 Gateway Session Key Resolver (`internal/gateway/resolver.go`)

The resolver is a **pure function** that derives a deterministic, filesystem-safe
session key from the inbound `NipperMessage`.  It has no store dependency and
does not create files.

| Channel | Session Resolution |
|---------|-------------------|
| WhatsApp | One session per chat JID (not sender JID) — allows group chat threading |
| Slack | One session per Slack channel+thread |
| Cron | One persistent session per job ID: `user:{userId}:channel:cron:session:{jobId}` |
| MQTT | One session per MQTT `clientId` |
| RabbitMQ | One session per `correlationId` or routing key source |

The resolved `sessionKey` is set on the `NipperMessage` before publishing to
RabbitMQ.  The agent, upon receiving the message, creates the session on its
local filesystem if it doesn't already exist.

### 9.2 DeliveryContext Registry (`internal/gateway/registry.go`)

Thread-safe, in-memory map of `sessionKey → DeliveryContext`.  The router
registers the delivery context when a message enters the pipeline; the
dispatcher looks it up when routing agent response events back to channel
adapters.

- `Register(sessionKey, dc)` — called by the router after key resolution
- `Lookup(sessionKey) (DeliveryContext, bool)` — called by the dispatcher
- `Remove(sessionKey)` — called on the `done` event
- `EvictOlderThan(cutoff)` — periodic housekeeping for abandoned entries

### 9.3 Agent Session Library (`pkg/session/`)

The filesystem-based session store, file locking, and compaction logic live in
`pkg/session/` — an **importable Go library** designed for agents.  Any Go
agent can use:

```go
import "github.com/jescarri/open-nipper/pkg/session"
```

**Library contents:**

| File | Purpose |
|------|---------|
| `types.go` | Standalone types: `Session`, `SessionMetadata`, `TranscriptLine`, `ContextUsage`, `CreateSessionRequest` — no dependency on `internal/models` |
| `key.go` | `BuildSessionKey`, `ParseSessionKey`, `SanitizeSessionID` |
| `store.go` | `Store` / `SessionStore` interface — filesystem-backed session store rooted at `~/.open-nipper`. `CreateSession`, `GetSession`, `ListSessions`, `LoadTranscript`, `AppendTranscript`, `UpdateMeta`, `ArchiveSession`, `ResetSession` |
| `lock.go` | `FileLock` — hard-link-based file locking. `CleanStaleLocks(dir)` for background housekeeping |
| `compaction.go` | `Compactor` — archives older transcript lines, rewrites active transcript atomically. `ShouldCompact` utility |

**Filesystem layout (managed by agents, not the gateway):**
```
~/.open-nipper/users/{userId}/
├── profile.json
├── sessions/
│   ├── sessions.json              (index, cached 45s TTL)
│   ├── {sessionId}.jsonl          (transcript, append-only)
│   ├── {sessionId}.jsonl.lock     (file lock)
│   └── {sessionId}.meta.json      (metadata)
├── memory/
│   └── YYYY-MM-DD.md
└── workspace/
```

WebSocket session commands (`sessions.compact`, `sessions.reset`) are forwarded
as **RabbitMQ control messages** to the agent — the gateway does not execute
them locally.

---

## Part 10 — Message Pipeline (Gateway Router)

### 10.1 Router (`internal/gateway/router.go`)

The central message pipeline that processes every inbound message:

```
func (r *Router) HandleMessage(ctx context.Context, raw []byte, adapter ChannelAdapter) error
```

**Steps:**
1. `adapter.NormalizeInbound(ctx, raw)` → `*NipperMessage` (or nil if message should be ignored)
2. If nil, return early (not an error)
3. `resolveUser(ctx, msg)` — query `datastore.ResolveIdentity(channelType, channelIdentity)`
4. **Allowlist guard** (see below)
5. `gateway.Resolver.Resolve(ctx, msg)` → populate `msg.SessionKey` (pure computation, no files)
6. `gateway.Registry.Register(msg.SessionKey, msg.DeliveryContext)` — track for response routing
7. Apply queue mode (collect debounce, deduplication)
8. Build `QueueItem` with mode and priority
9. `publisher.PublishMessage(ctx, item)` → handle backpressure
10. Trigger async UX effects (typing indicator, mark-as-read) for applicable channels

### 10.2 Allowlist Guard (`internal/allowlist/guard.go`)

Called at step 4. The guard:
1. If `userId == ""` (identity not found): log `warn` with `reason: "unknown_identity"`, write `admin_audit` entry, return 200 to caller
2. Check `datastore.IsUserEnabled(ctx, userId)`: if false, log + audit, return 200
3. Check `datastore.IsAllowed(ctx, userId, channelType)`: also check wildcard `*`; if false, log + audit, return 200
4. If all checks pass: return `nil` (allowed)

The audit entry format matches `GATEWAY_ARCHITECTURE.md`:
```json
{
  "action": "message.rejected",
  "actor": "system",
  "details": {
    "reason": "unknown_identity | user_disabled | not_in_allowlist",
    "channel_type": "...",
    "channel_identity": "[REDACTED]"
  }
}
```

**Channel-specific rejection behavior:**
- HTTP channels (WhatsApp, Slack): return HTTP 200 to webhook caller, no reply to sender
- MQTT: ack the message at broker level, publish nothing
- RabbitMQ channel: `basic.ack`, publish nothing

### 10.3 Queue Mode Handling (`internal/gateway/router.go`)

**Collect mode:**
- Maintain a per-user, per-channel buffer of pending messages with a debounce timer
- When debounce fires or `collectCap` is reached: assemble multi-message prompt and publish single `QueueItem`
- Drop oldest if cap exceeded (configurable policy: `old | new | summarize`)

**Steer mode:**
- Check if the user's agent queue has `messages_ready + messages_unacknowledged > 0` via Management API
- If idle: publish immediately
- If busy: queue normally

**Interrupt mode:**
- Publish `QueueItem` with high priority
- Also publish `ControlMessage{type: "interrupt"}` to `nipper.control` exchange for the user

### 10.4 Deduplication (`internal/gateway/dedup.go`)

- In-memory cache keyed by `(userId, dedupeStrategy, dedupeKey)`
- `message-id` strategy: `originMessageId`
- `prompt` strategy: `sha256(content.text)`
- `none` strategy: disabled
- TTL: `dedupeWindowMs` (default 30s)
- If duplicate detected: return early (log at `debug` level), do not publish to queue

---

## Part 11 — Event Dispatcher (Agent Response Routing)

### 11.1 Dispatcher (`internal/gateway/dispatcher.go`)

Consumes from `nipper-events-gateway` and routes events to the correct channel adapter.

**Event routing:**
1. Receive `NipperEvent` from consumer
2. Look up `DeliveryContext` for the session (from in-memory session store, or reload from disk)
3. Based on `channelType`, dispatch to the correct adapter
4. For non-streaming channels (WhatsApp, MQTT, RabbitMQ): buffer `delta` events in a per-session accumulator; on `done` event, assemble `NipperResponse` and call `adapter.DeliverResponse`
5. For streaming channels (Slack, WebSocket): forward `NipperEvent` events to `adapter.DeliverEvent` as they arrive
6. On `error` event: deliver error message to user via channel adapter

**Buffer management:**
- Per-session accumulator: `map[sessionKey]*strings.Builder` (append `delta.text`)
- On `done` event: create `NipperResponse`, call `DeliverResponse`, clear accumulator
- Accumulator timeout: 5 minutes (clean up stale accumulators)

**WebSocket fan-out:**
- For sessions that have active WebSocket subscribers (connected internal clients), also forward all events to WebSocket
- WebSocket event format: `{ "type": "event", "payload": NipperEvent }`

**Broadcast delivery (cron/headless):**
- If `deliveryContext.replyMode == "broadcast"`, deliver to all `notifyChannels`
- Parse `notifyChannels` entries: format `"channelType:channelId"`
- Route each to the appropriate adapter

---

## Part 12 — HTTP Server (Main: :18789)

### 12.1 Server Setup (`internal/gateway/server.go`)

```go
// Routes:
mux.HandleFunc("POST /webhook/whatsapp", webhookHandler.HandleWhatsApp)
mux.HandleFunc("POST /webhook/slack",    webhookHandler.HandleSlack)
mux.HandleFunc("POST /agents/register",  registerHandler.Handle)
mux.HandleFunc("GET  /ws",               wsHandler.Handle)
mux.HandleFunc("GET  /health",           healthHandler.Handle)
```

- Wrapped with `otelhttp.NewHandler` for automatic trace propagation
- Request timeout: configurable `read_timeout_seconds` / `write_timeout_seconds`
- Graceful shutdown: `server.Shutdown(ctx)` with 30-second deadline

### 12.2 WhatsApp Webhook Handler (`internal/gateway/webhook.go`)

```
POST /webhook/whatsapp
```
1. Read body (limit: 10MB)
2. Verify HMAC signature using `whatsapp.VerifyHMAC(body, header, hmacKey)`
   - On failure: return 401, log security event
3. Parse event type from JSON
4. For non-Message events: handle status events (log disconnects, etc.), return 200
5. For Message events: `router.HandleMessage(ctx, body, whatsappAdapter)`
6. Always return 200 to Wuzapi (prevents retries)

### 12.3 Slack Webhook Handler

```
POST /webhook/slack
```
1. Verify Slack signature using `slack.VerifySignature(body, headers, signingSecret)`
2. For `url_verification` type: return `{"challenge": "..."}` — no enqueue
3. For `event_callback` type with `message` event: `router.HandleMessage(ctx, body, slackAdapter)`
4. Always return 200

### 12.4 Agent Registration Handler (`internal/gateway/register.go`)

```
POST /agents/register
Authorization: Bearer npr_...
```

**Flow:**
1. Extract Bearer token from `Authorization` header; return 401 if missing/malformed
2. Compute `SHA-256(token)` and query `datastore.GetAgentByTokenHash(ctx, hash)`; return 401 if not found
3. Check `agent.Status != "revoked"`; return 401 if revoked
4. Load user: `datastore.GetUser(ctx, agent.UserID)`; return 403 if user disabled
5. Apply rate limiting: max `agents.registration.rate_limit` per minute per token prefix
6. Generate new RabbitMQ password (32 bytes, cryptographically random, base64url-encoded)
7. Create/update RabbitMQ user via Management API: `PUT /api/users/agent-{agentId}`
8. Set vhost permissions:
   - `configure: "^(nipper-agent-{userId}|nipper-control-{userId})$"`
   - `write: "^(nipper\.events|nipper\.logs)$"`
   - `read: "^(nipper-agent-{userId}|nipper-control-{userId})$"`
9. Update datastore: `UpdateAgentStatus(ctx, agentId, "registered", meta)` + `SetAgentRMQUsername`
10. Log `agent.registered` to `admin_audit`
11. Return registration response blob (see architecture doc format)

**Error responses:** intentionally vague for auth failures (all return `{"ok": false, "error": "unauthorized"}`).

---

## Part 13 — Admin API Server (:18790)

### 13.1 Server Setup (`internal/admin/server.go`)

- Binds to `127.0.0.1:18790` only — never `0.0.0.0`
- Optional Bearer token auth (if `gateway.admin.auth.enabled: true`)
- All successful operations log to `admin_audit` table
- Consistent JSON response format: `{"ok": true, "result": {...}}` / `{"ok": false, "error": "..."}`

### 13.2 User Management (`internal/admin/users.go`)

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/admin/users` | Create user — validate ID format, name required |
| `GET` | `/admin/users` | List all users |
| `GET` | `/admin/users/{userId}` | Get user by ID |
| `PUT` | `/admin/users/{userId}` | Update name, model, enabled flag, preferences |
| `DELETE` | `/admin/users/{userId}` | Delete user (cascade: identities, agents, allowlist); also delete RMQ queues and users |

**User deletion cleanup:**
1. List all agents for user → delete each agent's RMQ user via Management API
2. Delete RabbitMQ queues: `nipper-agent-{userId}`, `nipper-control-{userId}`
3. Delete user record (CASCADE handles DB rows)
4. Log `user.deleted` to audit

### 13.3 Identity Management (`internal/admin/identities.go`)

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/admin/users/{userId}/identities` | Add identity (`channel_type` + `channel_identity`) |
| `GET` | `/admin/users/{userId}/identities` | List identities for user |
| `DELETE` | `/admin/users/{userId}/identities/{id}` | Remove identity by ID |

Invalidate `ResolveIdentity` cache on add/remove.

### 13.4 Allowlist Management (`internal/admin/allowlist.go`)

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/admin/allowlist` | Add allowlist entry (`user_id`, `channel_type`, `enabled`) |
| `GET` | `/admin/allowlist` | List all entries |
| `GET` | `/admin/allowlist/{channelType}` | List entries for a channel |
| `PUT` | `/admin/allowlist/{userId}/{channelType}` | Enable/disable entry |
| `DELETE` | `/admin/allowlist/{userId}/{channelType}` | Remove entry |

Wildcard `channel_type: "*"` allows all channels.

### 13.5 Policy Management (`internal/admin/policies.go`)

| Method | Path | Action |
|--------|------|--------|
| `GET` | `/admin/users/{userId}/policies` | List all policies |
| `PUT` | `/admin/users/{userId}/policies/{type}` | Set policy (merge if exists) |
| `DELETE` | `/admin/users/{userId}/policies/{type}` | Remove policy (revert to defaults) |

Policy types: `tools`, `rate_limit`, `skills`, `models`.

### 13.6 Agent Provisioning (`internal/admin/agents.go`)

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/admin/agents` | Provision agent (bound to user + label) |
| `GET` | `/admin/agents` | List all agents (optional `?user_id=` filter) |
| `GET` | `/admin/agents/{agentId}` | Get agent details |
| `DELETE` | `/admin/agents/{agentId}` | Deprovision: revoke token, delete RMQ user |
| `POST` | `/admin/agents/{agentId}/rotate` | Rotate auth token |
| `POST` | `/admin/agents/{agentId}/revoke` | Revoke without deleting record |

**Token generation:**
- Generate 48 cryptographically random bytes using `crypto/rand`
- Encode as base62 (alphanumeric only, no special chars)
- Prepend `npr_` prefix
- Store `sha256(token)` and `token[0:8]` in DB
- Return plaintext token **once** in response

**Agent provisioning also:**
- Ensures RabbitMQ queues exist: declare `nipper-agent-{userId}` and `nipper-control-{userId}` with correct properties
- Creates bindings on `nipper.sessions` exchange: `nipper.sessions.{userId}.# → nipper-agent-{userId}`
- Creates binding on `nipper.control` exchange: `nipper.control.{userId} → nipper-control-{userId}`

### 13.7 System Operations (`internal/admin/system.go`)

| Method | Path | Action |
|--------|------|--------|
| `GET` | `/admin/health` | Return health of all subsystems |
| `GET` | `/admin/audit` | Query audit log with `since`, `until`, `action`, `user_id` filters |
| `POST` | `/admin/backup` | Trigger SQLite hot backup to `datastore.backup.path` |
| `GET` | `/admin/config` | Return current config with secrets redacted |

**Health response:**
```json
{
  "ok": true,
  "result": {
    "status": "healthy",
    "components": {
      "datastore": "ok",
      "rabbitmq": "ok",
      "rabbitmq_management": "ok",
      "whatsapp": "ok | degraded | disabled",
      "slack": "ok | degraded | disabled",
      "mqtt": "ok | degraded | disabled"
    },
    "agents": [
      { "user_id": "user-01", "queue": "nipper-agent-user-01", "consumer_count": 1, "messages_ready": 0, "status": "idle" }
    ]
  }
}
```

---

## Part 14 — WebSocket API

### 14.1 WebSocket Handler (`internal/gateway/websocket.go`)

- Upgrade HTTP to WebSocket using `gorilla/websocket`
- Optional auth: if `gateway.auth.token` is set, first message must be `{"type":"auth","token":"..."}`
- JSON-RPC style: `{"id": "req-001", "method": "sessions.create", "params": {...}}`
- Server-push events: `{"type": "event", "payload": NipperEvent}`
- One goroutine per connection: read loop + write channel
- Register connection in a per-session subscriber map for event fan-out

### 14.2 WebSocket Methods

| Method | Handler |
|--------|---------|
| `sessions.create` | Create session for user + channel |
| `sessions.list` | List sessions for userId |
| `sessions.info` | Get session metadata + context usage |
| `sessions.compact` | Trigger manual compaction |
| `sessions.reset` | Archive transcript, generate new sessionId |
| `sessions.delete` | Archive and remove session |
| `chat.send` | Enqueue message (same pipeline as webhook) |
| `chat.stream` | Subscribe to events for a sessionKey |
| `agents.status` | Query RMQ consumer count for user's queue |
| `config.get` | Return config (redacted) |
| `config.set` | Hot-reload specific config values (whitelisted keys only) |

---

## Part 15 — Agent Health Monitor

### 15.1 Health Check Goroutine

Background goroutine running every `agents.health_check_interval_seconds` (default 30s):
1. List all users with provisioned agents from datastore
2. For each user, query `ManagementClient.GetQueueInfo(ctx, "/nipper", "nipper-agent-{userId}")`
3. Determine agent status:
   | Condition | Status |
   |-----------|--------|
   | `consumers > 0` and `messages_unacknowledged > 0` | `processing` |
   | `consumers > 0` and `messages_unacknowledged == 0` | `idle` |
   | `consumers == 0` and `messages_ready > 0` | `degraded` |
   | `consumers == 0` and `messages_ready == 0` | `offline` |
4. If status is `degraded` for longer than `consumer_timeout_seconds`: log `warn`, emit metric
5. Store latest status in memory for health endpoint and WebSocket `agents.status` method
6. Update `nipper_agent_consumer_count` metric gauge

---

## Part 16 — Security

### 16.1 Startup Security Audit (`internal/security/audit.go`)

Run at startup, after config load and before accepting connections. Each check produces a `SecurityAuditFinding`. Critical findings log at `error`; warnings log at `warn`. The process does **not** exit on audit findings — it logs and continues.

| Check ID | Severity | What is checked |
|----------|----------|-----------------|
| `filesystem-permissions` | warn | `~/.open-nipper/` must have mode `0700` |
| `gateway-bind` | critical | `gateway.bind` must be `"127.0.0.1"` |
| `admin-bind` | critical | `gateway.admin.bind` must be `"127.0.0.1"` |
| `no-secrets-in-config` | critical | Config file must not contain literal tokens/passwords (regex scan) |
| `user-directory-isolation` | critical | No symlinks in `~/.open-nipper/users/` |
| `rabbitmq-tls` | warn | If RabbitMQ URL is non-localhost, must use `amqps://` |
| `audit-log-writable` | warn | Audit log directory must be writable |
| `at-least-one-user` | info | Log if no users exist yet |

### 16.2 Runtime Security Checks (`internal/security/runtime.go`)

Background goroutine:

| Check | Frequency | Action on failure |
|-------|-----------|-------------------|
| No symlinks in user dirs | Every 5m | Alert, remove symlink |
| Audit log writable | Every 5m | Alert, buffer in memory |
| RabbitMQ reachable | Every 60s | Halt message processing, alert |
| Failed registration attempts | Every 5m | Alert if > 10 failures in window |
| RabbitMQ Management API reachable | Every 60s | Warn, registration unavailable |

### 16.3 Rate Limiting (`internal/ratelimit/limiter.go`)

- In-memory per-user rate limiter using a sliding window counter
- Limits from `security.rate_limit.per_user` (configurable per-user override in `user_policies`)
- Applied in the router before queue publish
- Agent registration rate limit: per token prefix (not per user), from `agents.registration.rate_limit`

### 16.4 Audit Logging

All security-relevant events write to `admin_audit` table AND zap logger:
- `message.rejected` (from allowlist guard)
- `agent.register_failed` (from registration handler)
- `auth.failed` (from HMAC verification, WebSocket auth)
- All admin API operations

---

## Part 17 — CLI (`cmd/nipper/`, `cli/`)

### 17.1 Root Command

```
nipper [command]
```

Global flags:
- `--config` / `-c`: path to config file (default: `~/.open-nipper/config.yaml`)
- `--log-level`: override log level
- `--admin-url`: admin API URL (default: `http://127.0.0.1:18790`)

### 17.2 `nipper serve`

Starts the Gateway. This is the primary command.

```bash
nipper serve
nipper serve --config /etc/open-nipper/config.yaml
```

**Startup sequence (matches `GATEWAY_ARCHITECTURE.md`):**
1. Load and validate configuration
2. Initialize logger (JSON, structured)
3. Initialize OpenTelemetry (noop if not configured)
4. Expand `~` in datastore path
5. Open SQLite database, run migrations
6. Load users, identities, allowlist into cache
7. Run security audit
8. Initialize session store
9. Connect to RabbitMQ (internal queue system): declare topology
10. Connect to RabbitMQ Management API (if agent registration enabled)
11. Start HTTP main server (:18789)
12. Start Admin API server (:18790)
13. Start WebSocket handler
14. Start enabled channel adapters (WhatsApp → register webhook; Slack; MQTT; RabbitMQ channel)
15. Start cron scheduler
16. Start agent health monitor goroutine
17. Start runtime security check goroutine
18. Start event consumer (nipper-events-gateway)
19. Log `"gateway started"` at info level
20. Block on OS signal (SIGINT, SIGTERM)
21. Graceful shutdown: drain in-flight messages, close connections, close DB

### 17.3 `nipper admin user`

```bash
nipper admin user add --id user-01 --name "Alice" [--model claude-sonnet-4-20250514]
nipper admin user list
nipper admin user get user-01
nipper admin user update user-01 --name "Alice Smith" [--model ...] [--enable] [--disable]
nipper admin user delete user-01
```

All commands call the Admin API via HTTP and print JSON or human-readable output.

### 17.4 `nipper admin identity`

```bash
nipper admin identity add user-01 --channel whatsapp --identity "5491155553935@s.whatsapp.net"
nipper admin identity add user-01 --channel slack --identity "U0123ABC"
nipper admin identity list user-01
nipper admin identity remove user-01 --id 3
```

### 17.5 `nipper admin allow` / `nipper admin deny`

```bash
nipper admin allow user-01 --channel "*"
nipper admin allow user-02 --channel whatsapp
nipper admin deny user-03 --channel mqtt
nipper admin allowlist show
nipper admin allowlist show --channel whatsapp
```

### 17.6 `nipper admin agent`

```bash
nipper admin agent provision --user user-01 --label "anthropic-primary"
nipper admin agent list [--user user-01]
nipper admin agent get agt-user01-01
nipper admin agent rotate-token agt-user01-01
nipper admin agent revoke agt-user01-01
nipper admin agent delete agt-user01-01
```

`provision` output:
```
Agent provisioned successfully.
Agent ID:   agt-user01-01
Auth Token: npr_a1b2c3d4...

Save this token — it will not be shown again.

Start your agent with:
  NIPPER_GATEWAY_URL=http://gateway:18789 \
  NIPPER_AUTH_TOKEN=npr_a1b2c3d4... \
  python my_agent.py
```

### 17.7 `nipper admin backup`

```bash
nipper admin backup [--output /path/to/backup.db]
```

Calls `POST /admin/backup`. Prints backup file path on success.

### 17.8 `nipper admin health`

```bash
nipper admin health
```

Pretty-prints the health response.

### 17.9 `nipper admin audit`

```bash
nipper admin audit [--since 2026-02-21T00:00:00Z] [--action user.created] [--user user-01]
```

### 17.10 `nipper logs tail`

```bash
nipper logs tail
nipper logs tail --user alice --type thinking
nipper logs tail --type error
nipper logs tail --run run-7f3a
nipper logs tail --format json
```

Connects to RabbitMQ and consumes from:
- `nipper-logs-all` (default)
- `nipper-logs-user-{userId}` (if `--user` specified)
- `nipper-logs-errors` (if `--type error`)
- `nipper-logs-thinking` (if `--type thinking`)

Prints events formatted as table or raw JSON. `--format json` pipes raw AMQP message bodies.

### 17.11 `nipper plugins`

```bash
nipper plugins install ./my-plugin
nipper plugins install https://github.com/user/nipper-plugin-deploy.git
nipper plugins list
nipper plugins remove deploy
nipper plugins create my-plugin
nipper plugins test my-plugin --params '{"env": "staging"}'
nipper plugins validate my-plugin
```

**Plugin operations:**
- `install ./path`: copy plugin directory to `~/.open-nipper/plugins/{name}/`, validate structure
- `install url`: `git clone` to temp dir, validate, copy to plugins dir
- `list`: scan `~/.open-nipper/plugins/` and print table of name, version, description
- `remove name`: delete directory
- `create name`: scaffold directory with SKILL.md, scripts/run.sh, config.yaml templates
- `test name --params '{}'`: execute the plugin's entrypoint with given params in dry-run mode, print output
- `validate name`: check required files (SKILL.md, config.yaml), parse config, report any issues

---

## Part 18 — Gateway Lifecycle Details

### 18.1 Graceful Shutdown

On SIGINT or SIGTERM:
1. Stop accepting new connections on HTTP servers
2. Stop cron scheduler
3. Stop MQTT subscription
4. Stop RabbitMQ channel adapter consumer
5. Drain in-flight HTTP requests (30s timeout)
6. Stop event consumer (finish processing current batch)
7. Stop RabbitMQ publisher (wait for pending confirms if using publisher confirms)
8. Close all RabbitMQ connections
9. Close SQLite database
10. Flush OpenTelemetry spans and metrics
11. Log `"gateway stopped"` and exit 0

### 18.2 Error Recovery

| Failure | Recovery |
|---------|---------|
| RabbitMQ disconnect | Reconnect loop with exponential backoff (1s → 30s); buffer new messages during reconnect |
| Wuzapi unreachable | Log error; buffer outbound WhatsApp messages in memory; retry on reconnect |
| MQTT broker disconnect | Auto-reconnect via paho client; messages accumulate at broker |
| SQLite error | Log critical; reject all new messages (cannot verify users); surface in health endpoint |
| Channel delivery failure | Retry 3 times with 1s backoff; log error if all fail; do not nack the agent event |
| Management API unreachable | Log warn; `agents.status` returns last cached data; `/agents/register` returns 503 |

### 18.3 Connection Multiplexing

The Gateway uses separate AMQP channels for different purposes, all on the same AMQP connection:
- Channel 1: Publishing to `nipper.sessions` (inbound messages)
- Channel 2: Consuming from `nipper-events-gateway` (agent responses)
- Channel 3: Publishing to `nipper.control` (control signals)
- Channel 4+: RabbitMQ channel adapter (service-to-service)

---

## Part 19 — Implementation Order

Recommended implementation sequence:

| Phase | Features | Notes |
|-------|----------|-------|
| **Phase 1** | Project bootstrap, go.mod, config loader, Zap logger, OpenTelemetry noop | Foundation — everything depends on this |
| **Phase 2** | Data model types (`models/`), SQLite migrations, repository interface + implementation, cache | Datastore is needed by everything |
| **Phase 3** | RabbitMQ broker connection, topology declaration, publisher | Core queue infrastructure |
| **Phase 4** | Admin API server + all endpoints, CLI `admin` subcommands | Needed to provision users before testing channels |
| **Phase 5** | Allowlist guard, gateway session key resolver + DeliveryContext registry, `pkg/session/` agent library | Core pipeline logic; session files are agent-only |
| **Phase 6** | Gateway main HTTP server, webhook handlers, message pipeline router | Main traffic path |
| **Phase 7** | WhatsApp adapter (inbound + outbound), event consumer + dispatcher | First end-to-end channel |
| **Phase 8** | Slack adapter | Second channel |
| **Phase 9** | Agent registration endpoint (`/agents/register`) + Management API client | Enables agent bootstrapping |
| **Phase 10** | Agent health monitor | Observability |
| **Phase 11** | Cron adapter + scheduler | Scheduled tasks |
| **Phase 12** | MQTT adapter | IoT channel |
| **Phase 13** | RabbitMQ channel adapter | Service-to-service channel |
| **Phase 14** | WebSocket API | Internal clients |
| **Phase 15** | Security audit, runtime checks, rate limiting | Hardening |
| **Phase 16** | OpenTelemetry instrumentation (actual tracing + metrics) | Observability |
| **Phase 17** | CLI: `serve`, `logs tail`, `plugins` | Complete CLI |
| **Phase 18** | Graceful shutdown, config hot-reload, connection recovery hardening | Production readiness |

---

## Part 20 — Feature Checklist

### Configuration
- [ ] YAML config file loading with `~` expansion
- [ ] `config.local.yaml` overlay
- [ ] Environment variable overrides with `NIPPER_` prefix
- [ ] Inline `${ENV_VAR}` resolution in config values
- [ ] Config struct with all fields
- [ ] Config validation at startup

### Logger
- [ ] Single `*zap.Logger` initialized in `main.go`
- [ ] JSON output format
- [ ] Log level from `NIPPER_LOG_LEVEL` env var
- [ ] Logger passed to all subsystems (no globals)
- [ ] Structured fields: `userId`, `sessionKey`, `channelType`, `traceId`

### OpenTelemetry
- [ ] Noop provider when telemetry disabled (no log noise)
- [ ] OTLP trace exporter (HTTP)
- [ ] OTLP metrics exporter (HTTP)
- [ ] Prometheus metrics exporter
- [ ] `otelhttp` middleware on main HTTP server
- [ ] Trace context propagation in AMQP headers
- [ ] Span instrumentation: HTTP handlers, datastore, RabbitMQ, channel delivery
- [ ] All metric instruments defined and recorded
- [ ] Graceful fallback if OTEL endpoint unreachable

### Datastore
- [ ] `Repository` interface
- [ ] SQLite implementation (WAL mode, foreign keys)
- [ ] All 4 migrations with version tracking
- [ ] `users` CRUD
- [ ] `user_identities` CRUD + lookup
- [ ] `allowed_list` CRUD
- [ ] `user_policies` CRUD
- [ ] `agents` CRUD + token hash lookup
- [ ] `admin_audit` append + query
- [ ] In-memory cache layer with TTL and invalidation
- [ ] Hot backup via `sqlite3_backup` API
- [ ] `Repository` interface allows backend swap to PostgreSQL

### Channel Adapters
- [ ] `ChannelAdapter` interface
- [ ] WhatsApp: inbound normalization (text, image, audio, video, document, location, contact)
- [ ] WhatsApp: HMAC verification
- [ ] WhatsApp: outbound delivery (text, image, document, typing indicator, mark-as-read)
- [ ] WhatsApp: webhook registration at startup
- [ ] Slack: inbound normalization
- [ ] Slack: signature verification (replay-safe)
- [ ] Slack: outbound delivery (create + update for streaming)
- [ ] Slack: `url_verification` challenge handler
- [ ] Cron: scheduler with cron expressions
- [ ] Cron: user validation at startup
- [ ] Cron: `notifyChannels` broadcast delivery
- [ ] MQTT: subscribe to per-user inbox topics
- [ ] MQTT: outbound publish to `responseTopic` or outbox
- [ ] MQTT: reconnect with backoff
- [ ] RabbitMQ channel: per-user inbound queues
- [ ] RabbitMQ channel: outbound with `correlationId`

### Internal Queue (Gateway↔Agent)
- [ ] AMQP connection with reconnect
- [ ] Exchange/queue/binding topology declaration (idempotent)
- [ ] Per-user agent queue declaration (at agent provisioning)
- [ ] Per-user control queue declaration
- [ ] Publisher: `nipper.sessions` exchange
- [ ] Event consumer: `nipper-events-gateway` queue
- [ ] Control publisher: `nipper.control` exchange
- [ ] Backpressure handling on `nack` (queue full)
- [ ] Dead-letter exchange routing
- [ ] Management API client (CRUD for RMQ users + queue info)

### Message Pipeline
- [ ] Router: normalize → resolve user → allowlist → session → deduplicate → queue mode → publish
- [ ] Allowlist guard (unknown identity, user disabled, not in allowlist)
- [ ] Per-channel rejection behavior (always 200 for HTTP channels)
- [ ] Audit log entry on every rejection
- [ ] Collect mode with debounce timer and cap
- [ ] Steer mode
- [ ] Interrupt mode + control signal
- [ ] Deduplication cache (`message-id` and `prompt` strategies)

### Session Management
- [ ] Session store (filesystem-based)
- [ ] Session creation (atomic: dir + transcript + meta + index)
- [ ] Session resolver per channel type
- [ ] Session index (cached 45s TTL)
- [ ] JSONL transcript (append-only)
- [ ] Session metadata JSON
- [ ] File-based locking with stale detection
- [ ] Session archive (on reset/delete)

### Event Dispatcher
- [ ] `NipperEvent` consumer from `nipper-events-gateway`
- [ ] `DeliveryContext` lookup from session store
- [ ] Delta buffering for non-streaming channels
- [ ] Final response assembly on `done` event
- [ ] Streaming delivery for Slack, WebSocket
- [ ] Broadcast delivery for `replyMode: "broadcast"`
- [ ] WebSocket event fan-out

### HTTP Main Server (:18789)
- [ ] `/webhook/whatsapp` handler
- [ ] `/webhook/slack` handler  
- [ ] `/agents/register` handler with token auth
- [ ] `/ws` WebSocket endpoint
- [ ] `/health` endpoint
- [ ] `otelhttp` middleware
- [ ] Graceful shutdown

### Admin API (:18790)
- [ ] Bind to `127.0.0.1` only
- [ ] Optional Bearer token auth
- [ ] User CRUD endpoints
- [ ] Identity management endpoints
- [ ] Allowlist management endpoints
- [ ] Policy management endpoints
- [ ] Agent provisioning endpoints (provision, list, get, delete, rotate, revoke)
- [ ] Token generation (`npr_` prefix, 48 bytes, base62, SHA-256 hash stored)
- [ ] RabbitMQ queue provisioning during agent provision
- [ ] Health endpoint
- [ ] Audit log query endpoint
- [ ] Backup endpoint
- [ ] Config view endpoint (secrets redacted)
- [ ] All actions logged to `admin_audit`

### WebSocket API
- [ ] JSON-RPC protocol
- [ ] Optional auth gate
- [ ] `sessions.*` methods
- [ ] `chat.send` and `chat.stream` methods
- [ ] `agents.status` method
- [ ] `config.get` / `config.set` methods
- [ ] Server-push event delivery

### Agent Health Monitor
- [ ] Background goroutine every 30s
- [ ] `nipper_agent_consumer_count` gauge update
- [ ] `degraded` status after `consumer_timeout_seconds`
- [ ] Health status exposed via admin health endpoint and WebSocket

### Security
- [ ] Startup security audit (all checks)
- [ ] Runtime security checks (background goroutine)
- [ ] Per-user rate limiting (in-memory sliding window)
- [ ] Agent registration rate limiting (per token prefix)
- [ ] Audit logging for all security events
- [ ] No secrets in log output (redaction)
- [ ] Admin server on localhost only (enforced in code, not just config)

### CLI
- [ ] `nipper serve`
- [ ] `nipper admin user [add|list|get|update|delete]`
- [ ] `nipper admin identity [add|list|remove]`
- [ ] `nipper admin allow/deny/allowlist`
- [ ] `nipper admin agent [provision|list|get|rotate-token|revoke|delete]`
- [ ] `nipper admin health`
- [ ] `nipper admin audit`
- [ ] `nipper admin backup`
- [ ] `nipper logs tail [--user] [--type] [--run] [--format]`
- [ ] `nipper plugins [install|list|remove|create|test|validate]`
- [ ] Global `--config`, `--log-level`, `--admin-url` flags
- [ ] Human-readable and JSON output modes

### Production Readiness
- [ ] Graceful shutdown (all subsystems)
- [ ] Reconnect loops (RabbitMQ, MQTT, Wuzapi)
- [ ] Backpressure handling
- [ ] Config hot-reload for whitelisted settings
- [ ] Startup sequence exactly as documented
- [ ] All error paths return informative logs
- [ ] No goroutine leaks (all goroutines shut down cleanly)
- [ ] Context propagation throughout (cancellation on shutdown)

---

## Appendix: Key Design Decisions

1. **No global state.** The logger, config, and datastore are initialized once and passed as explicit dependencies. No `init()` side effects, no package-level vars that hold application state.

2. **Interface-first datastore.** The `Repository` interface is defined before any SQLite code is written. This ensures the SQLite implementation stays honest to the interface and a PostgreSQL backend can be dropped in.

3. **Adapter isolation.** Each channel adapter is fully self-contained. The main router does not have any channel-specific logic — it only calls the `ChannelAdapter` interface. Adding a new channel requires only implementing the interface and registering the adapter.

4. **Always-200 for webhook channels.** WhatsApp and Slack webhooks always receive HTTP 200, regardless of allowlist outcome. This is intentional: non-200 causes retries, and we do not want to reveal the system's existence to unauthorized senders.

5. **Noop telemetry by default.** If telemetry is disabled (default), the entire OpenTelemetry stack uses noop providers. There are zero error messages, zero panics, and zero log noise related to OTEL. The instrumentation calls (`tracer.Start()`, `meter.Int64Counter()`, etc.) are in the code regardless — the noop implementations make them into no-ops.

6. **Security audit is advisory, not blocking.** The startup audit logs findings but does not prevent the Gateway from starting. This avoids a misconfigured `~/.open-nipper/` permissions file preventing an otherwise-healthy system from starting. Critical findings are logged at ERROR level so they are visible in production log aggregators.

7. **Token design mirrors GitHub/Slack/OpenAI.** The `npr_` prefix makes these tokens easy to identify in logs, grep for in code, and catch with secret scanners (e.g., GitHub's push protection, Doppler, detect-secrets). The prefix is part of the token before hashing so the stored hash is of the full `npr_...` string.

8. **File locks as defense-in-depth.** The RabbitMQ `prefetch: 1` already serializes agent processing. File locks on transcript writes are an additional safety net: if a bug in the queue allows two messages through simultaneously, the transcript is still protected. The lock overhead is negligible for 1-3 users.
