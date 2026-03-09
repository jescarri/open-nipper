# Queue Architecture

## Overview

Open-Nipper uses a **RabbitMQ-backed queue system** adapted from OpenClaw's `command-queue` pattern. Messages never execute immediately — they enter RabbitMQ queues that enforce ordering, prevent race conditions, and control concurrency. Agents **consume** work from RabbitMQ queues; the Gateway **publishes** into them.

**RabbitMQ is the sole communication transport between the Gateway and agents.** The Gateway and agents never communicate directly — all messages (inbound `NipperMessage` items, outbound `NipperEvent` streams, acks, and control signals) flow through RabbitMQ exchanges and queues. This provides durable, inspectable, and decoupled communication that survives agent process restarts.

The queue is the synchronization boundary between the Gateway (fast, concurrent, multi-channel) and the Agent process (serial per user, expensive, AI-bound).

## Design Principles

1. **RabbitMQ as the backbone** — All Gateway↔Agent communication flows through RabbitMQ. No Unix sockets, no direct HTTP, no shared memory. RabbitMQ provides durability, routing, dead-lettering, and observability out of the box.
2. **Per-user serial execution** — Messages for a user execute in strict FIFO order via a single per-user queue (`nipper-agent-{userId}`). `prefetch: 1` on the agent side prevents parallel processing. This ensures conversational coherence and prevents transcript corruption.
3. **Per-user isolation** — User A's queue is invisible to User B. No cross-user queue inspection or manipulation. Enforced via separate RabbitMQ queues and consumer bindings.
4. **Agent pull model** — Agents consume from their per-user RabbitMQ queue using AMQP `basic.consume`. The Gateway publishes but never pushes directly into agent processes. The Gateway never starts, stops, or manages agents.

## Queue Topology

### RabbitMQ Exchange and Queue Layout

