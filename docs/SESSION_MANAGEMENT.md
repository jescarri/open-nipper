# Session Management Architecture

## Overview

Open-Nipper uses a **session-per-user** model inspired by OpenClaw's `SessionManager`. Every conversation is bound to exactly one user and one session. Sessions are the fundamental unit of isolation: User A's session can never read, write, or reference User B's data. The system supports 1-3 concurrent users, each with their own workspace, transcript history, and context window.

## Core Concepts

### Session Identity

Every session is identified by a composite key:

```
session_key := "user:{userId}:channel:{channelType}:session:{sessionId}"
```

| Field         | Type   | Description                                              |
|---------------|--------|----------------------------------------------------------|
| `userId`      | UUIDv7 | Immutable user identifier, assigned at onboarding        |
| `channelType` | enum   | `whatsapp`, `slack`, `cron`, `mqtt`, `rabbitmq` (see `GATEWAY_ARCHITECTURE.md` `ChannelType`) |
| `sessionId`   | UUIDv7 | Unique per conversation, rotated on `/reset`             |

A single user may have multiple sessions (one per channel, or multiple within a channel via threads). The `session_key` ensures no two users can collide.

### Session Lifecycle

```
┌──────────┐     ┌──────────┐     ┌───────────┐     ┌───────────┐
│  CREATE   │────▶│  ACTIVE   │────▶│ COMPACTED │────▶│ ARCHIVED  │
└──────────┘     └──────────┘     └───────────┘     └───────────┘
                       │                                    │
                       │         ┌───────────┐              │
                       └────────▶│   RESET    │─────────────┘
                                 └───────────┘
```

**CREATE** - A new session is allocated when a user sends their first message on a channel, or explicitly via the API. A fresh workspace directory, transcript file, and session entry are created atomically.

**ACTIVE** - The session is processing messages. Transcript grows, context window fills.

**COMPACTED** - The session's transcript has been compacted (old messages removed, summary retained). The session remains active; this state is tracked for metrics.

**ARCHIVED** - The session is closed. Transcript is moved to cold storage. No further messages accepted.

**RESET** - User issues `/reset`. A new `sessionId` is generated, the old transcript is archived, and the session restarts with a clean context window. The `userId` remains the same.

## Session Store

### Storage Layout

```
~/.open-nipper/
├── users/
│   ├── {userId}/
│   │   ├── profile.json            # User profile and preferences
│   │   ├── sessions/
│   │   │   ├── sessions.json       # Session index (all sessions for this user)
│   │   │   ├── {sessionId}.jsonl   # Transcript (append-only JSONL)
│   │   │   ├── {sessionId}.jsonl.lock  # File lock
│   │   │   └── {sessionId}.meta.json   # Session metadata
│   │   ├── memory/
│   │   │   └── YYYY-MM-DD.md       # Durable memory files
│   │   └── workspace/
│   │       └── ...                  # Agent working directory
```

Each user gets a completely separate directory tree. There is no shared state between users at the filesystem level.

### Session Index (`sessions.json`)

```json
{
  "userId": "01956a3b-...",
  "sessions": [
    {
      "sessionId": "01956a3c-...",
      "sessionKey": "user:01956a3b:channel:slack:session:01956a3c",
      "channelType": "slack",
      "createdAt": "2026-02-21T10:00:00Z",
      "updatedAt": "2026-02-21T14:30:00Z",
      "status": "active",
      "totalTokens": 42850,
      "contextTokens": 38200,
      "contextWindowLimit": 200000,
      "compactionCount": 0,
      "messageCount": 47,
      "model": "claude-sonnet-4-20250514"
    }
  ]
}
```

