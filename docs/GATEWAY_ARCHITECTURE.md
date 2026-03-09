# Gateway Architecture

## Overview

The Gateway is Open-Nipper's **control plane** — a single process that sits between all messaging channels and the agent processes. Inspired by OpenClaw's hub-and-spoke model, the Gateway accepts messages from any supported channel, normalizes them into a canonical data model, publishes them to **per-user RabbitMQ queues**, and streams responses back through the originating channel.

**RabbitMQ is the sole communication transport between the Gateway and agents** (see `QUEUE_ARCHITECTURE.md`). The Gateway publishes inbound messages to the `nipper.sessions` exchange and consumes agent response events from the `nipper-events-gateway` queue. Agents never communicate with the Gateway directly — all messages flow through RabbitMQ.

The Gateway does not perform AI inference and does not manage agent processes. It handles networking, authentication, message normalization, routing, and delivery. Agents are independent processes that consume from RabbitMQ — see `AGENT_ARCHITECTURE.md`.

The Gateway also exposes a **local administration API** on a separate port (default: `18790`, bound to `127.0.0.1` only) for managing users, identities, and the allowlist at runtime without restarting the system. User data is stored in a SQLite database (see `DATASTORE.md`). Generic operational parameters (channel configs, queue settings, etc.) are stored in config files.

**All inbound messages from every channel are checked against the user allowlist before processing.** Messages from unknown or disallowed users are **silently discarded and the action is logged** to both the audit log and the observability queue. No error response is sent to the sender — the system does not reveal its existence to unauthorized parties.

## Supported Channels

| Channel        | Transport               | Direction         | Use Case                                  |
|----------------|-------------------------|-------------------|-------------------------------------------|
| **WhatsApp**   | HTTP webhook (Wuzapi)   | Bidirectional     | Primary human interaction channel          |
| **Slack**      | HTTP Events API         | Bidirectional     | Team collaboration, notifications          |
| **Cron**       | Internal timer          | Outbound-trigger  | Scheduled tasks, headless automation       |
| **MQTT**       | MQTT subscribe/publish  | Bidirectional     | IoT devices, lightweight M2M events        |
| **WebSocket**  | WS (planned)            | Bidirectional     | Internal clients, dashboards, admin tools   |

## Architecture Diagram

```
                    HUMAN CHANNELS                      MACHINE CHANNELS
              ┌─────────┐  ┌─────────┐          ┌──────────┐
              │ WhatsApp │  │  Slack  │          │   MQTT   │
              │ (Wuzapi) │  │         │          │  Broker  │
              └────┬─────┘  └────┬────┘          └────┬─────┘
                   │             │                    │
         HTTP POST │    HTTP POST│         MQTT SUB   │
         (webhook) │   (events)  │                    │
                   │             │                    │
                   └──────┬──────┴────────────────────┘
                          │                                  │
                          ▼                                  ▼
              ┌────────────────────────────────────────────────────────┐
              │                      GATEWAY                          │
              │                                                       │
              │  ┌──────────────────────────────────────────────────┐  │
              │  │              HTTP Server (:18789)                │  │
              │  │  POST /webhook/whatsapp  ← Wuzapi callbacks     │  │
              │  │  POST /webhook/slack     ← Slack Events API     │  │
              │  │  POST /agents/register   ← Agent auto-register  │  │
              │  │  WS   /ws               ← Internal clients      │  │
              │  └──────────────────────┬───────────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────────────────────────────────────┐  │
              │  │        Admin API Server (:18790, localhost only) │  │
              │  │  POST /admin/users         ← Create user        │  │
              │  │  POST /admin/identities    ← Add identity       │  │
              │  │  POST /admin/allowlist      ← Manage allowlist  │  │
              │  │  POST /admin/agents        ← Provision agent    │  │
              │  │  GET  /admin/users         ← List users         │  │
              │  │  POST /admin/backup        ← Backup datastore   │  │
              │  └──────────────────────────────────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────────▼───────────────────────────┐  │
              │  │              Normalizer                          │  │
              │  │  Channel-native payload → NipperMessage          │  │
              │  └──────────────────────┬───────────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────────▼───────────────────────────┐  │
              │  │              Router                              │  │
              │  │  NipperMessage → session_key resolution          │  │
              │  └──────────────────────┬───────────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────────▼───────────────────────────┐  │
              │  │          Allowlist Guard + Auth + Verify         │  │
              │  │  Check user allowlist (DB) → DISCARD + LOG if   │  │
              │  │  not allowed. Validate HMAC sigs, channel tokens │  │
              │  └──────────────────────┬───────────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────┐   │   ┌───────────────────────┐  │
              │  │  Cron Scheduler  │───┘   │  MQTT / AMQP Clients  │  │
              │  │  (internal)      │       │  (persistent conns)   │  │
              │  └──────────────────┘       └───────────────────────┘  │
              │                         │                              │
              │  ┌──────────────────────▼───────────────────────────┐  │
              │  │              Datastore (SQLite)                  │  │
              │  │  Users, identities, allowlist, policies, agents  │  │
              │  │  See DATASTORE.md                                │  │
              │  └──────────────────────────────────────────────────┘  │
              │                                                       │
              └──────────────────────────┬────────────────────────────┘
                                         │
                              AMQP publish│to nipper.sessions exchange
                                         ▼
                              ┌───────────────────────┐
                              │  RabbitMQ Broker       │ → See QUEUE_ARCHITECTURE.md
                              │  vhost: /nipper        │
                              │                        │
                              │  nipper.sessions  ──►  │ Per-user agent queues
                              │  nipper.events    ◄──  │ Agent response events
                              │  nipper.control   ──►  │ Control signals
                              └───────────┬───────────┘
                                          │
                               AMQP consume│from nipper-agent-{userId}
                                          ▼
                              ┌───────────────────────┐
                              │  Agent Process         │ → See AGENT_ARCHITECTURE.md
                              │  (independent process, │
                              │   any language/SDK)    │
                              └───────────────────────┘
```

## Webhook Endpoints

The Gateway exposes HTTP endpoints that external services call. These are **inbound-only** — the Gateway never initiates HTTP calls to receive messages. Outbound delivery uses each channel's native API.

### WhatsApp Webhook (Wuzapi)

Wuzapi is configured to POST webhook events to the Gateway. The Gateway registers as Wuzapi's webhook target.

```
POST /webhook/whatsapp
Content-Type: application/json
X-Hmac-Signature: sha256=<hmac_hex>

{
  "type": "Message",
  "Info": { ... },
  "Message": { ... },
  "userID": "1",
  "instanceName": "nipper-wa",
  "token": "WUZAPI_USER_TOKEN"
}
```

**Wuzapi webhook setup** (called at Gateway startup):

```
POST http://localhost:8080/webhook
Token: WUZAPI_USER_TOKEN

{
  "WebhookUrl": "http://localhost:18789/webhook/whatsapp",
  "Events": ["Message", "ReadReceipt", "ChatPresence"]
}
```

### Slack Webhook

```
POST /webhook/slack
Content-Type: application/json
X-Slack-Signature: v0=<hmac_hex>
X-Slack-Request-Timestamp: 1708512000

{ "type": "event_callback", "event": { ... } }
```

### Internal WebSocket (Planned)

> **Status: Planned** — not yet implemented. Will provide bidirectional streaming for internal clients (CLI tools, dashboards, admin interfaces).

```
WS /ws
```

Will use a JSON-RPC protocol for real-time event streaming and command execution.

## Canonical Data Model

All channel-specific message formats are converted into this canonical model at the Gateway boundary. Agents never see Wuzapi JSON, Slack payloads, or MQTT packets — they only see `NipperMessage`.

### `ChannelType`