```
┌──────────────────────────────────────────────────────────────────────┐
│                         RABBITMQ BROKER                              │
│                         vhost: /nipper                               │
│                                                                      │
│  INBOUND (Gateway → Agent)                                           │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Exchange: nipper.sessions (topic, durable)                    │  │
│  │                                                                │  │
│  │  Routing key: nipper.sessions.{userId}.{sessionId}             │  │
│  │                                                                │  │
│  │  Bindings (one per user):                                      │  │
│  │    nipper.sessions.alice.# → queue: nipper-agent-alice         │  │
│  │    nipper.sessions.bob.#   → queue: nipper-agent-bob           │  │
│  │    nipper.sessions.carol.# → queue: nipper-agent-carol         │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  Per-user agent queues (one per user, created at provisioning):      │
│  ┌──────────────────────┐ ┌──────────────────────┐                   │
│  │ nipper-agent-alice   │ │ nipper-agent-bob     │                   │
│  │  [msg][msg][msg]     │ │  [msg]               │  ...             │
│  │  prefetch: 1         │ │  prefetch: 1         │                   │
│  │  consumer: 1 (agent) │ │  consumer: 1 (agent) │                   │
│  └──────────────────────┘ └──────────────────────┘                   │
│                                                                      │
│  OUTBOUND (Agent → Gateway)                                          │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Exchange: nipper.events (topic, durable)                      │  │
│  │                                                                │  │
│  │  Routing key: nipper.events.{userId}.{sessionId}               │  │
│  │                                                                │  │
│  │  Binding:                                                      │  │
│  │    nipper.events.# → queue: nipper-events-gateway              │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  Gateway consumes from:                                              │
│  ┌──────────────────────────────────┐                                │
│  │ nipper-events-gateway            │  ← All agent events            │
│  │  [event][event][event]           │     (delta, tool_*, done, etc.) │
│  └──────────────────────────────────┘                                │
│                                                                      │
│  CONTROL (Gateway → Agent)                                           │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Exchange: nipper.control (topic, durable)                     │  │
│  │                                                                │  │
│  │  Routing key: nipper.control.{userId}                          │  │
│  │  Bindings:                                                     │  │
│  │    nipper.control.alice → queue: nipper-control-alice           │  │
│  │    nipper.control.bob   → queue: nipper-control-bob             │  │
│  │  Messages: interrupt, abort                                    │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  DEAD LETTER                                                         │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │  Exchange: nipper.sessions.dlx → queue: nipper-sessions-dlq    │  │
│  │  Failed messages after max retries land here for inspection    │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

### Why RabbitMQ for Gateway↔Agent Communication?

| Property | Benefit |
|----------|---------|
| **Durable queues** | Messages survive broker restarts. No message loss on agent process restart. |
| **Per-queue prefetch** | `prefetch: 1` enforces serial execution per user without application-level locking. |
| **Dead-letter exchange** | Failed messages are captured for inspection, not silently dropped. |
| **Topic routing** | The Gateway publishes to a single exchange; RabbitMQ routes to the correct user's agent queue via binding keys. |
| **Native ack/nack** | AMQP acknowledgment semantics map directly to the agent's processing lifecycle. |
| **Management API** | Queue depth, consumer count, and message rates are visible via RabbitMQ's management plugin — free observability. |
| **Shared broker** | The same RabbitMQ instance serves the RabbitMQ channel adapter (service-to-service inbound) and the internal queue system, reducing infrastructure complexity. |
| **Network-only coupling** | Agents connect to RabbitMQ over the network (AMQP or AMQPS with TLS). No filesystem mounts, no Unix sockets, no shared memory between Gateway and agents. See `AGENT_ARCHITECTURE.md`. |

## Per-User Agent Queue

One RabbitMQ queue per user. All messages for a user — regardless of channel or session — land in this single queue. The `sessionKey` inside each message tells the agent which session context to load. Queues are created automatically when an agent is provisioned via the admin API (see `GATEWAY_ARCHITECTURE.md`, Agent Provisioning) and are durable — they survive broker restarts.

**Queue naming:**

```
nipper-agent-{userId}
```

Example: `nipper-agent-alice`

**Queue properties:**

```yaml
durable: true
exclusive: false
autoDelete: false
arguments:
  x-dead-letter-exchange: "nipper.sessions.dlx"
  x-dead-letter-routing-key: "nipper.sessions.dlq"
  x-message-ttl: 300000       # 5 min TTL — stale messages expire
  x-max-length: 50            # Backpressure: max 50 queued items per user
  x-overflow: "reject-publish" # Gateway gets nack if queue is full
```

### QueueItem

```typescript
interface QueueItem {
  id: string;                  // UUIDv7
  message: NipperMessage;      // Canonical message from Gateway
  enqueuedAt: string;          // ISO 8601
  priority: number;            // Higher = processed first (default: 0)
  mode: QueueMode;
  dedupeKey?: string;          // For deduplication
}
```

### Behavior

- When a message arrives for any session belonging to user `U`, the Gateway publishes to `nipper.sessions` exchange with routing key `nipper.sessions.{userId}.{sessionId}`.
- RabbitMQ routes the message to `nipper-agent-{userId}` queue (bound to `nipper.sessions.{userId}.#`).
- The user's agent process consumes from this queue with `prefetch: 1`, ensuring serial processing.
- If the agent is idle, it receives the message immediately. If it's busy processing another message, the new message waits in the RabbitMQ queue.
- On successful processing, the agent sends `basic.ack`. On failure, it sends `basic.nack` (message goes to DLX after retries).
- The agent uses the `sessionKey` field from each `QueueItem.message` to load the correct session transcript, metadata, and memory context.

## Queue Modes

Adapted from OpenClaw's queue mode system (`src/auto-reply/reply/queue/types.ts`):

| Mode        | Behavior                                                         |
|-------------|------------------------------------------------------------------|
| `queue`     | Simple FIFO. Messages wait their turn. Default mode.             |
| `steer`     | Process immediately if idle; if busy, queue and process next.    |
| `followup`  | Queue message as a follow-up; process after current run ends.    |
| `collect`   | Batch multiple messages into one prompt. Wait for debounce.      |
| `interrupt` | Cancel current run, process this message immediately.            |