The index is cached in memory with a 45-second TTL (following OpenClaw's pattern from `src/config/sessions/store.ts`). Writes are atomic: write to a temp file, then rename.

### Session Metadata (`{sessionId}.meta.json`)

```json
{
  "sessionId": "01956a3c-...",
  "userId": "01956a3b-...",
  "channelType": "slack",
  "channelMeta": {
    "type": "slack",
    "teamId": "T0123",
    "channelId": "C0456",
    "slackUserId": "U0123ABC",
    "threadTs": "1708512000.000100",
    "appId": "A0789",
    "botToken": "${SLACK_BOT_TOKEN}"
  },
  "model": "claude-sonnet-4-20250514",
  "providerOverride": null,
  "thinkingLevel": "normal",
  "skillSnapshot": ["deploy", "search-docs"],
  "createdAt": "2026-02-21T10:00:00Z",
  "lastActivityAt": "2026-02-21T14:30:00Z",
  "contextUsage": {
    "inputTokens": 38200,
    "outputTokens": 4650,
    "totalTokens": 42850,
    "contextWindowLimit": 200000,
    "usagePercent": 21.4,
    "compactionThreshold": 180000,
    "freshCount": true
  },
  "compactionCount": 0,
  "memoryFlushCount": 0
}
```

### Transcript File (`{sessionId}.jsonl`)

Append-only JSONL format, one JSON object per line:

```jsonl
{"role":"system","content":"You are...","ts":"2026-02-21T10:00:00Z","tokens":1200}
{"role":"user","content":"Deploy the app","ts":"2026-02-21T10:00:05Z","tokens":8,"channelType":"whatsapp","userId":"user-01"}
{"role":"assistant","content":"I'll deploy...","ts":"2026-02-21T10:00:07Z","tokens":150,"toolCalls":[{"id":"tc_1","name":"exec","params":{"cmd":"deploy.sh"}}]}
{"role":"tool_result","toolCallId":"tc_1","content":"Deployed v2.3.1","ts":"2026-02-21T10:00:12Z","tokens":45}
```

JSONL was chosen over monolithic JSON for the same reason OpenClaw uses it: append-only writes are crash-safe, and line-based formats allow efficient tail reads and compaction without rewriting the entire file.

## Session Locking

### File-Based Locks

Adapted from OpenClaw's `src/agents/session-write-lock.ts`:

```
Lock acquisition:
  1. Attempt to create {sessionId}.jsonl.lock with O_CREAT|O_EXCL
  2. Write lock metadata: { pid, acquiredAt, userId, operation }
  3. If file exists, read it and check staleness
  4. If stale (> 30 minutes), override the lock
  5. If not stale, wait with exponential backoff (max 10 seconds)

Lock release:
  1. Verify lock is owned by current process (PID match)
  2. Remove lock file
  3. If process crashes, watchdog cleans stale locks every 60 seconds
```

### Lock Guarantees

| Property          | Guarantee                                                  |
|-------------------|------------------------------------------------------------|
| Mutual exclusion  | Only one writer per session transcript at any time         |
| Stale detection   | Abandoned locks auto-expire after 30 minutes               |
| Reentrant         | Same PID can re-acquire its own lock                       |
| Max hold time     | 5 minutes; watchdog forcibly releases after this           |
| User isolation    | Locks are scoped to user directory; cross-user lock impossible |

### Why Both Queues AND Locks?

The queue system (see `QUEUE_ARCHITECTURE.md`) prevents concurrent execution at the logical level. File locks provide a **defense-in-depth** safety net:

- If a bug in the queue allows two messages through simultaneously, the lock prevents transcript corruption.
- If messages arrive from different channels for the same session, the lock serializes writes.
- If a process crashes mid-write, the lock prevents a subsequent process from reading a partial write.

## Session Creation

### Flow

```
User sends first message on channel
        │
        ▼
Gateway resolves session_key
        │
        ▼
Session exists? ──YES──▶ Load session, enqueue message
        │
        NO
        │
        ▼
Create user directory (if first session for user)
        │
        ▼
Generate sessionId (UUIDv7)
        │
        ▼
Create transcript file ({sessionId}.jsonl)
        │
        ▼
Write system prompt as first line
        │
        ▼
Create metadata file ({sessionId}.meta.json)
        │
        ▼
Update session index (sessions.json)
        │
        ▼
Initialize context tracking:
  contextTokens = system_prompt_tokens
  contextWindowLimit = model_default
        │
        ▼
Enqueue message for processing
```

### API: Create Session Explicitly

```json
{
  "method": "sessions.create",
  "params": {
    "userId": "01956a3b-...",
    "channelType": "whatsapp",
    "channelMeta": {
      "type": "whatsapp",
      "wuzapiUserId": "1",
      "wuzapiInstanceName": "nipper-wa",
      "wuzapiBaseUrl": "http://localhost:8080",
      "chatJid": "5491155553934@s.whatsapp.net",
      "senderJid": "5491155553935@s.whatsapp.net"
    },
    "model": "claude-sonnet-4-20250514",
    "skills": ["deploy", "search-docs"],
    "bootstrap": "You are a DevOps assistant for the payments team."
  }
}
```

Response:

```json
{
  "ok": true,
  "result": {
    "sessionId": "01956a3c-...",
    "sessionKey": "user:01956a3b:channel:whatsapp:session:01956a3c",
    "contextUsage": {
      "inputTokens": 1200,
      "outputTokens": 0,
      "totalTokens": 1200,
      "contextWindowLimit": 200000,
      "usagePercent": 0.6
    }
  }
}
```

## Context Usage Reporting

Every response from the system includes a `contextUsage` block. This is non-negotiable: the user and the system must always know how full the context window is.

### Context Usage Object

```json
{
  "contextUsage": {
    "inputTokens": 38200,
    "outputTokens": 4650,
    "totalTokens": 42850,
    "contextWindowLimit": 200000,
    "usagePercent": 21.4,
    "compactionThreshold": 180000,
    "remainingTokens": 157150,
    "freshCount": true,
    "lastCompactedAt": null,
    "compactionCount": 0
  }
}
```

| Field                 | Description                                                    |
|-----------------------|----------------------------------------------------------------|
| `inputTokens`        | Tokens consumed by conversation history + system prompt        |
| `outputTokens`       | Tokens generated by the model in this session                  |
| `totalTokens`        | `inputTokens + outputTokens`                                   |
| `contextWindowLimit`  | Maximum tokens for the current model                           |
| `usagePercent`        | `(inputTokens / contextWindowLimit) * 100`                     |
| `compactionThreshold` | Token count that triggers auto-compaction (90% of limit)       |
| `remainingTokens`     | `contextWindowLimit - inputTokens`                             |
| `freshCount`          | Whether counts are from actual API response (not estimated)    |
| `lastCompactedAt`     | Timestamp of last compaction, or null                          |
| `compactionCount`     | Number of times this session has been compacted                |

### When Context Usage Is Reported

- After every assistant response
- After compaction
- After session creation
- On `sessions.info` API call
- In the session index (summary form)

### Context Usage Alerts

| Usage Level | Threshold | Action                                                    |
|-------------|-----------|-----------------------------------------------------------|
| Normal      | 0-70%     | No action, report in metadata                             |
| Warning     | 70-85%    | Include warning in response metadata                      |
| Critical    | 85-90%    | Trigger memory flush (see `MEMORY.md`)                    |
| Overflow    | >90%      | Trigger auto-compaction                                   |
| Emergency   | Compaction fails | Prompt user to `/reset` or switch to larger model  |

## Compaction

### Strategy

Adapted from OpenClaw's progressive compaction (PDF, "Compaction: Keeping Conversations Manageable"):

```
Compaction is triggered when inputTokens > compactionThreshold (90% of limit)

Pass 1 — Gentle (keep last 20 turns)
  Remove messages older than 20 turns
  Preserve: all tool_use/tool_result pairs, user messages, recent assistant messages
  If fits → done

Pass 2 — Moderate (keep last 10 turns)
  More aggressive pruning
  Preserve: tool_result content, last 10 user messages
  If fits → done

Pass 3 — Aggressive (keep last 5 turns)
  Keep only 5 most recent turns + critical tool results
  If fits → done

Pass 4 — Truncation
  Truncate individual tool results:
    Keep first 5KB + last 2KB of each tool output
    Replace middle with "[truncated]"
  If fits → done

Pass 5 — Emergency
  Ask user to /reset
```

### Compaction Scoring

Messages are scored for retention priority:

| Factor          | Score Modifier |
|-----------------|----------------|
| Last 5 turns    | +100           |
| Last 10 turns   | +50            |
| Last 20 turns   | +20            |
| User message    | +10            |
| Tool result     | +15            |
| Assistant msg   | +5             |
| Short message   | +3             |
| System prompt   | +1000 (never remove) |

Lower-scoring messages are removed first. Tool_use and tool_result pairs are always removed together to prevent orphaned results.

### Compaction Process

```
1. Acquire session write lock
2. Read full transcript
3. Score all messages
4. Remove lowest-scoring messages until under threshold
5. Repair orphaned tool results (tool_result without matching tool_use)
6. Write compacted transcript to temp file
7. Archive removed messages to {sessionId}.archive.jsonl
8. Atomic rename: temp → transcript
9. Update session metadata (compactionCount++, token counts)
10. Release lock
11. Report new contextUsage
```

## Session Commands

| Command     | Description                          | Effect                                      |
|-------------|--------------------------------------|---------------------------------------------|
| `/reset`    | Clear conversation history           | Archive transcript, new sessionId, fresh context |
| `/compact`  | Manually trigger compaction          | Run compaction passes, report new usage      |
| `/status`   | Show session info                    | Return contextUsage + session metadata       |
| `/model`    | Switch model                         | Update session model, recalculate context limit |
| `/sessions` | List all sessions for current user   | Return session index                         |

## Multi-User Isolation Guarantees

| Boundary                | Enforcement                                                    |
|-------------------------|----------------------------------------------------------------|
| Filesystem              | Separate directory trees per userId, no symlinks allowed       |
| Session keys            | userId embedded in key, validated on every operation           |
| Locks                   | Scoped to user directory, cannot reference other user paths    |
| API authorization       | Every API call validated against authenticated userId          |
| Agent process scoping   | Agent process configured to access only its assigned user's directories |
| Memory                  | Durable memory files stored in user-scoped directory           |
| Context                 | No cross-session context sharing between users                 |

With 1-3 users, the system can keep all session indexes in memory (tiny footprint). The filesystem layout is the source of truth; the in-memory cache is a performance optimization, not a requirement.