```typescript
type ChannelType =
  | "whatsapp"    // WhatsApp via Wuzapi
  | "slack"       // Slack Events API
  | "cron"        // Internal scheduled jobs
  | "mqtt";       // MQTT broker (IoT, lightweight M2M)
```

### `NipperMessage` (Inbound)

```typescript
interface NipperMessage {
  // Identity
  messageId: string;          // UUIDv7, assigned by Gateway
  originMessageId: string;    // Channel-native message ID (for deduplication)
  timestamp: string;          // ISO 8601, Gateway receive time

  // Routing
  userId: string;             // Resolved Open-Nipper user ID
  sessionKey: string;         // Composite session key (see SESSION_MANAGEMENT.md)
  channelType: ChannelType;   // "whatsapp" | "slack" | "cron" | "mqtt"
  channelId: string;          // Channel-specific identifier

  // Content
  content: MessageContent;    // Normalized content (see below)

  // Metadata
  channelMeta: ChannelMeta;   // Channel-specific metadata (preserved but not used by agents)
  threadId?: string;          // Thread/reply context if applicable
  replyTo?: string;           // messageId this is a reply to

  // Delivery
  deliveryContext: DeliveryContext; // How to send responses back
}
```

### `MessageContent`

```typescript
interface MessageContent {
  type: "text" | "multimodal";
  text?: string;                   // Plain text content
  parts?: ContentPart[];           // For multimodal messages
}

interface ContentPart {
  type: "text" | "image" | "file" | "audio" | "video" | "location" | "contact";
  text?: string;
  mimeType?: string;               // "image/jpeg", "audio/ogg", "application/pdf", etc.
  url?: string;                    // Presigned URL, S3 URL, or local path
  base64?: string;                 // Base64-encoded content (from Wuzapi, small files)
  filename?: string;
  sizeBytes?: number;

  // Location (type: "location")
  latitude?: number;
  longitude?: number;
  locationName?: string;

  // Contact (type: "contact")
  vcard?: string;
}
```

### `ChannelMeta`

Channel-specific metadata is preserved for reference but **never leaks into the agent prompt**. This allows the Gateway to reconstruct channel-native responses.

```typescript
type ChannelMeta =
  | WhatsAppMeta
  | SlackMeta
  | CronMeta
  | MqttMeta
  | RabbitMqMeta;
```

#### WhatsApp Metadata (Wuzapi)

```typescript
interface WhatsAppMeta {
  type: "whatsapp";

  // Wuzapi instance
  wuzapiUserId: string;        // Wuzapi user/instance ID
  wuzapiInstanceName: string;  // Wuzapi instance name
  wuzapiBaseUrl: string;       // Wuzapi API base URL

  // WhatsApp identifiers
  chatJid: string;             // Chat JID: "5491155553934@s.whatsapp.net" (DM) or "120362...@g.us" (group)
  senderJid: string;           // Sender JID: "5491155553935@s.whatsapp.net"
  messageId: string;           // WhatsApp message ID: "3EB06F9067F80BAB89FF"
  pushName: string;            // Sender display name: "John Doe"

  // Context
  isGroup: boolean;
  isFromMe: boolean;
  quotedMessageId?: string;    // ID of the message being replied to
  quotedParticipant?: string;  // JID of the quoted message sender

  // Media (if applicable)
  hasMedia: boolean;
  mediaType?: "image" | "audio" | "video" | "document" | "sticker";
  s3Url?: string;              // S3 URL if Wuzapi S3 storage is configured
  mediaKey?: string;           // WhatsApp media decryption key (for re-download)
}
```

#### Slack Metadata

```typescript
interface SlackMeta {
  type: "slack";
  teamId: string;
  channelId: string;
  slackUserId: string;         // Slack user ID (not Open-Nipper userId)
  threadTs?: string;
  appId: string;
  botToken: string;            // From environment variable, not stored in config
}
```

#### Cron Metadata

```typescript
interface CronMeta {
  type: "cron";
  jobId: string;
  schedule: string;            // Cron expression or interval
  triggeredAt: string;         // ISO 8601
  prompt: string;              // The prompt configured for this cron job
}
```

#### MQTT Metadata

```typescript
interface MqttMeta {
  type: "mqtt";
  broker: string;              // Broker identifier from config
  topic: string;               // MQTT topic: "nipper/user-01/inbox"
  qos: 0 | 1 | 2;             // Quality of service level
  retain: boolean;             // Whether this was a retained message
  clientId: string;            // Publishing client ID
  correlationId?: string;      // For request-response patterns (MQTT 5.0)
  responseTopic?: string;      // Reply-to topic (MQTT 5.0)
  userProperties?: Record<string, string>; // MQTT 5.0 user properties
}
```

### `DeliveryContext`

Tells the system how to route responses back to the user:

```typescript
interface DeliveryContext {
  channelType: ChannelType;
  channelId: string;
  userId: string;
  threadId?: string;
  replyMode: "inline" | "thread" | "dm" | "broadcast" | "reply-to";
  notifyChannels?: string[];   // For cron/headless: additional channels to notify
  capabilities: ChannelCapabilities;
}

interface ChannelCapabilities {
  supportsStreaming: boolean;      // Can we stream token-by-token?
  supportsMarkdown: boolean;
  supportsReactions: boolean;
  supportsThreads: boolean;
  supportsFileUpload: boolean;
  supportsInlineButtons: boolean;
  supportsEditing: boolean;        // Can we edit a sent message?
  supportsReadReceipts: boolean;   // Can we mark messages as read?
  supportsTypingIndicator: boolean;// Can we show "typing..."?
  supportsLocation: boolean;       // Can we send location messages?
  supportsContacts: boolean;       // Can we send contact cards?
  maxMessageLength: number;        // Platform limit
}
```

**Channel Capability Matrix:**

| Capability            | WhatsApp | Slack  | Cron   | MQTT   |
|-----------------------|----------|--------|--------|--------|
| Streaming             | No (*)   | Yes    | No     | No     |
| Markdown              | Partial  | Yes    | N/A    | No     |
| Reactions             | Yes      | Yes    | No     | No     |
| Threads               | Yes      | Yes    | No     | No     |
| File upload           | Yes      | Yes    | No     | No     |
| Inline buttons        | Yes      | Yes    | No     | No     |
| Message editing       | Yes      | Yes    | No     | No     |
| Read receipts         | Yes      | No     | No     | No     |
| Typing indicator      | Yes      | Yes    | No     | No     |
| Location              | Yes      | No     | No     | No     |
| Contacts              | Yes      | No     | No     | No     |
| Max message length    | 65536    | 40000  | N/A    | N/A    |

(*) WhatsApp does not support real-time streaming. The Gateway buffers the full response and sends it as a single message. For long responses, the Gateway can show a typing indicator via Wuzapi's `/chat/presence` endpoint while the agent is processing.

### `NipperResponse` (Outbound)

```typescript
interface NipperResponse {
  // Identity
  responseId: string;           // UUIDv7
  inReplyTo: string;            // messageId of the triggering NipperMessage
  timestamp: string;

  // Routing
  userId: string;
  sessionKey: string;
  deliveryContext: DeliveryContext;

  // Content
  content: MessageContent;
  toolCalls?: ToolCallSummary[];  // Summary of tools used (for UI display)

  // Context
  contextUsage: ContextUsage;     // Always present (see SESSION_MANAGEMENT.md)

  // Streaming
  streamingComplete: boolean;     // false during streaming, true on final message
}
```

### `NipperEvent` (Streaming)

For real-time streaming of assistant responses:

```typescript
interface NipperEvent {
  type: "delta" | "tool_start" | "tool_progress" | "tool_end" | "thinking" | "error" | "done";
  sessionKey: string;
  responseId: string;
  timestamp: string;

  // Type-specific payloads
  delta?: { text: string };
  tool?: { toolCallId: string; name: string; status: string; result?: string };
  thinking?: { text: string };
  error?: { code: string; message: string; recoverable: boolean };
  contextUsage?: ContextUsage;   // Included on "done" events
}
```

For channels that don't support streaming (WhatsApp, MQTT), the Gateway **buffers** all `delta` events and delivers the final assembled response as a single message on the `done` event.

## Channel Adapters

Each channel adapter is a self-contained module that implements the `ChannelAdapter` interface:

```typescript
interface ChannelAdapter {
  channelType: ChannelType;

  // Lifecycle
  start(): Promise<void>;
  stop(): Promise<void>;
  healthCheck(): Promise<HealthStatus>;

  // Inbound: convert channel-native message → NipperMessage
  normalizeInbound(raw: unknown): Promise<NipperMessage>;

  // Outbound: convert NipperResponse/NipperEvent → channel-native format
  deliverResponse(response: NipperResponse): Promise<void>;
  deliverEvent(event: NipperEvent): Promise<void>;

  // Identity: resolve channel user → Open-Nipper userId
  resolveUser(channelUserId: string): Promise<string | null>;
}
```

### WhatsApp Adapter (Wuzapi)

#### Inbound Normalization

```
Wuzapi webhook POST (JSON mode):
{
  "type": "Message",
  "Info": {
    "ID": "3EB06F9067F80BAB89FF",
    "MessageSource": {
      "Chat": "5491155553934@s.whatsapp.net",
      "Sender": "5491155553935@s.whatsapp.net",
      "IsFromMe": false,
      "IsGroup": false
    },
    "PushName": "Alice",
    "Timestamp": "2026-02-21T10:00:05Z",
    "Type": "text"
  },
  "Message": {
    "Conversation": "Deploy the payments service to staging"
  },
  "userID": "1",
  "instanceName": "nipper-wa"
}

                    ▼ WhatsAppAdapter.normalizeInbound()

NipperMessage:
{
  messageId: "01956a3c-...",
  originMessageId: "3EB06F9067F80BAB89FF",
  timestamp: "2026-02-21T10:00:05Z",
  userId: "user-01",                    // Resolved from WhatsApp JID 5491155553935
  sessionKey: "user:user-01:channel:whatsapp:session:sess-01",
  channelType: "whatsapp",
  channelId: "5491155553934@s.whatsapp.net",
  content: {
    type: "text",
    text: "Deploy the payments service to staging"
  },
  channelMeta: {
    type: "whatsapp",
    wuzapiUserId: "1",
    wuzapiInstanceName: "nipper-wa",
    wuzapiBaseUrl: "http://localhost:8080",
    chatJid: "5491155553934@s.whatsapp.net",
    senderJid: "5491155553935@s.whatsapp.net",
    messageId: "3EB06F9067F80BAB89FF",
    pushName: "Alice",
    isGroup: false,
    isFromMe: false,
    hasMedia: false
  },
  deliveryContext: {
    channelType: "whatsapp",
    channelId: "5491155553934@s.whatsapp.net",
    userId: "user-01",
    replyMode: "inline",
    capabilities: {
      supportsStreaming: false,
      supportsMarkdown: false,
      supportsReactions: true,
      supportsThreads: true,
      supportsFileUpload: true,
      supportsInlineButtons: true,
      supportsEditing: true,
      supportsReadReceipts: true,
      supportsTypingIndicator: true,
      supportsLocation: true,
      supportsContacts: true,
      maxMessageLength: 65536
    }
  }
}
```

#### WhatsApp Media Message Normalization

```
Wuzapi webhook (image with S3):
{
  "type": "Message",
  "Info": {
    "ID": "3EB06F9067F80BAB89FG",
    "MessageSource": {
      "Chat": "5491155553934@s.whatsapp.net",
      "Sender": "5491155553935@s.whatsapp.net",
      "IsFromMe": false,
      "IsGroup": false
    },
    "PushName": "Alice",
    "Timestamp": "2026-02-21T10:01:00Z"
  },
  "Message": {
    "ImageMessage": {
      "Caption": "What's wrong with this error?",
      "Mimetype": "image/jpeg",
      "FileLength": 245632,
      "URL": "https://mmg.whatsapp.net/...",
      "MediaKey": "base64key..."
    }
  },
  "s3": {
    "url": "https://my-bucket.s3.amazonaws.com/users/1/inbox/.../image.jpg",
    "mimeType": "image/jpeg",
    "fileName": "3EB06F9067F80BAB89FG.jpg",
    "size": 245632
  },
  "userID": "1",
  "instanceName": "nipper-wa"
}

                    ▼ WhatsAppAdapter.normalizeInbound()

NipperMessage:
{
  messageId: "01956a3d-...",
  originMessageId: "3EB06F9067F80BAB89FG",
  timestamp: "2026-02-21T10:01:00Z",
  userId: "user-01",
  sessionKey: "user:user-01:channel:whatsapp:session:sess-01",
  channelType: "whatsapp",
  channelId: "5491155553934@s.whatsapp.net",
  content: {
    type: "multimodal",
    text: "What's wrong with this error?",
    parts: [
      {
        type: "text",
        text: "What's wrong with this error?"
      },
      {
        type: "image",
        mimeType: "image/jpeg",
        url: "https://my-bucket.s3.amazonaws.com/users/1/inbox/.../image.jpg",
        filename: "3EB06F9067F80BAB89FG.jpg",
        sizeBytes: 245632
      }
    ]
  },
  channelMeta: {
    type: "whatsapp",
    wuzapiUserId: "1",
    wuzapiInstanceName: "nipper-wa",
    wuzapiBaseUrl: "http://localhost:8080",
    chatJid: "5491155553934@s.whatsapp.net",
    senderJid: "5491155553935@s.whatsapp.net",
    messageId: "3EB06F9067F80BAB89FG",
    pushName: "Alice",
    isGroup: false,
    isFromMe: false,
    hasMedia: true,
    mediaType: "image",
    s3Url: "https://my-bucket.s3.amazonaws.com/users/1/inbox/.../image.jpg"
  },
  deliveryContext: { ... }
}
```

#### WhatsApp Outbound Delivery

The adapter delivers responses by calling Wuzapi's REST API:

```
NipperResponse received by WhatsAppAdapter
        │
        ▼
Show typing indicator:
  POST http://localhost:8080/chat/presence
  Token: WUZAPI_USER_TOKEN
  { "Phone": "5491155553935", "State": "composing" }
        │
        ▼
Send text response:
  POST http://localhost:8080/chat/send/text
  Token: WUZAPI_USER_TOKEN
  {
    "Phone": "5491155553935",
    "Body": "I'll deploy the payments service to staging now...",
    "ContextInfo": {
      "StanzaId": "3EB06F9067F80BAB89FF",            // Quote the original message
      "Participant": "5491155553935@s.whatsapp.net"
    }
  }
        │
        ▼
Mark original message as read:
  POST http://localhost:8080/chat/markread
  Token: WUZAPI_USER_TOKEN
  { "Id": "3EB06F9067F80BAB89FF", "Chat": "5491155553934@s.whatsapp.net" }
```

For multimodal responses (sending images, documents):

```
POST http://localhost:8080/chat/send/image
Token: WUZAPI_USER_TOKEN
{
  "Phone": "5491155553935",
  "Caption": "Here's the deployment log",
  "Image": "base64_encoded_image_data..."
}
```

#### WhatsApp HMAC Verification

The Gateway verifies Wuzapi webhook signatures before processing:

```typescript
function verifyWuzapiHmac(
  body: Buffer,
  signature: string,       // From x-hmac-signature header
  hmacKey: string           // Configured HMAC key
): boolean {
  const expected = crypto
    .createHmac("sha256", hmacKey)
    .update(body)
    .digest("hex");
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  );
}
```

#### WhatsApp Event Filtering

The adapter only processes `Message` events from Wuzapi. Other event types are handled for UX but don't trigger agent runs:

| Wuzapi Event      | Gateway Action                                             |
|--------------------|------------------------------------------------------------|
| `Message`          | Normalize → enqueue → agent processes                      |
| `ReadReceipt`      | Update delivery status in session metadata                 |
| `ChatPresence`     | Log (typing indicators from WhatsApp users)                |
| `Connected`        | Log, update health status                                  |
| `Disconnected`     | Log, alert, attempt reconnect via Wuzapi `/session/connect` |
| All others         | Log, ignore                                                |

### Slack Adapter

```
Slack Event API payload:
{
  "type": "event_callback",
  "event": {
    "type": "message",
    "text": "deploy the payments service",
    "user": "U0123ABC",
    "channel": "C0456DEF",
    "ts": "1708512000.000100",
    "thread_ts": "1708511900.000050"
  }
}

                    ▼ SlackAdapter.normalizeInbound()

NipperMessage:
{
  messageId: "01956a3c-...",
  originMessageId: "1708512000.000100",
  userId: "user-01",
  sessionKey: "user:user-01:channel:slack:session:sess-01",
  channelType: "slack",
  channelId: "C0456DEF",
  content: { type: "text", text: "deploy the payments service" },
  channelMeta: {
    type: "slack",
    teamId: "T0123",
    channelId: "C0456DEF",
    slackUserId: "U0123ABC",
    threadTs: "1708511900.000050",
    appId: "A0789",
    botToken: "${SLACK_BOT_TOKEN}"
  },
  deliveryContext: {
    channelType: "slack",
    channelId: "C0456DEF",
    userId: "user-01",
    threadId: "1708511900.000050",
    replyMode: "thread",
    capabilities: {
      supportsStreaming: true,
      supportsMarkdown: true,
      supportsReactions: true,
      supportsThreads: true,
      supportsFileUpload: true,
      supportsInlineButtons: true,
      supportsEditing: true,
      supportsReadReceipts: false,
      supportsTypingIndicator: true,
      supportsLocation: false,
      supportsContacts: false,
      maxMessageLength: 40000
    }
  }
}
```

### Cron Adapter

```
Cron job fires at 09:00 UTC:
{
  jobId: "daily-report",
  schedule: "0 9 * * *",
  userId: "user-02",
  prompt: "Check server logs and report anomalies",
  notifyChannel: "slack:C0789GHI"
}

                    ▼ CronAdapter.normalizeInbound()

NipperMessage:
{
  messageId: "01956a3d-...",
  originMessageId: "cron:daily-report:2026-02-21T09:00:00Z",
  userId: "user-02",
  sessionKey: "user:user-02:channel:cron:session:daily-report",
  channelType: "cron",
  channelId: "daily-report",
  content: { type: "text", text: "Check server logs and report anomalies" },
  channelMeta: {
    type: "cron",
    jobId: "daily-report",
    schedule: "0 9 * * *",
    triggeredAt: "2026-02-21T09:00:00Z",
    prompt: "Check server logs and report anomalies"
  },
  deliveryContext: {
    channelType: "cron",
    channelId: "daily-report",
    userId: "user-02",
    replyMode: "broadcast",
    notifyChannels: ["slack:C0789GHI"],
    capabilities: {
      supportsStreaming: false,
      supportsMarkdown: false,
      supportsReactions: false,
      supportsThreads: false,
      supportsFileUpload: false,
      supportsInlineButtons: false,
      supportsEditing: false,
      supportsReadReceipts: false,
      supportsTypingIndicator: false,
      supportsLocation: false,
      supportsContacts: false,
      maxMessageLength: 0
    }
  }
}
```

### MQTT Adapter

MQTT provides lightweight machine-to-machine messaging. The adapter subscribes to topics and publishes responses.

**Topic Convention:**

```
Inbound:  nipper/{userId}/inbox          # Messages TO the agent
Outbound: nipper/{userId}/outbox         # Responses FROM the agent
Status:   nipper/{userId}/status         # Agent status updates
Events:   nipper/{userId}/events/{type}  # Typed event stream
```

**Inbound Normalization:**

```
MQTT message received on topic: nipper/user-01/inbox
QoS: 1
Payload:
{
  "text": "What is the CPU usage on node-03?",
  "clientId": "sensor-gateway-01",
  "correlationId": "req-42",
  "responseTopic": "sensors/gateway-01/responses"
}

                    ▼ MqttAdapter.normalizeInbound()

NipperMessage:
{
  messageId: "01956a3e-...",
  originMessageId: "mqtt:sensor-gateway-01:req-42",
  userId: "user-01",
  sessionKey: "user:user-01:channel:mqtt:session:sensor-gateway-01",
  channelType: "mqtt",
  channelId: "nipper/user-01/inbox",
  content: { type: "text", text: "What is the CPU usage on node-03?" },
  channelMeta: {
    type: "mqtt",
    broker: "local-mosquitto",
    topic: "nipper/user-01/inbox",
    qos: 1,
    retain: false,
    clientId: "sensor-gateway-01",
    correlationId: "req-42",
    responseTopic: "sensors/gateway-01/responses"
  },
  deliveryContext: {
    channelType: "mqtt",
    channelId: "nipper/user-01/outbox",
    userId: "user-01",
    replyMode: "reply-to",
    capabilities: {
      supportsStreaming: false,
      supportsMarkdown: false,
      supportsReactions: false,
      supportsThreads: false,
      supportsFileUpload: false,
      supportsInlineButtons: false,
      supportsEditing: false,
      supportsReadReceipts: false,
      supportsTypingIndicator: false,
      supportsLocation: false,
      supportsContacts: false,
      maxMessageLength: 0
    }
  }
}
```

**Outbound Delivery:**

```
MQTT PUBLISH
  Topic: sensors/gateway-01/responses    (from responseTopic, or fallback to nipper/{userId}/outbox)
  QoS: 1
  Payload:
  {
    "responseId": "01956a3f-...",
    "inReplyTo": "01956a3e-...",
    "correlationId": "req-42",
    "text": "CPU usage on node-03 is currently at 78.3%",
    "contextUsage": { ... }
  }
```

## Gateway Server

### Protocol

The Gateway exposes **two** HTTP servers:

1. **Main server** on `http://127.0.0.1:18789` — Channel webhooks and WebSocket for internal clients.
2. **Admin server** on `http://127.0.0.1:18790` — Local-only REST API for user and system administration.

Both bind to `127.0.0.1` by default. The admin server **must** never be exposed to the network — it provides unauthenticated access to user management operations. Use an SSH tunnel or reverse proxy with authentication if remote admin access is needed.

#### Main Server (Channel + WebSocket)

- **HTTP routes** for channel webhooks (`/webhook/whatsapp`, `/webhook/slack`)
- **WebSocket** for internal clients (`/ws`), using JSON-RPC-style request/response

**WebSocket Request:**
```json
{
  "id": "req-001",
  "method": "sessions.create",
  "params": { "userId": "...", "channelType": "whatsapp" }
}
```

**WebSocket Response:**
```json
{
  "id": "req-001",
  "ok": true,
  "result": { "sessionId": "...", "contextUsage": { ... } }
}
```

**WebSocket Event (server-push):**
```json
{
  "type": "event",
  "payload": {
    "type": "delta",
    "sessionKey": "user:user-01:channel:whatsapp:session:sess-01",
    "delta": { "text": "Deploying..." }
  }
}
```