### Queue Mode Configuration

```yaml
queue:
  defaultMode: "steer"

  perChannel:
    whatsapp:
      mode: "collect"          # WhatsApp users tend to send rapid-fire messages
      debounceMs: 2000
      collectCap: 5           # Max 5 messages in one batch
    slack:
      mode: "steer"
      debounceMs: 500         # Wait 500ms for more messages before processing
    cron:
      mode: "queue"
      debounceMs: 0
      priority: -1            # Lower priority than interactive messages
    mqtt:
      mode: "queue"           # Machine messages are processed in strict order
      debounceMs: 0
    rabbitmq:
      mode: "queue"           # Service-to-service messages in strict order
      debounceMs: 0
```

### Collect Mode (Message Batching)

When `collect` mode is active and multiple messages arrive within the debounce window:

```
Message 1 arrives at T+0:    "check the logs"
Message 2 arrives at T+800:  "specifically for the payments service"
Message 3 arrives at T+1500: "from the last hour"

Debounce window: 2000ms
After T+2000 (no more messages):

Collected prompt sent to agent:
  "The user sent multiple messages in quick succession:
   1. check the logs
   2. specifically for the payments service
   3. from the last hour"
```

### Interrupt Mode

For urgent messages that should preempt the current run:

```
1. Current agent run is processing message A
2. User sends "STOP" or an urgent command
3. Gateway publishes interrupt-mode message to nipper.sessions exchange
4. Gateway publishes ControlMessage { type: "interrupt" } to nipper.control exchange
5. Agent receives control message on its control queue consumer
6. Agent cancels current run (partial results discarded or saved), nack's message A
7. Agent picks up the interrupt message from the agent queue
```

## Deduplication

Messages can be deduplicated to prevent double-processing (common with webhook retries):

| Strategy     | Deduplication Key              | Use Case                          |
|-------------|-------------------------------|-----------------------------------|
| `message-id` | `originMessageId`            | Webhook retry protection          |
| `prompt`     | Hash of `content.text`        | User double-sends the same text   |
| `none`       | No deduplication              | Every message is processed        |

```typescript
interface DedupeConfig {
  strategy: "message-id" | "prompt" | "none";
  windowMs: number;           // Dedup window (default: 30000ms)
}
```

The deduplication cache is per-session and in-memory. With 1-3 users, the memory footprint is negligible.

## Queue Item Lifecycle

```
┌──────────┐     ┌──────────┐     ┌────────────┐     ┌───────────┐
│ ENQUEUED │────▶│ WAITING  │────▶│ PROCESSING │────▶│ COMPLETED │
└──────────┘     └──────────┘     └────────────┘     └───────────┘
                      │                 │
                      │                 │           ┌───────────┐
                      │                 └──────────▶│  FAILED   │
                      │                             └───────────┘
                      │
                      │           ┌───────────┐
                      └──────────▶│  DROPPED  │  (collect cap exceeded,
                                  └───────────┘   or dedup match)
```

## Agent Consume Model (AMQP)

Agents do not receive messages pushed by the Gateway over direct connections. Instead, agents consume from their dedicated per-user RabbitMQ queue using standard AMQP `basic.consume` with `prefetch: 1`.

```typescript
interface AgentQueueClient {
  // Connect to RabbitMQ broker
  connect(brokerUrl: string, credentials: AmqpCredentials): Promise<AmqpConnection>;

  // Start consuming from the per-user agent queue
  consume(agentQueueName: string): AsyncIterable<AmqpMessage<QueueItem>>;

  // Acknowledge successful processing
  ack(deliveryTag: number): void;

  // Negative acknowledgment — requeue or dead-letter
  nack(deliveryTag: number, requeue: boolean): void;

  // Publish events back to Gateway via the events exchange
  publishEvent(event: NipperEvent): void;

  // Subscribe to control messages (interrupt, abort)
  consumeControl(controlQueueName: string): AsyncIterable<AmqpMessage<ControlMessage>>;
}
```

