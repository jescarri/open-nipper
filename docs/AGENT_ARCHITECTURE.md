# Agent Architecture

## Overview

Agents are the execution units of Open-Nipper. Each agent is a **standalone process** that connects to RabbitMQ, consumes messages from a **per-user queue**, and publishes response events back. Agents can execute tools (shell commands, web fetch, document parsing, MCP-loaded tools), resolve secrets from **environment variables**, and route responses back to the user through the Gateway's channel adapters (WhatsApp, Slack, MQTT, RabbitMQ) using the `DeliveryContext` attached to each inbound `NipperMessage` — including headless channels like cron (see `GATEWAY_ARCHITECTURE.md`).

**The reference Go agent uses the [Eino SDK](https://github.com/cloudwego/eino) for its agentic loop.** Specifically, it uses `react.NewAgent` (ReAct pattern) for tool-calling model interactions and `eino-ext` components for LLM providers, MCP integration, and container sandbox execution.

**The Gateway never starts, stops, or manages agents.** The Gateway publishes messages to RabbitMQ and consumes events from RabbitMQ. Everything in between is the agent's responsibility. The Gateway detects agent availability by monitoring consumer count on queues via the RabbitMQ Management API — nothing more.

**Agents are polyglot.** An agent can be written in any programming language — Go, Python, TypeScript, Rust, Java — as long as it speaks AMQP and conforms to the `NipperMessage`/`NipperEvent` protocol. The reference implementation is Go + Eino.

**Agents support OpenAI and LocalAI.** The reference agent supports any OpenAI-compatible API (OpenAI, Azure OpenAI, LocalAI, vLLM) via `eino-ext/components/model/openai` and local Ollama models via `eino-ext/components/model/ollama`. Switching providers is a config change.

**Agents are specialized per user.** Different users can be served by completely different agent implementations. The routing is implicit: each agent process consumes from its user's queue.

**Secrets come from environment variables.** Config fields using `${VAR}` syntax are expanded from the process environment at startup. No external secret manager dependency (1Password `op` CLI remains an option for operators who mount it in the agent container, but env vars are the default path). Agent config (including `base_path`, telemetry endpoints, and inference keys) is loaded from the config file with env vars expanded; the default `base_path` is `${HOME}/.open-nipper`, which is resolved to the actual home directory at load time.

**Telemetry is optional.** If OpenTelemetry endpoints are not configured or telemetry is disabled in the agent config, the agent runs without instrumentation and without emitting telemetry-related errors or log noise.

**Defense in depth:** The agent must not execute destructive operations (e.g. delete data, format disks, rm -rf) even if the user asks. The reference agent enforces this via a global safety preamble in the system prompt, per-tool security directives, and tool-level blocklists (e.g. bash command validation).

## Design Principles

1. **User-bound** — One agent process per user. The agent consumes from a single per-user queue (`nipper-agent-{userId}`) and handles all sessions for that user. The `sessionKey` inside each message tells the agent which session context to load.
2. **Gateway-decoupled** — The Gateway and agents communicate exclusively through RabbitMQ. The Gateway never starts, stops, inspects, or manages agent processes. It publishes messages and consumes events. Period.
3. **Eino-powered** — The reference Go agent uses the Eino SDK's `react.NewAgent` (ReAct pattern) for the agentic loop: ChatModel → Tools → ChatModel until done. Tools implement Eino's `tool.BaseTool` interface.
4. **Multi-provider** — The agent supports OpenAI-compatible APIs and Ollama via Eino model components. Provider switching is a config change (`inference.provider` + `inference.base_url`).
5. **RabbitMQ-coupled** — Agents consume messages from their per-user queue (`nipper-agent-{userId}`) and publish response events to the `nipper.events` exchange. RabbitMQ is the sole communication transport between agents and the Gateway (see `QUEUE_ARCHITECTURE.md`).
6. **Env-var secrets** — All secrets are resolved from environment variables at startup. Config fields use `${VAR}` syntax. No external secret manager dependency by default.
7. **Sandboxed execution** — Shell commands run inside a Docker container sandbox (`eino-ext/sandbox.DockerSandbox`) with configurable image (default: `ubuntu:noble`), resource limits, and volume mounts.
8. **MCP-extensible** — External tools can be loaded from MCP servers (STDIO and SSE transports), configured via YAML with `${VAR}` env-var expansion in all fields including auth headers. See `MCP_NEXT_STEPS.md` for OAuth2 integration plans.
9. **Stateless runtime, stateful storage** — The agent process is long-lived but stateless. All persistent state lives in session transcripts and memory files.
10. **Observable** — Every decision, thought, tool call, and memory operation is emitted to a dedicated RabbitMQ observability queue in real time. All emitted data is sanitized of PII and secrets before publishing.
11. **Adaptive prompt compaction** — Agents can dynamically shorten their system prompt based on the capabilities and context window size of the model they use.

## Per-User Agent Queue

Each user gets exactly one RabbitMQ queue. All messages for that user — regardless of channel (WhatsApp, Slack, MQTT, cron) or session — land in this single queue.

```
Exchange: nipper.sessions (topic, durable)
  Routing key: nipper.sessions.{userId}.{sessionId}
  Binding: nipper.sessions.{userId}.# → queue: nipper-agent-{userId}
```

The agent consumes from `nipper-agent-{userId}` with `prefetch: 1`, processing messages one at a time. Each message contains a `sessionKey` that identifies the session context (transcript, metadata, memory). The agent loads the correct session state for each message.

```
┌──────────┐     ┌─────────────────┐     ┌────────────────────┐
│ Gateway   │────▶│ nipper.sessions │────▶│ nipper-agent-alice │──▶ Alice's Agent
│           │     │    exchange     │     └────────────────────┘
│           │     │                 │     ┌────────────────────┐
│           │     │                 │────▶│ nipper-agent-bob   │──▶ Bob's Agent
└──────────┘     └─────────────────┘     └────────────────────┘
```

**Why per-user queues instead of per-session queues?**

- Simpler topology — one queue per user, not one queue per conversation.
- Agent discovery is trivial — the agent just consumes from `nipper-agent-{userId}`.
- No queue lifecycle management — the queue is created once when the user is provisioned, not on every new session.
- `prefetch: 1` enforces serial processing, which is correct for 1–3 users.
- The `sessionKey` in each message provides all the context the agent needs to load the right session.

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

## Specialized Agents Per User

Different users can be served by completely different agent implementations. The Gateway does not know or care what runs behind the queue — it only sees the `NipperEvent` stream.

### How It Works

Routing is implicit. Each agent process consumes from a specific user's queue. No mapping table, no Gateway-side routing logic. You deploy Alice's agent pointed at `nipper-agent-alice`, Bob's agent pointed at `nipper-agent-bob`. Done.

```
┌────────────────────────────────────────────────────────────────────┐
│                    SPECIALIZED AGENT DEPLOYMENT                     │
│                                                                    │
│  Alice (Anthropic SDK)                                             │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │  Process: python alice_agent.py                              │  │
│  │  SDK: anthropic (Claude Sonnet)                              │  │
│  │  Queue: nipper-agent-alice                                   │  │
│  │  Features: extended thinking, tool use, RAG pipeline         │  │
│  └─────────────────────────────────────────────────────────────┘  │
│                                                                    │
│  Bob (LangGraph + LocalAI)                                         │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │  Process: python bob_agent.py                                │  │
│  │  SDK: langgraph + ollama (llama3.1:70b)                      │  │
│  │  Queue: nipper-agent-bob                                     │  │
│  │  Features: custom graph, local inference, no cloud deps      │  │
│  └─────────────────────────────────────────────────────────────┘  │
│                                                                    │
│  Carol (OpenAI SDK)                                                │
│  ┌─────────────────────────────────────────────────────────────┐  │
│  │  Process: node carol_agent.js                                │  │
│  │  SDK: openai (GPT-4o)                                        │  │
│  │  Queue: nipper-agent-carol                                   │  │
│  │  Features: function calling, structured output, web search   │  │
│  └─────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘
```

### Example: Alice's Agent (Anthropic SDK)

```python
import pika, json, os, requests, anthropic

# --- Auto-registration: get RabbitMQ config from Gateway ---
gateway_url = os.environ["NIPPER_GATEWAY_URL"]
auth_token = os.environ["NIPPER_AUTH_TOKEN"]

reg = requests.post(
    f"{gateway_url}/agents/register",
    headers={"Authorization": f"Bearer {auth_token}"},
    json={"agent_type": "anthropic-sdk", "version": "1.0.0"}
).json()["result"]
rmq = reg["rabbitmq"]

# --- Connect to RabbitMQ with received credentials ---
client = anthropic.Anthropic()
connection = pika.BlockingConnection(pika.ConnectionParameters(
    host=rmq["url"].split("://")[1].split(":")[0],
    port=int(rmq["url"].split(":")[-1]),
    credentials=pika.PlainCredentials(rmq["username"], rmq["password"]),
    virtual_host=rmq["vhost"]
))
channel = connection.channel()
channel.basic_qos(prefetch_count=1)

def on_message(ch, method, properties, body):
    queue_item = json.loads(body)
    nipper_msg = queue_item["message"]
    session_key = nipper_msg["sessionKey"]

    transcript = load_transcript(session_key)

    response = client.messages.create(
        model="claude-sonnet-4-20250514",
        max_tokens=8096,
        system=build_system_prompt(session_key),
        messages=transcript + [{"role": "user", "content": nipper_msg["content"]["text"]}]
    )

    event = {
        "type": "done",
        "sessionKey": session_key,
        "responseId": generate_uuid(),
        "timestamp": now_iso(),
        "delta": {"text": response.content[0].text},
        "contextUsage": compute_usage(response)
    }
    ch.basic_publish(
        exchange=rmq["exchanges"]["events"],
        routing_key=f"nipper.events.{nipper_msg['userId']}.{nipper_msg['sessionKey']}",
        body=json.dumps(event)
    )
    ch.basic_ack(delivery_tag=method.delivery_tag)

channel.basic_consume(queue=rmq["queues"]["agent"], on_message_callback=on_message)
channel.start_consuming()
```

### Example: Bob's Agent (LangGraph + LocalAI)

```python
import pika, json, os, requests
from langgraph.graph import StateGraph
from langchain_openai import ChatOpenAI

# --- Auto-registration ---
gateway_url = os.environ["NIPPER_GATEWAY_URL"]
auth_token = os.environ["NIPPER_AUTH_TOKEN"]

reg = requests.post(
    f"{gateway_url}/agents/register",
    headers={"Authorization": f"Bearer {auth_token}"},
    json={"agent_type": "langgraph", "version": "1.0.0"}
).json()["result"]
rmq = reg["rabbitmq"]

llm = ChatOpenAI(
    base_url="http://localhost:11434/v1",  # Ollama
    model="llama3.1:70b",
    api_key="ollama"
)

graph = StateGraph(AgentState)
graph.add_node("reason", reason_node)
graph.add_node("tool_exec", tool_exec_node)
graph.add_edge("reason", "tool_exec")
graph.add_edge("tool_exec", "reason")
agent = graph.compile()

connection = pika.BlockingConnection(pika.ConnectionParameters(
    host=rmq["url"].split("://")[1].split(":")[0],
    port=int(rmq["url"].split(":")[-1]),
    credentials=pika.PlainCredentials(rmq["username"], rmq["password"]),
    virtual_host=rmq["vhost"]
))
channel = connection.channel()
channel.basic_qos(prefetch_count=1)

def on_message(ch, method, properties, body):
    queue_item = json.loads(body)
    nipper_msg = queue_item["message"]
    session_key = nipper_msg["sessionKey"]

    result = agent.invoke({
        "messages": load_transcript(session_key) + [{"role": "user", "content": nipper_msg["content"]["text"]}]
    })

    event = {
        "type": "done",
        "sessionKey": session_key,
        "responseId": generate_uuid(),
        "timestamp": now_iso(),
        "delta": {"text": result["messages"][-1].content}
    }
    ch.basic_publish(
        exchange=rmq["exchanges"]["events"],
        routing_key=f"nipper.events.{nipper_msg['userId']}.{nipper_msg['sessionKey']}",
        body=json.dumps(event)
    )
    ch.basic_ack(delivery_tag=method.delivery_tag)

channel.basic_consume(queue=rmq["queues"]["agent"], on_message_callback=on_message)
channel.start_consuming()
```

### Example: Go Agent (OpenAI SDK)

```go
// Auto-register with Gateway
regResp := registerAgent(os.Getenv("NIPPER_GATEWAY_URL"), os.Getenv("NIPPER_AUTH_TOKEN"))
rmq := regResp.RabbitMQ

connStr := fmt.Sprintf("amqps://%s:%s@%s/%s", rmq.Username, rmq.Password, rmq.TlsURL, rmq.Vhost)
conn, _ := amqp.DialTLS(connStr, tlsConfig)
ch, _ := conn.Channel()
ch.Qos(1, 0, false)
msgs, _ := ch.Consume(rmq.Queues.Agent, "", false, false, false, false, nil)

for msg := range msgs {
    var item QueueItem
    json.Unmarshal(msg.Body, &item)

    response := callOpenAI(item.Message.Content.Text)

    event, _ := json.Marshal(NipperEvent{Type: "done", SessionKey: item.Message.SessionKey, ...})
    ch.Publish(rmq.Exchanges.Events, routingKey, false, false, amqp.Publishing{Body: event})
    msg.Ack(false)
}
```

### SDK Compatibility Matrix

Agents are free to use any AI SDK. The Gateway never sees which SDK or model the agent uses — it only sees the `NipperEvent` stream.

| SDK / Framework | Languages | Notes |
|----------------|-----------|-------|
| **Anthropic SDK** | Python, TypeScript, Go, Java | Native Claude support, extended thinking |
| **OpenAI SDK** | Python, TypeScript, Go, .NET | GPT-4, o1, o3, any OpenAI-compatible endpoint |
| **LangChain** | Python, TypeScript | Chain-of-thought, RAG, tool orchestration |
| **LangGraph** | Python, TypeScript | Stateful agent graphs, custom control flow |
| **LiteLLM** | Python | Unified API across 100+ model providers |
| **Vercel AI SDK** | TypeScript | Streaming-first, React/Next.js integration |
| **Ollama** | Any (HTTP API) | Local models, no cloud dependency |
| **vLLM / TGI** | Any (HTTP API) | Self-hosted inference servers |
| **AWS Bedrock** | Python, Go, Java | Managed models (Claude, Llama, etc.) |
| **Direct HTTP** | Any | Raw API calls to any provider |

## Agent Lifecycle

The agent lifecycle is owned entirely by the agent process. The Gateway has no role in starting, stopping, or restarting agents. However, the Gateway provides a **provisioning and auto-registration** mechanism that eliminates manual RabbitMQ configuration — see `GATEWAY_ARCHITECTURE.md`.

```
┌─────────────┐
│   STOPPED   │  No agent process running
└──────┬──────┘
       │ Operator starts agent with NIPPER_GATEWAY_URL + NIPPER_AUTH_TOKEN
       ▼
┌─────────────┐
│ REGISTERING │  POST /agents/register → receive RabbitMQ config
└──────┬──────┘
       │ RabbitMQ credentials, queue names, exchanges received
       ▼
┌─────────────┐
│ CONNECTING  │  Agent connects to RabbitMQ with received credentials
└──────┬──────┘
       │ Connected, consuming from nipper-agent-{userId}
       ▼
┌─────────────┐
│  CONSUMING  │  Waiting for messages
└──────┬──────┘
       │ Message received
       ▼
┌─────────────┐
│ PROCESSING  │  AI inference + tool execution
└──────┬──────┘
       │ Response complete, ack sent
       ▼
┌─────────────┐
│  CONSUMING  │  Wait for next message
└─────────────┘
```

**Key behaviors:**

- Agent processes are **long-lived**. They start, auto-register to get RabbitMQ config, connect to RabbitMQ, and consume messages in a loop. They stay running until the operator stops them.
- On startup, the agent calls `POST /agents/register` on the Gateway with its auth token. The Gateway validates the token, creates/rotates RabbitMQ credentials, and returns the full configuration blob (see `GATEWAY_ARCHITECTURE.md`, Agent Auto-Registration Endpoint). The agent only needs two environment variables: `NIPPER_GATEWAY_URL` and `NIPPER_AUTH_TOKEN`.
- If the agent process crashes, RabbitMQ redelivers the unacknowledged message when the agent reconnects. No message loss. On restart, the agent re-registers (idempotent) and gets fresh credentials.
- If no agent is consuming from a user's queue, messages accumulate in RabbitMQ (durable queue). When the agent starts, it drains the backlog.
- The Gateway monitors consumer count via the RabbitMQ Management API. If a user's queue has zero consumers, the Gateway can surface a health warning — but it does not attempt to start an agent.
- **RabbitMQ connection loss:** The reference Go agent runs a reconnect loop: when the consume loop exits (e.g. delivery channel closed), it closes the connection, waits a short backoff, re-registers with the Gateway (to obtain fresh credentials if rotated), reconnects to RabbitMQ, and resumes consuming. No manual restart is required.

### Health Monitoring

```yaml
agents:
  health_check_interval_seconds: 30
  consumer_timeout_seconds: 60     # If no consumer on a user's queue, mark degraded
```

The Gateway periodically checks consumer count on each `nipper-agent-{userId}` queue. If a queue has zero consumers for longer than `consumer_timeout_seconds`, the Gateway logs a warning and marks the user's agent status as `degraded`. Messages continue to queue (up to `x-max-length`) — they are not lost.

## Agent Deployment

An agent is any process that speaks AMQP and conforms to the `NipperMessage`/`NipperEvent` protocol. The Gateway is uninvolved in how agents are deployed, started, or managed. Agents are **provisioned** via the Gateway admin API, which issues an auth token. The agent uses this token to **auto-register** at startup and receive its RabbitMQ configuration (see `GATEWAY_ARCHITECTURE.md`).

| Deployment Method | Example | Best For |
|-------------------|---------|----------|
| **systemd service** | `systemctl start nipper-agent-alice` | Single-machine, production |
| **Docker container** | `docker run -e NIPPER_AUTH_TOKEN=npr_... agent-alice` | Portable, reproducible |
| **Kubernetes pod** | Deployment with `NIPPER_AUTH_TOKEN` secret | Multi-machine, auto-scaling |
| **Bare process** | `NIPPER_AUTH_TOKEN=npr_... python alice_agent.py` | Development, testing |
| **Cloud function** | AWS Lambda, GCP Cloud Run with env vars | Serverless, pay-per-invocation |

In all cases, the agent must:
1. Call `POST /agents/register` on the Gateway with its auth token to receive RabbitMQ config
2. Connect to the RabbitMQ broker using the received credentials (AMQP or AMQPS)
3. Consume from `nipper-agent-{userId}` with `prefetch: 1`
4. Publish `NipperEvent` messages to the `nipper.events` exchange
5. Consume control messages from `nipper-control-{userId}`
6. Conform to the `NipperMessage`/`NipperEvent` JSON schema

**Required environment variables:**

| Variable | Description |
|----------|-------------|
| `NIPPER_GATEWAY_URL` | Gateway base URL (e.g., `http://gateway:18789`) |
| `NIPPER_AUTH_TOKEN` | Agent auth token issued during provisioning (`npr_...`) |

All other configuration (RabbitMQ URL, credentials, queue names, exchange names, vhost, user info, policies) is received dynamically from the registration endpoint.

### Network Requirements

The agent needs network access to two things: the **Gateway** (HTTP, for auto-registration) and the **RabbitMQ broker** (AMQP, for message consumption). The Gateway and the agent do **not** need to be on the same host, subnet, or continent.

```
┌──────────────────┐        ┌──────────────────┐        ┌──────────────────┐
│     Gateway      │        │    RabbitMQ      │        │  Alice's Agent   │
│   (Host A)       │──AMQP──│   Broker         │──AMQP──│  (Host B)        │
│                  │        │   (TLS + auth)   │        │  Anthropic SDK   │
│  /agents/register│◀─HTTP──│                  │        │                  │
└──────────────────┘        └──────────────────┘        └──────────────────┘
                                    │
                                    │──AMQP──┌──────────────────┐
                                    │        │  Bob's Agent     │
                                    │        │  (Host C / K8s)  │
                                    │        │  LangGraph       │
                                    │        └──────────────────┘
```

For production deployments:
- **TLS** for encrypted transport (AMQPS on port 5671)
- **Per-agent credentials** provisioned automatically via the auto-registration endpoint (vhost-scoped, per-user permissions — see `GATEWAY_ARCHITECTURE.md`)
- **HTTPS** for the registration endpoint if agents connect over untrusted networks
- **Firewall rules** allowing only AMQP from known agent hosts or VPN ranges

## Multi-Language Agent Protocol

Agents can be written in **any programming language**. The contract is the AMQP message protocol, not a language-specific API.

### Required Capabilities

| Capability | Protocol | Details |
|-----------|----------|---------|
| **Consume messages** | AMQP `basic.consume` | From `nipper-agent-{userId}` with `prefetch: 1` |
| **Publish events** | AMQP `basic.publish` | To `nipper.events` exchange with routing key `nipper.events.{userId}.{sessionId}` |
| **Consume control messages** | AMQP `basic.consume` | From `nipper-control-{userId}` |
| **Ack/nack messages** | AMQP `basic.ack`/`basic.nack` | After processing or on failure |
| **Parse NipperMessage** | JSON | Deserialize the `QueueItem` body |
| **Emit NipperEvent** | JSON | Serialize events: `delta`, `tool_start`, `tool_end`, `thinking`, `done`, `error` |

## AI SDK Configuration (Eino)

The reference Go agent uses Eino model components. Model provider configuration lives in the agent config file:

### OpenAI / OpenAI-Compatible (LocalAI, vLLM, Azure)

```yaml
agent:
  inference:
    provider: "openai"
    model: "gpt-4o"
    api_key: "${OPENAI_API_KEY}"
    base_url: ""                    # empty = api.openai.com
    temperature: 0.7
    max_tokens: 4096
```

For LocalAI or vLLM with OpenAI-compatible API:

```yaml
agent:
  inference:
    provider: "openai"
    model: "mistral-7b"
    api_key: "not-needed"
    base_url: "http://localhost:8080/v1"
```

### Ollama (Local Models)

```yaml
agent:
  inference:
    provider: "ollama"
    model: "llama3.1:70b"
    base_url: "http://localhost:11434"
    temperature: 0.7
```

### Eino Model Factory

The agent creates the ChatModel via a factory function:

```go
import (
    einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
    einoollama "github.com/cloudwego/eino-ext/components/model/ollama"
)

func NewChatModel(ctx context.Context, cfg InferenceConfig) (model.ChatModel, error) {
    switch cfg.Provider {
    case "openai":
        return einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
            Model:       cfg.Model,
            APIKey:      cfg.APIKey,
            BaseURL:     cfg.BaseURL,
            Temperature: &cfg.Temperature,
            MaxTokens:   &cfg.MaxTokens,
        })
    case "ollama":
        return einoollama.NewChatModel(ctx, &einoollama.ChatModelConfig{
            BaseURL: cfg.BaseURL,
            Model:   cfg.Model,
        })
    }
}
```

## Agent Runtime

### The Agent Loop (Eino ReAct)

The agent loop uses Eino's `react.NewAgent` which implements the ReAct (Reason + Act) pattern:

```
┌──────────────────────────────────────────────────────────────┐
│                   AGENT RUNTIME LOOP (Eino)                  │
│                                                              │
│  1. Consume message from RabbitMQ queue    ◀── LOG: message_received
│  2. Load session transcript (by sessionKey)◀── LOG: session_loaded
│  3. Build system prompt + Eino messages    ◀── LOG: prompt_built
│  4. Check context window → compact?        ◀── LOG: context_check / compaction
│  5. ┌─── EINO REACT AGENT ─────────────────────────────  ─┐   │
│     │                                                     │   │
│     │   react.NewAgent(ctx, &react.AgentConfig{           │   │
│     │       ToolCallingModel: chatModel,                  │   │
│     │       ToolsConfig: toolsNodeConfig,                 │   │
│     │       MaxStep: 25,                                  │   │
│     │   })                                                │   │
│     │                                                     │   │
│     │   ChatModel decides:                                │   │
│     │     ├─ tool_call → Eino executes tool automatically │   │
│     │     │              ◀── LOG: tool_call / tool_result │   │
│     │     │              → loop back to ChatModel         │   │
│     │     └─ text response → done                         │   │
│     │                        ◀── LOG: response_generated  │   │
│     │                                                     │   │
│     └─────────────────────────────────────────────────────┘   │
│  6. Append response + tool calls to transcript                │
│  7. Update session metadata               ◀── LOG: session_updated
│  8. Publish NipperEvent(s) to nipper.events exchange          │
│  9. Ack the RabbitMQ message (basic.ack)                      │
│  10. Report contextUsage                  ◀── LOG: run_complete
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

The Eino ReAct agent handles the inner loop (ChatModel → Tools → ChatModel) automatically. The outer loop (consume → process → ack) is agent code.

### System Prompt Construction

The agent builds a system prompt containing:

```
1. Base persona (config or /persona override)
2. Durable memory injection (last N days of saved notes)
3. Agent commands reference (/help, /new, /usage, /compact, /status, /persona)
4. Available tools with descriptions and usage hints
5. Per-tool security directives (bash, doc_fetch, web_search)
6. Channel-specific formatting rules (Markdown, WhatsApp, plaintext)
7. Global safety preamble (8 mandatory security rules — defense in depth)
```

### Adaptive System Prompt Compaction

Different models have vastly different context window sizes and reasoning capabilities. A 200K-token model like Claude Sonnet can absorb a rich, detailed system prompt. A 4K-token local model like Llama 3.2 3B cannot. Agents dynamically adjust the system prompt based on the model they are configured to use.

#### Compaction Levels

| Level | Target Budget | Strategy | Suitable For |
|-------|-------------|----------|-------------|
| `full` | 8,000–12,000 tokens | Include everything: all tools, all skills, full bootstrap, full memory, verbose instructions | Large-context models (Claude Sonnet/Opus, GPT-4o, 128K+ context) |
| `standard` | 4,000–6,000 tokens | Include core tools, active skills only, truncated bootstrap, recent memory (3 days), concise instructions | Mid-size models (Haiku, GPT-4o-mini, 32K–128K context) |
| `compact` | 1,500–3,000 tokens | Core tools only, top 3 skills by relevance, no bootstrap, no memory injection, minimal instructions | Small models (Llama 3.2 3B, Phi-3, 4K–16K context) |
| `minimal` | 500–1,000 tokens | Essential tools only (read, write, exec), no skills, no memory, bare-bones instructions | Tiny models, embedding-only endpoints, or function-calling-only use |

#### Compaction Configuration

```yaml
agent:
  prompt:
    compaction_level: "auto"       # "auto" | "full" | "standard" | "compact" | "minimal"

    auto_thresholds:
      full:     128000             # Models with >= 128K context window
      standard: 32000              # Models with >= 32K context window
      compact:  8000               # Models with >= 8K context window
      minimal:  0                  # Everything below 8K

    overrides:
      "claude-sonnet-4-20250514": "full"
      "claude-3-5-haiku-20241022": "standard"
      "gpt-4o-mini": "standard"
      "llama3.2:3b": "compact"
      "phi-3-mini": "compact"
```

When `compaction_level` is `"auto"`, the agent inspects the model's context window limit and selects the appropriate level. Explicit overrides per model name take precedence.

#### What Gets Compacted

| Component | full | standard | compact | minimal |
|-----------|------|----------|---------|---------|
| Runtime info | Full | Abbreviated (OS, time only) | One line | Omitted |
| Tool schemas | All tools, full JSON | All tools, abbreviated | Core tools only (read, write, exec, memory_write) | Minimal (read, exec) |
| Skill descriptions | All skills, full SKILL.md | Active skills, first paragraph only | Top 3 by keyword match, title + one-liner | Omitted |
| Bootstrap context | Full bootstrap.md | First 1,000 tokens | Omitted | Omitted |
| Memory injection | Last 7 days | Last 3 days, 500 token cap | Omitted | Omitted |
| Channel capabilities | Full | Abbreviated | Omitted | Omitted |
| Instruction text | Verbose with examples | Concise, no examples | Bullet points only | Single sentence |
| Security preamble | Full input tagging + hierarchy | Abbreviated | Minimal | Omitted |

#### Prompt Builder Interface

```typescript
interface PromptBuilder {
  build(options: {
    compactionLevel: "full" | "standard" | "compact" | "minimal";
    tools: ToolDefinition[];
    skills: SkillDefinition[];
    bootstrap?: string;
    memory?: string;
    channelCapabilities?: ChannelCapabilities;
    contextWindowLimit: number;
  }): string;

  estimateTokens(prompt: string): number;

  selectCompactionLevel(contextWindowLimit: number, config: PromptConfig): CompactionLevel;
}
```

The prompt builder runs **inside the agent**. Each agent implementation is responsible for building its own prompt at the appropriate compaction level.

### Tool Execution (Eino Tools)

All tools implement Eino's `tool.BaseTool` interface, registered via `utils.InferTool()` for automatic JSON schema generation:

| Tool            | Description                              | Sandbox | Stage |
|-----------------|------------------------------------------|---------|-------|
| `web_fetch`     | Fetch and parse web pages                | No      | 3     |
| `bash`          | Execute shell commands (Docker sandbox)  | Yes     | 4     |
| `web_search`    | Search DuckDuckGo or Google              | No      | 5     |
| `doc_fetch`     | Fetch documents (HTTP, S3/Minio) + EXIF  | No      | 6     |
| `memory_write`  | Write to durable memory                  | No      | 9     |
| `memory_read`   | Read from durable memory                 | No      | 9     |
| `get_weather`   | Environment Canada weather / forecast    | No      | 7     |
| `cron_list_jobs`           | List user's scheduled cron jobs (prompts) | No   | 7     |
| `cron_list_notify_channels`| List user's notify channels (channelType:channelId) for cron notify_channels | No | 7 |
| `cron_add_job`             | Add a scheduled cron job (prompt only)   | No   | 7     |
| `cron_remove_job`          | Remove a cron job by id                  | No   | 7     |
| MCP tools       | Loaded from external MCP servers         | Varies  | 8     |

Tools are registered with the Eino agent at startup:

```go
tools, _ := tools.BuildTools(ctx, cfg)
mcpTools := mcpLoader.Tools()
allTools := append(tools, mcpTools...)

agent, _ := react.NewAgent(ctx, &react.AgentConfig{
    ToolCallingModel: chatModel,
    ToolsConfig: compose.ToolsNodeConfig{
        Tools: allTools,
    },
    MaxStep: cfg.MaxSteps,
})
```

Every tool call goes through a **policy check** before execution (see `SECURITY.md`):

```go
type ToolPolicy struct {
    Allow []string `yaml:"allow"` // Glob patterns
    Deny  []string `yaml:"deny"`
}
```

The policy is enforced in code, not by the AI. The Eino agent generates tool calls; the tool wrapper checks policy before executing.

## Secrets Access (Environment Variables)

Agents resolve secrets from **environment variables** at startup. Config fields using `${VAR}` syntax are expanded from the process environment. This is the default and recommended approach — no external secret manager dependency.

### Flow

```
Agent config declares:
  inference:
    api_key: "${OPENAI_API_KEY}"
  s3:
    access_key: "${MINIO_ACCESS_KEY}"
    secret_key: "${MINIO_SECRET_KEY}"
        │
        ▼
Config loader expands ${VAR} from os.Getenv():
  OPENAI_API_KEY=sk-... → inference.api_key = "sk-..."
        │
        ▼
Resolved values held in memory, never written to disk
        │
        ▼
Passed to LLM client, tool configs, sandbox env
```

### Secret Configuration

```yaml
agent:
  inference:
    api_key: "${OPENAI_API_KEY}"

  secrets:
    env_map:
      github_token: "GITHUB_TOKEN"
      deploy_ssh_key: "DEPLOY_SSH_KEY"
      minio_access: "MINIO_ACCESS_KEY"
      minio_secret: "MINIO_SECRET_KEY"
```

The `env_map` provides named references that tools can use. The resolver validates all required env vars are set at startup and logs which names were resolved (never values).

### Secret Scoping

Per-user scoping is achieved through deployment: each user's agent runs as a separate process with its own environment. Alice's agent process has `OPENAI_API_KEY=sk-alice-...`, Bob's has `OPENAI_API_KEY=sk-bob-...`. The process boundary is the isolation mechanism.

### 1Password (Optional)

Operators who prefer 1Password can mount the `op` CLI in the agent container and use it to populate env vars at process startup (e.g., `eval $(op signin) && op run -- nipper agent`). The agent itself does not call `op` directly — it reads the already-resolved env vars.

## Bidirectional Communication

### Agent → User (Response Channel)

Every `NipperMessage` carries a `deliveryContext` that tells the system how to route responses. The agent publishes events to RabbitMQ; the Gateway consumes them and delivers to the appropriate channel.

```
Agent generates response
        │
        ▼
Agent publishes NipperEvent to RabbitMQ:
  Exchange: nipper.events
  Routing key: nipper.events.{userId}.{sessionId}
  Body: { type: "delta", text: "Deploying...", sessionKey: "..." }
        │
        ▼
Gateway consumes from nipper-events-gateway queue
        │
        ▼
Gateway looks up deliveryContext for this session
        │
        ▼
Gateway routes to correct channel adapter:
  ├── WhatsApp → Send via Wuzapi API (buffered, no streaming)
  ├── Slack → Update Slack message (streaming)
  ├── MQTT → Publish to outbox topic
  ├── RabbitMQ (channel) → Publish to outbound exchange
  └── Cron → Route to notifyChannels
```

### Agent → Other Channels (Cross-Channel Messaging)

Agents can send messages to channels other than the originating one using the `message` tool:

```json
{
  "tool": "message",
  "params": {
    "target": "slack:C0789GHI",
    "text": "Deployment to staging completed successfully. Version: v2.3.1"
  }
}
```

The `message` tool publishes to the `nipper.events` exchange with a special `x-nipper-event-type: cross_channel` header, and the Gateway routes it to the target channel.

### Headless Events (Cron / Automated)

Cron jobs and automated events have no originating user channel to respond to. The agent handles this with the **notification channel** pattern:

```
Cron job fires → NipperMessage with channelType: "cron"
        │
        ▼
DeliveryContext includes:
  replyMode: "broadcast"
  notifyChannels: ["slack:C0789GHI", "whatsapp:5491155553935@s.whatsapp.net"]
        │
        ▼
Agent processes, generates response
        │
        ▼
Agent publishes response as NipperEvent to nipper.events exchange
        │
        ▼
Gateway sees replyMode: "broadcast"
        │
        ▼
Gateway delivers to ALL notifyChannels:
  ├── slack:C0789GHI → Post in Slack channel
  └── whatsapp:5491155553935@s.whatsapp.net → Send via Wuzapi
```

### Notification Strategies

| Strategy    | Behavior                                                       |
|-------------|----------------------------------------------------------------|
| `broadcast` | Send to all `notifyChannels`                                   |
| `dm`        | Send as DM to the session's user on their preferred channel    |
| `silent`    | Log only, no delivery (for background jobs that don't need notification) |
| `escalate`  | Send to notifyChannels only if the result contains errors/warnings |

## Agent-to-Agent Communication

Agents can spawn sub-agents using the `session_spawn` tool:

```json
{
  "tool": "session_spawn",
  "params": {
    "task": "Research the latest Kubernetes CVEs and summarize",
    "model": "fast",
    "timeout": 120
  }
}
```

This creates a child session within the same user's scope. The child message is published to the same user's agent queue (`nipper-agent-{userId}`). The parent agent waits for completion by subscribing to the child session's events on the `nipper.events` exchange.

The child session:
- Gets its own session transcript
- Has access to the parent's workspace (read-only)
- Reports results back via RabbitMQ (`nipper.events` exchange)
- Inherits the parent's tool policy (cannot escalate permissions)
- Has a depth limit (default: 3) to prevent infinite recursion

## Model Resolution

Each agent instance is configured with a single provider and model. Different users can use different providers by running separate agent processes with different configs.

```yaml
# Alice's agent config — OpenAI
agent:
  inference:
    provider: "openai"
    model: "gpt-4o"
    api_key: "${OPENAI_API_KEY}"

# Bob's agent config — Local Ollama
agent:
  inference:
    provider: "ollama"
    model: "llama3.1:70b"
    base_url: "http://localhost:11434"
```

### Error Handling

```
LLM call fails
        │
        ▼
Transient error (rate limit, timeout)?
  ├── YES → Retry with exponential backoff (max 3 attempts)
  └── NO → Return error event to user via NipperEvent{Type: "error"}
```

## Agent Observability Logger

### Purpose

The observability logger gives you a real-time window into every agent's decision-making process. Every thought, tool call, memory operation, context management decision, and error is emitted as a structured event to a dedicated RabbitMQ queue.

**All data is sanitized of PII, passwords, and sensitive content before it leaves the agent process.** The sanitizer runs inside the agent, before publishing to RabbitMQ.

### Architecture

```
Agent Process
        │
        │ Raw log events (may contain sensitive data)
        ▼
┌─────────────────────────┐
│    Sanitization Pipeline │
│                          │
│  1. Secret scrubbing     │  Remove any resolved 1Password values
│  2. PII redaction        │  Names, emails, phone numbers, IPs
│  3. Credential patterns  │  API keys, tokens, passwords, JWTs
│  4. User-defined rules   │  Custom regex patterns from config
│  5. Content truncation   │  Cap large payloads (tool output, etc.)
│                          │
└────────────┬────────────┘
             │ Clean, safe log events
             ▼
┌─────────────────────────┐
│   RabbitMQ              │
│                          │
│   Exchange: nipper.logs  │  (topic exchange)
│                          │
│   Routing keys:          │
│     nipper.logs.{userId}.{eventType}
│                          │
│   Queues:                │
│     nipper-logs-all         ← binds to nipper.logs.#
│     nipper-logs-user-01     ← binds to nipper.logs.user-01.#
│     nipper-logs-errors      ← binds to nipper.logs.*.error
│     nipper-logs-thinking    ← binds to nipper.logs.*.thinking
│                          │
└─────────────────────────┘
             │
             ▼
   Consumer (dashboard, CLI tail, Grafana, ELK, etc.)
```

### Log Event Types

| Event Type          | Emitted When                                           | Contains                                             |
|---------------------|--------------------------------------------------------|------------------------------------------------------|
| `message_received`  | Agent pulls a message from the queue                   | Sanitized message text, channel type, session key    |
| `session_loaded`    | Session transcript is read from disk                   | Session ID, message count, token count               |
| `prompt_built`      | System prompt is assembled                             | Skill names loaded, bootstrap size, memory injected  |
| `context_check`     | Context window usage evaluated                         | Token counts, usage %, threshold status              |
| `model_call`        | AI model API call is made                              | Provider, model, input token estimate, attempt #     |
| `thinking`          | Model emits reasoning/thinking blocks                  | Sanitized thinking text                              |
| `tool_call`         | Model decides to invoke a tool                         | Tool name, sanitized parameters                      |
| `tool_result`       | Tool execution completes                               | Tool name, exit code, sanitized truncated output     |
| `response_generated`| Model produces a final text response                   | Sanitized response excerpt, token counts             |
| `memory_write`      | Agent writes to durable memory                         | Memory file, sanitized content excerpt               |
| `memory_read`       | Agent reads from durable memory                        | Memory file, query (if search)                       |
| `compaction`        | Context compaction is triggered                        | Pass level, messages removed, tokens freed           |
| `key_rotation`      | API key rotated due to rate limit                      | Provider, reason, cooldown duration                  |
| `error`             | Any error during agent execution                       | Error type, sanitized message, recoverable flag      |
| `session_updated`   | Session metadata written after run                     | Updated token counts, compaction count               |
| `run_complete`      | Full agent run finishes (success or failure)           | Duration, total tool calls, final context usage      |
| `secret_accessed`   | Agent resolves a secret from 1Password                 | Reference path (e.g. `op://vault/deploy/ssh-key`), NOT the value |
| `plugin_executed`   | A plugin script runs                                   | Plugin name, sanitized parameters, duration          |

### Log Event Schema

```typescript
interface AgentLogEvent {
  eventId: string;               // UUIDv7
  timestamp: string;             // ISO 8601, nanosecond precision
  eventType: AgentLogEventType;

  userId: string;
  sessionKey: string;
  sessionId: string;
  runId: string;                 // Groups all events from a single agent run

  payload: Record<string, unknown>;

  agentType?: string;            // "anthropic-sdk", "langgraph", "openai-sdk", etc.
  model?: string;
  provider?: string;
  attemptNumber?: number;
  durationMs?: number;
  parentEventId?: string;        // Links tool_result back to tool_call, etc.
}
```

### Sanitization Pipeline

The sanitizer runs **inside the agent process**, before publishing to RabbitMQ. Every log event passes through all 5 sanitization stages. There is no code path that bypasses the sanitizer.

```typescript
interface LogSanitizer {
  sanitize(event: AgentLogEvent): AgentLogEvent;
}
```

#### Stage 1: Secret Scrubbing

Every secret value resolved from 1Password during the current agent run is tracked in memory. The scrubber replaces any occurrence of these values with `[REDACTED_SECRET]`.

```typescript
function scrubSecrets(text: string, resolvedSecrets: string[]): string {
  for (const secret of resolvedSecrets) {
    if (secret.length < 8) continue;
    text = text.replaceAll(secret, "[REDACTED_SECRET]");
  }
  return text;
}
```

#### Stage 2: PII Redaction

Pattern-based redaction of personally identifiable information:

| Pattern                         | Replacement             |
|---------------------------------|-------------------------|
| Email addresses                 | `[REDACTED_EMAIL]`      |
| Phone numbers                   | `[REDACTED_PHONE]`      |
| IP addresses (v4 and v6)        | `[REDACTED_IP]`         |
| Credit card numbers             | `[REDACTED_CC]`         |
| Social security / national IDs  | `[REDACTED_ID]`         |
| WhatsApp JIDs                   | `[REDACTED_JID]`        |
| Names (from datastore)          | `[REDACTED_NAME]`       |

#### Stage 3: Credential Pattern Detection

Catches credentials that weren't in the 1Password resolver:

| Pattern                         | Replacement             |
|---------------------------------|-------------------------|
| `Bearer ...`                    | `Bearer [REDACTED]`     |
| `token=...`, `apikey=...`       | `token=[REDACTED]`      |
| `-----BEGIN ... KEY-----`       | `[REDACTED_KEY_BLOCK]`  |
| `eyJ...` (base64 JWT)          | `[REDACTED_JWT]`        |
| `AKIA...` (AWS key prefix)     | `[REDACTED_AWS_KEY]`    |
| `sk-...`, `pk-...`             | `[REDACTED_API_KEY]`    |
| `ghp_...`, `gho_...`           | `[REDACTED_GH_TOKEN]`   |
| `xox[bpas]-...`                | `[REDACTED_SLACK_TOKEN]`|
| `password=...`, `passwd=...`   | `password=[REDACTED]`   |

#### Stage 4: User-Defined Rules

Operators can add custom redaction rules:

```yaml
observability:
  sanitizer:
    customRules:
      - pattern: "INTERNAL-PROJECT-\\w+"
        replacement: "[REDACTED_PROJECT_ID]"
      - pattern: "customer_id=\\d+"
        replacement: "customer_id=[REDACTED]"
```

#### Stage 5: Content Truncation

Large payloads are capped:

| Field                  | Max Size | Truncation Strategy                       |
|------------------------|----------|-------------------------------------------|
| `thinking.text`        | 4 KB     | Keep first 2 KB + last 2 KB, `[...]` mid |
| `tool_result.output`   | 8 KB     | Keep first 5 KB + last 3 KB              |
| `response.text`        | 4 KB     | Keep first 2 KB + last 2 KB              |
| `memory.content`       | 2 KB     | Keep first 2 KB                           |
| `message.text`         | 2 KB     | Keep first 2 KB                           |

### RabbitMQ Topology

```yaml
observability:
  enabled: true
  rabbitmq:
    exchange: "nipper.logs"
    exchangeType: "topic"
    durable: true
    routingKeyFormat: "nipper.logs.{userId}.{eventType}"

    queues:
      - name: "nipper-logs-all"
        bindingKey: "nipper.logs.#"
        durable: true
        ttl: 86400000                # 24h TTL
        maxLength: 100000

      - name: "nipper-logs-errors"
        bindingKey: "nipper.logs.*.error"
        durable: true
        ttl: 604800000               # 7 day TTL

      - name: "nipper-logs-thinking"
        bindingKey: "nipper.logs.*.thinking"
        durable: false
        ttl: 3600000                 # 1h TTL

    publisherConfirms: false         # Fire-and-forget
    mandatory: false                 # Don't fail agent runs if logging fails
```

### Logger Guarantees

| Property                   | Guarantee                                                |
|----------------------------|----------------------------------------------------------|
| **Never blocks the agent** | Publishing is fire-and-forget. If RabbitMQ is down, events are dropped silently. |
| **Always sanitized**       | Every event passes through all 5 sanitization stages. |
| **No secrets in transit**  | Resolved 1Password values are scrubbed from all text fields before publishing. |
| **No PII leakage**         | PII patterns are redacted. Known user names are replaced. WhatsApp JIDs are redacted. |
| **Ephemeral by default**   | Log queues have TTLs and max-length caps. |
| **Per-user filterable**    | Routing key includes userId for targeted subscriptions. |

### Logger vs Audit Log

| Aspect         | Observability Logger                  | Audit Log                             |
|----------------|---------------------------------------|---------------------------------------|
| **Purpose**    | Real-time debugging, understanding agent reasoning | Compliance, forensics, incident investigation |
| **Transport**  | RabbitMQ (streaming)                  | JSONL files on disk                   |
| **Retention**  | Hours to days (TTL-based)             | Permanent (rotated daily)             |
| **Content**    | Sanitized, truncated, safe to view    | Minimal metadata, no content          |
| **Audience**   | Operators watching agent behavior     | Security team, auditors               |
| **Failure mode**| Drop events silently                 | Buffer in memory, retry               |

### Consuming the Logger

**CLI tail (real-time):**

```bash
nipper logs tail
nipper logs tail --user alice --type thinking
nipper logs tail --type error
nipper logs tail --run run-7f3a
nipper logs tail --format json | jq '.payload'
```

## Agent Status API

The Gateway exposes agent status by querying RabbitMQ consumer state:

```json
{
  "method": "agents.status",
  "params": { "userId": "alice" }
}
```

Response:

```json
{
  "ok": true,
  "result": {
    "userId": "alice",
    "queue": "nipper-agent-alice",
    "consumerCount": 1,
    "messagesReady": 0,
    "messagesUnacknowledged": 1,
    "status": "processing",
    "lastActivity": "2026-02-21T14:30:00Z"
  }
}
```

Status is derived from RabbitMQ queue state:

| Condition | Status |
|-----------|--------|
| `consumerCount > 0` and `messagesUnacknowledged > 0` | `processing` |
| `consumerCount > 0` and `messagesUnacknowledged == 0` | `idle` |
| `consumerCount == 0` and `messagesReady > 0` | `degraded` |
| `consumerCount == 0` and `messagesReady == 0` | `offline` |