### WebSocket API Methods

| Method               | Description                                  |
|----------------------|----------------------------------------------|
| `sessions.create`    | Forward to agent via RabbitMQ control message |
| `sessions.list`      | Forward to agent via RabbitMQ control message |
| `sessions.info`      | Forward to agent via RabbitMQ control message |
| `sessions.compact`   | Forward to agent via RabbitMQ control message |
| `sessions.reset`     | Forward to agent via RabbitMQ control message |
| `sessions.delete`    | Forward to agent via RabbitMQ control message |
| `chat.send`          | Send a message to a session                  |
| `chat.stream`        | Subscribe to streaming events for a session  |
| `agents.status`      | Get agent queue/consumer status              |
| `config.get`         | Get current configuration                    |
| `config.set`         | Update configuration                         |

### Agent Auto-Registration Endpoint

The main server exposes an endpoint that agents call at startup to retrieve their RabbitMQ configuration. This endpoint lives on the main server (`:18789`), not the admin server, because agents may be running on remote hosts. It is authenticated by the agent's `npr_` auth token, issued during provisioning via the admin API.

```
POST /agents/register
Authorization: Bearer npr_a1b2c3d4e5f6...
Content-Type: application/json
```

**Request body (optional metadata):**
```json
{
  "agent_type": "anthropic-sdk",
  "version": "1.0.0",
  "hostname": "agent-host-01",
  "capabilities": ["streaming", "tool_use", "extended_thinking"]
}
```

The request body is optional — useful for observability and health dashboards but not required for registration.

**Response:**
```json
{
  "ok": true,
  "result": {
    "agent_id": "agt-user01-01",
    "user_id": "user-01",
    "user_name": "Alice",

    "rabbitmq": {
      "url": "amqp://rabbitmq.example.com:5672",
      "tls_url": "amqps://rabbitmq.example.com:5671",
      "username": "agent-agt-user01-01",
      "password": "generated-secure-password",
      "vhost": "/nipper",

      "queues": {
        "agent": "nipper-agent-user-01",
        "control": "nipper-control-user-01"
      },
      "exchanges": {
        "sessions": "nipper.sessions",
        "events": "nipper.events",
        "control": "nipper.control",
        "logs": "nipper.logs"
      },
      "routing_keys": {
        "events_publish": "nipper.events.{userId}.{sessionId}",
        "logs_publish": "nipper.logs.{userId}.{eventType}"
      }
    },

    "user": {
      "id": "user-01",
      "name": "Alice",
      "default_model": "claude-sonnet-4-20250514",
      "preferences": {}
    },

    "policies": {
      "tools": {
        "allow": ["read", "write", "edit", "exec", "memory_read", "memory_write"],
        "deny": ["session_spawn", "message"]
      }
    }
  }
}
```

#### Registration Flow

```
Agent process starts with NIPPER_GATEWAY_URL + NIPPER_AUTH_TOKEN
        │
        ▼
POST /agents/register
Authorization: Bearer npr_a1b2...
        │
        ▼
Gateway validates token:
  SHA-256(token) == agents.token_hash AND agents.status != "revoked"
  ├── Invalid/revoked → 401 Unauthorized
  └── Valid → Continue
        │
        ▼
Gateway looks up agent → user binding
        │
        ▼
Gateway checks user is enabled
  ├── Disabled → 403 Forbidden
  └── Enabled → Continue
        │
        ▼
Gateway creates/updates RabbitMQ user via Management API:
  PUT /api/users/agent-{agentId}
  Set vhost permissions:
    configure: ^(nipper-agent-{userId}|nipper-control-{userId})$
    write:     ^(nipper\.events|nipper\.logs)$
    read:      ^(nipper-agent-{userId}|nipper-control-{userId})$
        │
        ▼
Gateway updates agents table:
  status → "registered"
  last_registered_at → now
  last_registered_ip → request IP
  rmq_username → "agent-{agentId}"
        │
        ▼
Gateway logs agent.registered to admin_audit
        │
        ▼
Return config blob (RabbitMQ credentials, queue names, exchanges, user info, policies)
```

#### Idempotent Registration

Registration is **idempotent**. An agent can call `/agents/register` multiple times:

- **First call**: Creates the RabbitMQ user, sets permissions, returns credentials
- **Subsequent calls**: Returns the same RabbitMQ username but generates a **new password** (when `token_rotation_on_register` is enabled), updates the RabbitMQ user's password via the Management API

This means an agent can safely re-register on restart — it always gets fresh, valid credentials. The previous password is invalidated, which cleanly handles the case where an agent process crashes and a new instance starts.

#### RabbitMQ Permission Scoping

The RabbitMQ user created for each agent has tightly scoped permissions on the `/nipper` vhost:

| Permission | Regex | Effect |
|------------|-------|--------|
| **configure** | `^(nipper-agent-{userId}\|nipper-control-{userId})$` | Can only declare/assert its own queues |
| **write** | `^(nipper\.events\|nipper\.logs)$` | Can publish events and logs only |
| **read** | `^(nipper-agent-{userId}\|nipper-control-{userId})$` | Can only consume its own queues |

This prevents an agent from reading another user's queue, publishing to the sessions exchange (only the Gateway publishes there), or declaring arbitrary queues.

#### Error Responses

| Scenario | HTTP Status | Response |
|----------|-------------|----------|
| Missing or malformed Authorization header | `401` | `{"ok": false, "error": "unauthorized"}` |
| Invalid token (hash mismatch) | `401` | `{"ok": false, "error": "unauthorized"}` |
| Revoked agent | `401` | `{"ok": false, "error": "unauthorized"}` |
| Agent's user disabled | `403` | `{"ok": false, "error": "user_disabled"}` |
| Agent's user deleted (cascade) | `401` | `{"ok": false, "error": "unauthorized"}` |
| RabbitMQ Management API unreachable | `503` | `{"ok": false, "error": "service_unavailable", "retry_after": 30}` |
| Rate limited | `429` | `{"ok": false, "error": "rate_limited", "retry_after": 60}` |

Error responses for authentication failures are intentionally vague (`"unauthorized"`) to avoid leaking information about token validity, agent existence, or user status.

#### Agent cron API (list / add / remove scheduled jobs)

When the cron channel adapter is enabled, the main server also exposes agent-scoped cron endpoints. Agents call these with the same Bearer token used for registration; the gateway resolves the token to the agent's `user_id` and scopes all operations to that user. **Cron jobs are prompts only** — each job has an `id`, `schedule` (6-field cron with seconds), `prompt` (the message sent to the agent when the job fires), and optional `notify_channels`. There is no command or script field.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/agents/me/notify-channels` | List this user's delivery channels in `channelType:channelId` format (for cron `notify_channels`) |
| GET | `/agents/me/cron/jobs` | List cron jobs for the authenticated agent's user |
| POST | `/agents/me/cron/jobs` | Add a cron job (body: `id`, `schedule`, `prompt`, optional `notify_channels`) |
| DELETE | `/agents/me/cron/jobs/{id}` | Remove a cron job by id (only jobs owned by the user can be removed) |

Jobs are persisted in the `cron_jobs` table (see `DATASTORE.md`) and loaded into the cron scheduler at gateway startup; additions and removals via this API take effect immediately. The agent can use the Eino tools `cron_list_jobs`, `cron_list_notify_channels`, `cron_add_job`, and `cron_remove_job` when `tools.cron` is enabled and policy allows them. Use `cron_list_notify_channels` to obtain valid `notify_channels` values (e.g. `whatsapp:5491155553935@s.whatsapp.net`) before adding a job.

### Admin API (Local Endpoint)