### Consume Flow

```
Agent process starts with NIPPER_GATEWAY_URL + NIPPER_AUTH_TOKEN
        │
        ▼
Auto-register with Gateway:
  POST /agents/register → receive RabbitMQ config (URL, credentials, queue names)
  See GATEWAY_ARCHITECTURE.md, Agent Auto-Registration Endpoint
        │
        ▼
Connect to RabbitMQ broker using received credentials
        │
        ▼
Declare/assert agent queue: nipper-agent-{userId}
  Set prefetch: 1 (serial processing)
        │
        ▼
basic.consume(agentQueue) — blocks until message arrives
        │
        ▼
Message delivered by RabbitMQ
        │
        ▼
Agent reads sessionKey from QueueItem.message, loads session context
        │
        ▼
Agent processes message (AI inference + tool execution)
        │
        ▼
Agent publishes NipperEvents to nipper.events exchange:
  Routing key: nipper.events.{userId}.{sessionId}
  Gateway consumes from nipper-events-gateway queue
        │
        ▼
Agent sends basic.ack(deliveryTag)
  RabbitMQ removes message from agent queue
        │
        ▼
Agent continues consuming (blocks until next message)
```

### Control Messages

The Gateway can send control signals to agents via the `nipper.control` exchange:

```typescript
type ControlMessage =
  | { type: "interrupt"; reason: string }     // Cancel current run, process next
  | { type: "abort"; reason: string }         // Cancel current run, do not process next
```

Agents consume from a per-user control queue (`nipper-control-{userId}`) in a separate AMQP channel, allowing them to receive interrupt signals while processing a message.

## RabbitMQ Communication Protocol

All Gateway↔Agent communication flows through RabbitMQ exchanges on the same broker (vhost: `/nipper`):

| Exchange | Type | Direction | Purpose |
|----------|------|-----------|---------|
| `nipper.sessions` | topic | Gateway → Agent | Inbound `NipperMessage` items for agent processing |
| `nipper.events` | topic | Agent → Gateway | Outbound `NipperEvent` streams (delta, tool_*, done) |
| `nipper.control` | topic | Gateway → Agent | Control signals (interrupt, abort, shutdown) |
| `nipper.sessions.dlx` | fanout | Dead letter | Failed messages after max retries |
| `nipper.logs` | topic | Gateway → Observers | Observability events (audit, metrics, rejections) |

### Message Format (AMQP Properties)

**Inbound (Gateway → Agent):**

```
Exchange: nipper.sessions
Routing key: nipper.sessions.alice.sess-a1b2
Content-Type: application/json
Message-ID: 01956a3c-...     (QueueItem.id)
Correlation-ID: 01956a3c-... (for request-response tracing)
Timestamp: 1708512005
Headers:
  x-nipper-user-id: alice
  x-nipper-session-key: user:alice:channel:whatsapp:session:sess-a1b2
  x-nipper-queue-mode: steer
  x-nipper-priority: 0

Body: { ... QueueItem (JSON) ... }
```

RabbitMQ routes this to queue `nipper-agent-alice` via the binding `nipper.sessions.alice.#`.

**Outbound (Agent → Gateway):**

```
Exchange: nipper.events
Routing key: nipper.events.alice.sess-a1b2
Content-Type: application/json
Correlation-ID: 01956a3c-...  (links back to inbound message)
Headers:
  x-nipper-user-id: alice
  x-nipper-session-key: user:alice:channel:whatsapp:session:sess-a1b2
  x-nipper-event-type: delta | tool_start | tool_end | done | error

Body: { ... NipperEvent (JSON) ... }
```

**Control (Gateway → Agent):**

```
Exchange: nipper.control
Routing key: nipper.control.alice
Content-Type: application/json
Headers:
  x-nipper-control-type: interrupt | abort

Body: { ... ControlMessage (JSON) ... }
```

## Backpressure

Backpressure is enforced at multiple levels, with RabbitMQ providing the primary enforcement:

| Condition                        | Enforced By | Response                                           |
|----------------------------------|-------------|----------------------------------------------------|
| Agent queue > 50 items           | RabbitMQ `x-max-length` + `reject-publish` | Gateway receives `basic.nack` from broker, returns backpressure signal to channel |
| Collect cap exceeded             | Gateway (pre-publish) | Drop oldest messages in batch (configurable)       |
| Agent offline (0 consumers)      | Gateway health check | Log warning, messages queue up to `x-max-length`; Gateway surfaces degraded status |
| Message TTL expired (5 min)      | RabbitMQ `x-message-ttl` | Message dead-lettered to `nipper.sessions.dlx`     |

### Drop Policies (for Collect Mode)

| Policy      | Behavior                                              |
|-------------|-------------------------------------------------------|
| `old`       | Drop oldest messages when cap is exceeded             |
| `new`       | Reject new messages when cap is exceeded              |
| `summarize` | Summarize excess messages into a single line          |

## Metrics and Observability

Every queue operation emits metrics:

```typescript
interface QueueMetrics {
  // Counters
  messagesEnqueued: Counter;       // Total messages entering queues
  messagesProcessed: Counter;      // Total messages successfully processed
  messagesFailed: Counter;         // Total messages that failed processing
  messagesDropped: Counter;        // Total messages dropped (dedup, cap, etc.)

  // Gauges
  queueDepth: Gauge;               // Current items waiting per user
  activeAgents: Gauge;             // Agents currently processing a message
  consumerCount: Gauge;            // Connected agent consumers per queue

  // Histograms
  queueWaitTime: Histogram;        // Time from enqueue to processing start
  processingTime: Histogram;       // Time from processing start to ack
  endToEndLatency: Histogram;      // Time from enqueue to ack
}
```

Labels: `userId`, `channelType`, `sessionKey`, `queueMode`.

Additionally, RabbitMQ's management plugin exposes native metrics for all agent queues:
- Queue depth (messages ready + unacknowledged)
- Consumer count per queue (0 = agent offline, 1 = healthy)
- Publish/deliver rates
- Message age (time in queue)

These are available via the RabbitMQ Management API (`http://localhost:15672/api/`) and can be scraped by Prometheus via the `rabbitmq_prometheus` plugin.

## Queue Persistence and Durability

Agent queues are **durable RabbitMQ queues**. Messages are persistent (delivery mode 2). This means:

- **Messages survive RabbitMQ broker restarts** — queued items are written to disk by RabbitMQ.
- **Messages survive agent process restarts** — when an agent process is restarted, it reconnects to its queue and resumes consuming. Unacknowledged messages are redelivered automatically by RabbitMQ.
- **Messages survive Gateway restarts** — messages already published to RabbitMQ are safe. The Gateway reconnects and resumes publishing/consuming.