The admin API is a REST endpoint exposed on a separate port, bound to localhost only. It provides administrative operations that modify the datastore (see `DATASTORE.md`). All admin actions are logged to the `admin_audit` table.

**Base URL:** `http://127.0.0.1:18790/admin`

#### User Management

```
POST   /admin/users                   Create a new user
GET    /admin/users                   List all users
GET    /admin/users/{userId}          Get user details
PUT    /admin/users/{userId}          Update user (name, model, enabled)
DELETE /admin/users/{userId}          Delete user and all associated data
```

**Create user:**
```bash
curl -X POST http://127.0.0.1:18790/admin/users \
  -H "Content-Type: application/json" \
  -d '{
    "id": "user-01",
    "name": "Alice",
    "default_model": "claude-sonnet-4-20250514",
    "enabled": true
  }'
```

**Response:**
```json
{
  "ok": true,
  "result": {
    "id": "user-01",
    "name": "Alice",
    "enabled": true,
    "default_model": "claude-sonnet-4-20250514",
    "created_at": "2026-02-21T15:00:00Z"
  }
}
```

#### Identity Management

```
POST   /admin/users/{userId}/identities      Add a channel identity
GET    /admin/users/{userId}/identities      List user's identities
DELETE /admin/users/{userId}/identities/{id}  Remove an identity
```

**Add WhatsApp identity:**
```bash
curl -X POST http://127.0.0.1:18790/admin/users/user-01/identities \
  -H "Content-Type: application/json" \
  -d '{
    "channel_type": "whatsapp",
    "channel_identity": "5491155553935@s.whatsapp.net"
  }'
```

**Add Slack identity:**
```bash
curl -X POST http://127.0.0.1:18790/admin/users/user-01/identities \
  -H "Content-Type: application/json" \
  -d '{
    "channel_type": "slack",
    "channel_identity": "U0123ABC"
  }'
```

#### Allowlist Management

```
POST   /admin/allowlist                      Add user to allowlist for a channel
GET    /admin/allowlist                      List all allowlist entries
GET    /admin/allowlist/{channelType}        List allowed users for a channel
PUT    /admin/allowlist/{userId}/{channelType}  Enable/disable user on channel
DELETE /admin/allowlist/{userId}/{channelType}  Remove user from channel allowlist
```

**Allow user on all channels:**
```bash
curl -X POST http://127.0.0.1:18790/admin/allowlist \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-01",
    "channel_type": "*",
    "enabled": true
  }'
```

**Allow user on WhatsApp only:**
```bash
curl -X POST http://127.0.0.1:18790/admin/allowlist \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-02",
    "channel_type": "whatsapp",
    "enabled": true
  }'
```

#### Policy Management

```
GET    /admin/users/{userId}/policies          List user's policies
PUT    /admin/users/{userId}/policies/{type}   Set/update a policy
DELETE /admin/users/{userId}/policies/{type}   Remove policy override (use defaults)
```

**Set tool policy:**
```bash
curl -X PUT http://127.0.0.1:18790/admin/users/user-02/policies/tools \
  -H "Content-Type: application/json" \
  -d '{
    "allow": ["read", "write", "edit", "exec", "memory_*"],
    "deny": ["session_spawn"],
    "require_confirmation": ["exec"]
  }'
```

#### Agent Provisioning

Agents are provisioned via the admin API, which binds an agent to a user and generates an auth token. The agent uses this token to auto-register and receive its RabbitMQ configuration (see Agent Auto-Registration Endpoint below).

```
POST   /admin/agents                   Provision a new agent (bound to a user)
GET    /admin/agents                   List all provisioned agents
GET    /admin/agents/{agentId}         Get agent details
DELETE /admin/agents/{agentId}         Deprovision agent (revoke token, delete RMQ user)
POST   /admin/agents/{agentId}/rotate  Rotate auth token (invalidates previous token)
POST   /admin/agents/{agentId}/revoke  Revoke agent without deleting record
```

**Provision agent:**
```bash
curl -X POST http://127.0.0.1:18790/admin/agents \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "user-01",
    "label": "anthropic-primary"
  }'
```

**Response (auth token shown ONCE):**
```json
{
  "ok": true,
  "result": {
    "agent_id": "agt-user01-01",
    "user_id": "user-01",
    "label": "anthropic-primary",
    "status": "provisioned",
    "auth_token": "npr_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2w3x4y5z6",
    "created_at": "2026-02-21T15:00:00Z"
  }
}
```

The `auth_token` uses the prefix `npr_` (nipper) for easy identification in logs and secret scanners — same pattern as `ghp_` (GitHub), `xoxb-` (Slack), `sk-` (OpenAI). The token is a cryptographically random 48-byte value, base62-encoded. It is shown once and never stored — only its SHA-256 hash is persisted in the `agents` table (see `DATASTORE.md`).

**What provisioning does:**

1. Validates the target user exists and is enabled
2. Generates the `npr_` prefixed auth token (48 bytes, cryptographically random, base62-encoded)
3. Stores `SHA-256(token)` and `token[0:8]` (prefix for identification) in the `agents` table
4. Ensures the user's RabbitMQ queues exist (`nipper-agent-{userId}`, `nipper-control-{userId}`) with correct bindings on the `nipper.sessions` and `nipper.control` exchanges
5. Logs `agent.provisioned` to the `admin_audit` table
6. Returns the plaintext token in the response (one-time)

**Rotate token:**
```bash
curl -X POST http://127.0.0.1:18790/admin/agents/agt-user01-01/rotate
```

Response includes a new `auth_token`. The previous token is immediately invalidated. Any agent process using the old token must re-register with the new one.

**Deprovision agent:**
```bash
curl -X DELETE http://127.0.0.1:18790/admin/agents/agt-user01-01
```

Deprovisioning revokes the auth token, deletes the agent's RabbitMQ user via the Management API (which severs any active AMQP connections), removes the agent record from the database, and logs `agent.deprovisioned` to the audit table. The user's RabbitMQ queues (`nipper-agent-{userId}`) are **not** deleted — they belong to the user, not the agent. A new agent can be provisioned for the same user.

**List agents:**
```bash
curl http://127.0.0.1:18790/admin/agents
```

```json
{
  "ok": true,
  "result": [
    {
      "agent_id": "agt-user01-01",
      "user_id": "user-01",
      "label": "anthropic-primary",
      "status": "registered",
      "token_prefix": "npr_a1b2",
      "rmq_username": "agent-agt-user01-01",
      "last_registered_at": "2026-02-21T15:30:00Z",
      "created_at": "2026-02-21T15:00:00Z"
    }
  ]
}
```

#### System Operations

```
GET    /admin/health                   System health check
GET    /admin/audit                    Query audit log (with filters)
POST   /admin/backup                   Trigger database backup
GET    /admin/config                   View current config (secrets redacted)
```

**Query audit log:**
```bash
curl "http://127.0.0.1:18790/admin/audit?since=2026-02-21T00:00:00Z&action=user.created"
```

#### Admin API Configuration

```yaml
gateway:
  bind: "127.0.0.1"
  port: 18789

  admin:
    enabled: true
    bind: "127.0.0.1"               # MUST be localhost — never expose to network
    port: 18790
    auth:
      enabled: false                 # Disabled by default (localhost-only is sufficient)
      token: "${ADMIN_API_TOKEN}"    # Optional: require bearer token if auth enabled

  agents:
    registration:
      enabled: true                  # Enable the /agents/register endpoint on main server
      rate_limit: 10                 # Max registrations per minute per token
      token_rotation_on_register: true # Rotate RMQ password on each registration

    rabbitmq_management:
      url: "http://localhost:15672"  # RabbitMQ Management API (for creating agent users)
      username: "${RABBITMQ_MGMT_USERNAME}"
      password: "${RABBITMQ_MGMT_PASSWORD}"
```

#### CLI Convenience Wrapper

The `nipper` CLI wraps the admin API for common operations:

```bash
# User management
nipper admin user add --id user-01 --name "Alice" --model claude-sonnet-4-20250514
nipper admin user list
nipper admin user disable user-01
nipper admin user delete user-01

# Identity management
nipper admin identity add user-01 --channel whatsapp --identity "5491155553935@s.whatsapp.net"
nipper admin identity add user-01 --channel slack --identity "U0123ABC"
nipper admin identity list user-01

# Allowlist management
nipper admin allow user-01 --channel "*"
nipper admin allow user-02 --channel whatsapp
nipper admin deny user-03 --channel mqtt
nipper admin allowlist show

# Agent provisioning
nipper admin agent provision --user user-01 --label "anthropic-primary"
nipper admin agent list
nipper admin agent list --user user-01
nipper admin agent rotate-token agt-user01-01
nipper admin agent revoke agt-user01-01
nipper admin agent delete agt-user01-01

# Backup
nipper admin backup
```

The `nipper admin agent provision` command outputs the auth token and a ready-to-use startup command:

```
Agent provisioned successfully.
Agent ID:   agt-user01-01
Auth Token: npr_a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2w3x4y5z6

Save this token — it will not be shown again.

Start your agent with:
  NIPPER_GATEWAY_URL=http://gateway:18789 \
  NIPPER_AUTH_TOKEN=npr_a1b2c3d4... \
  python my_agent.py
```

### Gateway Lifecycle

```
1. Load configuration (config.yaml + env vars)
2. Resolve secrets from environment variables
3. Open datastore (SQLite — see DATASTORE.md)
4. Run schema migrations (if needed)
5. Load users, identities, allowlist, and agents into memory cache
6. Initialize in-memory DeliveryContext registry (session files are agent-side only)
7. Connect to RabbitMQ broker (internal queue system):
   a. Declare exchanges: nipper.sessions, nipper.events, nipper.control, nipper.sessions.dlx
   b. Declare/bind nipper-events-gateway queue (Gateway consumes agent events here)
   c. Start consuming from nipper-events-gateway (Agent → Gateway event stream)
   d. Ensure per-user agent queues exist for all provisioned agents
8. Connect to RabbitMQ Management API (if agents.registration.enabled):
   a. Verify connectivity to http://localhost:15672
   b. Validate management credentials
9. Start HTTP server (webhook endpoints + /agents/register, :18789)
10. Start Admin API server (local only, :18790)
11. Start WebSocket server
12. Connect to Wuzapi, register webhook URL
13. Start Slack adapter (Events API subscription)
14. Connect to MQTT broker (channel adapter), subscribe to inbound topics
15. Start cron scheduler
17. Run security audit (see SECURITY.md)
18. Accept connections
    └─▶ For each inbound message (from any channel):
        a. Adapter normalizes → NipperMessage
        b. Gateway resolves userId from channel identity (datastore lookup)
        c. *** ALLOWLIST CHECK ***
           ├── User not found in datastore → DISCARD message, LOG action, return 200
           ├── User found but NOT in allowlist for this channel → DISCARD, LOG, return 200
           ├── User found but disabled → DISCARD, LOG, return 200
           └── User found AND allowed → Continue
        d. Gateway validates channel-specific auth (HMAC sigs, channel tokens)
        e. Gateway resolves session_key (pure computation — no file I/O)
        f. Gateway registers DeliveryContext in in-memory registry
        g. Gateway publishes QueueItem to nipper.sessions exchange
           (routing key: nipper.sessions.{userId}.{sessionId})
        h. RabbitMQ routes to per-user agent queue (nipper-agent-{userId})
        i. Agent consumes message, creates session if needed, processes it
        j. Agent publishes NipperEvents to nipper.events exchange
        k. Gateway consumes events from nipper-events-gateway queue
        l. Gateway looks up DeliveryContext from registry, routes to correct adapter
        m. Adapter delivers to channel-native format
```

## Message Flow: WhatsApp End-to-End

```
User sends WhatsApp message
        │
        ▼
WhatsApp servers → Wuzapi (whatsmeow WebSocket)
        │
        ▼
Wuzapi POST /webhook/whatsapp (JSON + HMAC signature)
        │
        ▼
Gateway HTTP handler receives POST
        │
        ▼
Verify HMAC signature (x-hmac-signature header)
  ├── Invalid → 401, log attempt
  └── Valid → Continue
        │
        ▼
Filter event type
  ├── Not "Message" → Handle status event, return 200
  └── "Message" → Continue
        │
        ▼
Filter self-messages
  ├── IsFromMe: true → Ignore (prevents echo loops), return 200
  └── IsFromMe: false → Continue
        │
        ▼
WhatsAppAdapter.normalizeInbound() → NipperMessage
        │
        ▼
Gateway.resolveUser(senderJid: "5491155553935@s.whatsapp.net")
  ├── Unknown JID → DISCARD + LOG (see Allowlist Guard), return 200
  └── Known user → "user-01"
        │
        ▼
*** ALLOWLIST GUARD ***
Gateway.checkAllowlist(userId: "user-01", channelType: "whatsapp")
  ├── User disabled → DISCARD + LOG, return 200
  ├── User NOT in allowlist for "whatsapp" → DISCARD + LOG, return 200
  └── User allowed → Continue
        │
        ▼
Gateway.resolveSessionKey(message) — pure computation, no file I/O
  └── Derive session_key from channel meta (chatJID, channelID, jobID, etc.)
        │
        ▼
Gateway.registry.Register(sessionKey, deliveryContext) — in-memory
        │
        ▼
Mark message as read (async, non-blocking):
  POST Wuzapi /chat/markread
        │
        ▼
Show typing indicator (async, non-blocking):
  POST Wuzapi /chat/presence { "State": "composing" }
        │
        ▼
Gateway publishes QueueItem to RabbitMQ:
  Exchange: nipper.sessions
  Routing key: nipper.sessions.user-01.{sessionId}
  └── See QUEUE_ARCHITECTURE.md
        │
        ▼
RabbitMQ routes to per-user agent queue: nipper-agent-user-01
        │
        ▼
Agent consumes message from RabbitMQ, creates session on disk if new, processes it
  └── See AGENT_ARCHITECTURE.md (agents own session files, not the gateway)
        │
        ▼
Agent publishes NipperEvents to nipper.events exchange (buffered for WhatsApp)
        │
        ▼
Gateway consumes events from nipper-events-gateway queue
        │
        ▼
On "done" event → WhatsAppAdapter.deliverResponse()
  └── POST Wuzapi /chat/send/text (or /image, /document, etc.)
        │
        ▼
Clear typing indicator:
  POST Wuzapi /chat/presence { "State": "paused" }
        │
        ▼
Return 200 to original webhook (if still within timeout)
```

## Channel Registration

Channels are registered at startup via configuration:

```yaml
gateway:
  bind: "127.0.0.1"
  port: 18789
  admin:
    enabled: true
    bind: "127.0.0.1"               # Local-only admin endpoint
    port: 18790

channels:
  whatsapp:
    enabled: true
    adapter: "whatsapp"
    config:
      wuzapiBaseUrl: "http://localhost:8080"
      wuzapiToken: "${WUZAPI_USER_TOKEN}"
      wuzapiHmacKey: "${WUZAPI_HMAC_KEY}"
      wuzapiInstanceName: "nipper-wa"
      webhookPath: "/webhook/whatsapp"
      webhookFormat: "json"            # Must be "json" (not "form")
      events: ["Message", "ReadReceipt", "ChatPresence", "Connected", "Disconnected"]
      s3:
        enabled: true                  # Use Wuzapi S3 for media storage
      delivery:
        markAsRead: true               # Auto-mark inbound messages as read
        showTyping: true               # Show typing indicator while processing
        quoteOriginal: true            # Quote the user's message in replies

  slack:
    enabled: true
    adapter: "slack"
    config:
      appToken: "${SLACK_APP_TOKEN}"
      botToken: "${SLACK_BOT_TOKEN}"
      signingSecret: "${SLACK_SIGNING_SECRET}"
      webhookPath: "/webhook/slack"

  cron:
    enabled: true
    adapter: "cron"
    jobs:
      - id: "daily-report"
        schedule: "0 9 * * *"
        userId: "user-02"
        prompt: "Check server logs and report anomalies"
        notifyChannel: "slack:C0789GHI"
      - id: "weekly-summary"
        schedule: "0 10 * * 1"
        userId: "user-01"
        prompt: "Generate weekly project summary"
        notifyChannel: "whatsapp:5491155553935@s.whatsapp.net"

  mqtt:
    enabled: true
    adapter: "mqtt"
    config:
      broker: "mqtt://localhost:1883"
      clientId: "open-nipper-gateway"
      username: "${MQTT_USERNAME}"
      password: "${MQTT_PASSWORD}"
      topicPrefix: "nipper"            # Subscribe to nipper/{userId}/inbox
      qos: 1
      cleanSession: false              # Persistent session for offline messages
      keepAlive: 60
      reconnect:
        enabled: true
        initialDelayMs: 1000
        maxDelayMs: 30000

```

All Gateway secrets are resolved from environment variables at startup. This keeps the Gateway lightweight and free of external secret-management dependencies. Agent-side secrets use 1Password via `op` CLI (see `AGENT_ARCHITECTURE.md` and `SECURITY.md`).

Channel adapter configuration lives in **config files** (`~/.open-nipper/config.yaml`), not in the database. These are static infrastructure settings that rarely change and require a restart to take effect. User data (who can use which channel) lives in the **datastore** — see `DATASTORE.md`.

## User Resolution

The Gateway maps channel-native user identifiers to Open-Nipper user IDs using the datastore (see `DATASTORE.md`). Users and their channel identities are stored in the `users` and `user_identities` tables, managed at runtime via the admin API.

**Resolution by channel:**

| Channel    | Native Identifier                     | Resolution                              |
|------------|---------------------------------------|-----------------------------------------|
| WhatsApp   | Sender JID from Wuzapi `Info.MessageSource.Sender` | Query `user_identities` WHERE `channel_type='whatsapp'` AND `channel_identity=JID` |
| Slack      | Slack user ID from event payload      | Query `user_identities` WHERE `channel_type='slack'` AND `channel_identity=slackUserId` |
| Cron       | `userId` from job config              | Direct match against `users.id`         |
| MQTT       | `x-nipper-user` header or topic path  | Extract userId from topic `nipper/{userId}/inbox`, validate against `users.id` |

Unknown identities are rejected and the rejection is logged. There is no implicit user creation — users must be added via the admin API before they can interact with the system.

For MQTT, the userId is embedded in the topic structure. The adapter validates that the claimed userId exists in the datastore and that the claimed userId matches the authenticated connection (if MQTT auth is configured).

## Allowlist Guard

**Every inbound message from every channel passes through the allowlist guard before any further processing.** This is the first check after user resolution. Messages that fail the allowlist check are **silently discarded** — no error response is sent to the sender.

### Enforcement Flow

```
Inbound message arrives (any channel)
        │
        ▼
Resolve userId from channel identity (datastore lookup)
  ├── Identity not found → DISCARD + LOG
  └── Identity found → userId resolved
        │
        ▼
Check user enabled (users.enabled)
  ├── User disabled → DISCARD + LOG
  └── User enabled → Continue
        │
        ▼
Check allowlist (allowed_list table)
  ├── Entry with channel_type="*" and enabled=true → ALLOWED
  ├── Entry with channel_type="{this_channel}" and enabled=true → ALLOWED
  ├── No matching entry or entry disabled → DISCARD + LOG
  └── ALLOWED → Continue to routing + processing
```

### What Gets Logged on Rejection

Every discarded message produces an audit log entry and an observability event:

```typescript
interface AllowlistRejection {
  timestamp: string;               // ISO 8601
  event_type: "message.rejected";
  reason: "unknown_identity" | "user_disabled" | "not_in_allowlist";
  channel_type: ChannelType;       // "whatsapp" | "slack" | "mqtt"
  channel_identity: string;        // The raw channel identifier (redacted in observability logs)
  resolved_user_id?: string;       // If identity was resolved but user was disabled/not allowed
  source_ip?: string;              // For HTTP channels (webhooks)
  message_excerpt?: string;        // First 50 chars of message text (redacted in observability logs)
}
```

**Audit log entry** (written to `admin_audit` table in datastore):

```json
{
  "timestamp": "2026-02-21T10:05:00Z",
  "action": "message.rejected",
  "actor": "system",
  "target_user_id": null,
  "details": {
    "reason": "unknown_identity",
    "channel_type": "whatsapp",
    "channel_identity": "[REDACTED_JID]",
    "source": "webhook"
  }
}
```

**Observability event** (published to `nipper.logs` exchange):

```json
{
  "eventType": "message_rejected",
  "payload": {
    "reason": "not_in_allowlist",
    "channelType": "whatsapp",
    "resolvedUserId": "user-03",
    "channelIdentity": "[REDACTED_JID]"
  }
}
```

### Per-Channel Rejection Behavior

| Channel | Rejection Response | Why |
|---------|-------------------|-----|
| **WhatsApp** | Return HTTP 200 to Wuzapi webhook, do nothing else | Returning non-200 would cause Wuzapi to retry. No WhatsApp reply is sent — do not reveal system existence. |
| **Slack** | Return HTTP 200 to Slack Events API, do nothing else | Returning non-200 triggers Slack retries. No Slack message is sent back. |
| **MQTT** | Message consumed and discarded. No reply published. | MQTT message is ack'd at broker level to prevent redelivery. |
| **Cron** | Skipped (cron jobs reference userId directly, validated at config load) | Cron jobs are internal; if userId is invalid, the job is disabled at startup. |

### Why Silent Discard?

Sending error responses to unauthorized users leaks information:
- It confirms the system exists and is operational
- It reveals which identifiers are monitored
- It provides a feedback loop for attackers probing the system

The correct behavior is to act as if the message was never received. The only trace is the internal audit log, visible only to administrators.

## Error Handling

| Error                      | Gateway Action                                        |
|----------------------------|-------------------------------------------------------|
| Unknown channel identity   | Silently discard, log to audit + observability         |
| User disabled              | Silently discard, log to audit + observability         |
| User not in allowlist      | Silently discard, log to audit + observability         |
| Malformed message          | Reject, return error to channel                       |
| Invalid HMAC signature     | Reject with 401, log security event                   |
| Session not found          | N/A at gateway level — agent creates session on first message |
| Queue full                 | Return backpressure signal to channel                 |
| Agent offline (0 consumers)| Log degraded status, messages queue in RabbitMQ       |
| Agent timeout              | Return partial result + error to channel              |
| Channel delivery failure   | Retry with exponential backoff (3 attempts)           |
| Wuzapi unreachable         | Buffer outbound messages, retry on reconnect          |
| MQTT broker disconnect     | Auto-reconnect with backoff, buffer messages          |
| WebSocket disconnect       | Buffer events, replay on reconnect (30s window)       |
| Wuzapi returns error       | Log, retry once, then report failure to session       |
| Datastore unreachable      | Reject all messages (cannot verify users), log critical error     |