**What happens on crash/restart?**
- **Agent process crashes** — The in-flight message (not yet ack'd) is redelivered by RabbitMQ when the agent reconnects. No message loss. Partial agent output (streaming events already published to `nipper.events`) may have been delivered to the user; the Gateway handles idempotent redelivery.
- **Gateway crashes** — Messages already in RabbitMQ queues are safe. Agent events published to `nipper.events` queue accumulate until the Gateway reconnects and drains them. Channel delivery resumes.
- **RabbitMQ broker crashes** — Durable queues and persistent messages are recovered from disk on broker restart. In-flight messages (not yet ack'd by agents) are redelivered.
- **Network partition** — RabbitMQ's built-in partition handling applies. The system uses `pause-minority` mode for consistency over availability.

### Queue Lifecycle

Agent queues (`nipper-agent-{userId}`) are long-lived. They are created automatically when an agent is provisioned via the admin API (`POST /admin/agents` — see `GATEWAY_ARCHITECTURE.md`) and exist for the lifetime of the user. The provisioning process declares the queue, sets its properties, and creates the binding on the `nipper.sessions` exchange. The agent's RabbitMQ credentials (created during auto-registration) are scoped to only access these queues.

When an agent is **deprovisioned** (`DELETE /admin/agents/{agentId}`), the agent's RabbitMQ user is deleted (severing connections), but the queues are **not** deleted — they belong to the user, not the agent. A new agent can be provisioned for the same user.

When a **user** is deleted via the admin API, the Gateway deletes all associated agents and their queues:

```
User deleted via admin API
        │
        ▼
Gateway deletes associated agent records and RabbitMQ users
        │
        ▼
Gateway deletes RabbitMQ queues:
  nipper-agent-{userId}
  nipper-control-{userId}
        │
        ▼
Any remaining messages in the queues are discarded
```

## RabbitMQ Configuration

```yaml
queue:
  transport: "rabbitmq"               # Explicit: RabbitMQ is the only supported transport

  rabbitmq:
    url: "amqp://localhost:5672"
    username: "${RABBITMQ_USERNAME}"
    password: "${RABBITMQ_PASSWORD}"
    vhost: "/nipper"
    heartbeat: 60
    reconnect:
      enabled: true
      initialDelayMs: 1000
      maxDelayMs: 30000

    exchanges:
      sessions:
        name: "nipper.sessions"
        type: "topic"
        durable: true
      events:
        name: "nipper.events"
        type: "topic"
        durable: true
      control:
        name: "nipper.control"
        type: "topic"
        durable: true
      dlx:
        name: "nipper.sessions.dlx"
        type: "fanout"
        durable: true

    agentQueues:
      durable: true
      prefetch: 1                     # Serial processing per user
      messageTtl: 300000              # 5 min TTL
      maxLength: 50                   # Max queued items per user
      overflow: "reject-publish"
      deadLetterExchange: "nipper.sessions.dlx"

    eventsQueue:
      name: "nipper-events-gateway"
      durable: true
      prefetch: 50                    # Gateway can handle many events concurrently

    dlq:
      name: "nipper-sessions-dlq"
      durable: true
      messageTtl: 86400000            # 24h TTL for dead-lettered messages
```

The same RabbitMQ broker instance is shared with the RabbitMQ channel adapter (see `GATEWAY_ARCHITECTURE.md`) and the observability logger (see `AGENT_ARCHITECTURE.md`), but each uses a separate set of exchanges and queues within the `/nipper` vhost.

## Cron Queue Integration

Cron jobs are a special case: they produce messages without a user typing anything.

```
Cron Scheduler
        │
        ▼
CronAdapter.normalizeInbound() → NipperMessage with channelType: "cron"
        │
        ▼
Gateway publishes to nipper.sessions exchange → lands in nipper-agent-{userId}
        │
        ▼
Agent processes like any other message
        │
        ▼
Response routed via DeliveryContext (see GATEWAY_ARCHITECTURE.md):
  ├── notifyChannels: ["slack:C0789GHI"] → Delivered via Slack adapter
  ├── notifyChannels: ["whatsapp:5491155553935@s.whatsapp.net"] → Delivered via WhatsApp adapter (Wuzapi)
  └── No notifyChannels → Logged, no delivery
```

Cron messages have lower priority than interactive messages (default priority: -1 vs 0). Since all messages for a user land in a single queue with `prefetch: 1`, cron messages wait behind interactive messages naturally.

## Cross-Channel Message Handling

All messages for a user — regardless of channel — land in the same per-user agent queue (`nipper-agent-{userId}`). They are processed serially in FIFO order.

```
WhatsApp msg → nipper-agent-alice → [msg-from-whatsapp, msg-from-slack, msg-from-mqtt] → serial
Slack msg    → nipper-agent-alice ↗
MQTT msg     → nipper-agent-alice ↗
                                     Agent response for msg-from-whatsapp → WhatsApp (via Wuzapi)
                                     Agent response for msg-from-slack    → Slack (via Slack API)
                                     Agent response for msg-from-mqtt     → MQTT (via outbox topic)
```

Each message carries its own `deliveryContext`, so the agent's response is always routed back through the correct channel adapter regardless of which channel the message came from.
