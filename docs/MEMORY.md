# Memory Architecture

## Overview

Open-Nipper's memory system is directly adapted from OpenClaw's in-file memory management. The system uses three tiers of memory — **context window** (ephemeral), **durable memory files** (persistent), and **session transcripts** (append-only log) — to give agents the ability to remember across conversations while respecting token limits.

The fundamental tension: AI models have finite context windows, but conversations can be arbitrarily long. Memory management is the art of deciding what to keep, what to summarize, and what to forget.

## Memory Tiers

```
┌──────────────────────────────────────────────────────┐
│                  TIER 1: CONTEXT WINDOW               │
│                                                       │
│  Active working memory. Everything the model can      │
│  "see" right now. Limited by model's token cap.       │
│                                                       │
│  Size: 4K–200K tokens (model-dependent)               │
│  Lifetime: Current request only                       │
│  Format: System prompt + conversation messages        │
│                                                       │
│  ┌─────────────────────────────────────────────────┐  │
│  │ System prompt (skills, bootstrap, tools)        │  │
│  │ Durable memory injection (recent memories)      │  │
│  │ Conversation history (last N turns)             │  │
│  │ Current user message                            │  │
│  └─────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│              TIER 2: DURABLE MEMORY FILES             │
│                                                       │
│  Persistent memory that survives compaction.          │
│  Written by the agent, read at prompt construction.   │
│                                                       │
│  Size: Unbounded (but only recent files injected)     │
│  Lifetime: Permanent (until manually deleted)         │
│  Format: Markdown files organized by date             │
│                                                       │
│  Location: ~/.open-nipper/users/{userId}/memory/      │
│  Files: YYYY-MM-DD.md (one per day)                   │
│                                                       │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│              TIER 3: SESSION TRANSCRIPTS               │
│                                                       │
│  Complete conversation log. Source of truth.           │
│  Never modified in place, only appended or compacted. │
│                                                       │
│  Size: Unbounded (grows with conversation)            │
│  Lifetime: Until session archived/deleted             │
│  Format: JSONL (one JSON object per line)             │
│                                                       │
│  Location: ~/.open-nipper/users/{userId}/sessions/    │
│  Files: {sessionId}.jsonl                             │
│                                                       │
└──────────────────────────────────────────────────────┘
```

## Tier 1: Context Window Management

### Token Tracking

Every message in the conversation carries a token count:

```jsonl
{"role":"user","content":"Deploy to staging","ts":"...","tokens":8}
{"role":"assistant","content":"I'll deploy...","ts":"...","tokens":150,"toolCalls":[...]}
{"role":"tool_result","toolCallId":"tc_1","content":"Deployed v2.3.1","ts":"...","tokens":45}
```

The session metadata tracks running totals:

```json
{
  "contextUsage": {
    "inputTokens": 38200,
    "outputTokens": 4650,
    "totalTokens": 42850,
    "contextWindowLimit": 200000,
    "usagePercent": 21.4,
    "freshCount": true
  }
}
```

`freshCount` indicates whether the token counts come from the actual API response (`true`) or are estimated locally (`false`). After compaction, counts are re-estimated until the next API call provides fresh numbers.

### Context Window Guard

Before every AI model call, the agent checks:

```
if (inputTokens > contextWindowLimit * 0.90) {
    triggerAutoCompaction();
}
```

This 90% threshold (the `compactionThreshold`) leaves headroom for the model's response. The guard runs **before** the API call, not after — preventing expensive failed requests.

### System Prompt Token Budget

The system prompt has a soft budget:

| Component              | Approximate Tokens | Notes                         |
|------------------------|-------------------|-------------------------------|
| Runtime info           | ~200              | OS, workspace, time           |
| Tool schemas           | ~2,000            | JSON schemas for all tools    |
| Skill descriptions     | ~500-5,000        | Depends on number of plugins  |
| Bootstrap context      | ~500-2,000        | User-defined                  |
| Durable memory inject  | ~500-2,000        | Last 7 days of memory files   |
| Channel capabilities   | ~100              | What the channel supports     |
| **Total**              | **~4,000-12,000** |                               |

The system prompt is effectively **always present** — it's never compacted. This is why the bootstrap file and skill descriptions should be concise: they consume tokens on every single request.

## Tier 2: Durable Memory Files

### What Is Durable Memory?

Durable memory is information the agent explicitly writes to survive beyond the current conversation's context window. When compaction removes old messages, the information in them is lost from the context window — but if the agent previously wrote key facts to durable memory, those facts persist.

This is adapted from OpenClaw's `memory-flush.ts` system.

### Memory File Format

```
~/.open-nipper/users/{userId}/memory/
├── 2026-02-19.md
├── 2026-02-20.md
└── 2026-02-21.md
```

Each file contains agent-written observations:

```markdown
# 2026-02-21

## Session: slack (01956a3c)

- User prefers blue-green deployments over rolling updates
- Production environment uses Kubernetes 1.29 on GKE
- The payments service has a known memory leak when processing > 1000 TPS
- Staging environment URL: https://staging.payments.internal

## Session: cron (daily-report)

- Server anomaly detected: disk usage on node-03 exceeded 90% at 08:45 UTC
- Recommended action: expand PVC or enable log rotation
```

### Memory Flush: Pre-Compaction Persistence

Adapted from OpenClaw's `shouldRunMemoryFlush()`:

```
Context window approaching threshold (85% full)
        │
        ▼
Should flush? Check:
  ├── Is this the first time hitting 85% since last compaction? (compactionCount check)
  ├── Is there enough room to run the flush prompt? (reserveTokensFloor: 4000)
  └── Has a flush already run this compaction cycle? (memoryFlushCompactionCount check)
        │
        ▼
If yes → Inject memory flush prompt:
  "Your context window is getting full. Before compaction removes old messages,
   please review the conversation and write any important facts, decisions,
   preferences, or context to durable memory using the memory_write tool.
   Focus on information that would be lost and would be valuable in future
   conversations."
        │
        ▼
Agent writes to /memory/YYYY-MM-DD.md
        │
        ▼
Compaction proceeds (old messages removed)
        │
        ▼
Next request: durable memory is injected into system prompt
  → Agent still "remembers" key facts even though messages are gone
```

### Memory Injection

At prompt construction time, recent durable memory files are loaded and injected:

```typescript
function buildMemoryContext(userId: string, lookbackDays: number = 7): string {
  const memoryDir = `~/.open-nipper/users/${userId}/memory/`;
  const files = listFiles(memoryDir)
    .filter(f => isWithinDays(f, lookbackDays))
    .sort(byDateDescending);

  if (files.length === 0) return "";

  const content = files.map(f => readFile(f)).join("\n\n");
  return `## Durable Memory (last ${lookbackDays} days)\n\n${content}`;
}
```

The injected memory becomes part of the system prompt, consuming tokens from the context window budget. If memory files are too large, only the most recent entries are included.

### Memory Write Tool

```typescript
interface MemoryWriteTool {
  name: "memory_write";
  params: {
    content: string;        // Markdown content to append
    category?: string;      // Optional category tag
  };
  execute: (params) => {
    const today = formatDate(new Date(), "YYYY-MM-DD");
    const file = `~/.open-nipper/users/${userId}/memory/${today}.md`;
    appendFile(file, `\n${params.content}\n`);
    return { ok: true, file, bytesWritten: params.content.length };
  };
}
```

### Memory Read Tool

```typescript
interface MemoryReadTool {
  name: "memory_read";
  params: {
    query?: string;         // Search term
    days?: number;          // Lookback period (default: 30)
  };
  execute: (params) => {
    const files = listMemoryFiles(userId, params.days);
    if (params.query) {
      return searchMemoryFiles(files, params.query);
    }
    return readRecentMemory(files);
  };
}
```

## Tier 3: Session Transcripts

### JSONL Format

Session transcripts use JSONL (JSON Lines) — one JSON object per line, append-only:

```jsonl
{"role":"system","content":"You are...","ts":"2026-02-21T10:00:00Z","tokens":1200,"seq":0}
{"role":"user","content":"Deploy the app","ts":"2026-02-21T10:00:05Z","tokens":8,"seq":1,"channelType":"slack"}
{"role":"assistant","content":"I'll deploy...","ts":"2026-02-21T10:00:07Z","tokens":150,"seq":2,"toolCalls":[{"id":"tc_1","name":"exec"}]}
{"role":"tool_result","toolCallId":"tc_1","content":"Deployed v2.3.1","ts":"2026-02-21T10:00:12Z","tokens":45,"seq":3}
```

### Why JSONL?

Matching OpenClaw's design choice:

1. **Append-only is crash-safe** — If the process crashes mid-write, only the last incomplete line is corrupted. All previous lines remain valid.
2. **Efficient tail reads** — Loading the last N lines doesn't require parsing the entire file. Seek to end, read backwards.
3. **Compaction without rewrite** — Compacted messages can be removed by rewriting only the affected portion.
4. **Line-based indexing** — Each line has a `seq` number. Line maps enable fast lookups.
5. **Streaming-friendly** — New messages are appended as they arrive; no need to parse and re-serialize the entire history.

### Transcript Operations

| Operation     | How It Works                                                    |
|---------------|-----------------------------------------------------------------|
| Append        | `appendFile(transcriptPath, JSON.stringify(message) + "\n")`    |
| Read history  | Read all lines, parse each as JSON, return array                |
| Read recent   | Read last N lines (seek from end)                               |
| Compact       | Score messages, remove low-priority, write new file, archive old |
| Search        | Grep through lines, parse matches                               |
| Token count   | Sum `tokens` field across all lines                             |

## Compaction Deep Dive

### When Compaction Triggers

```
inputTokens > compactionThreshold (90% of contextWindowLimit)
        │
        ▼
Pre-compaction:
  ├── Run memory flush (if not already done this cycle)
  └── Acquire session write lock
        │
        ▼
Compaction passes (progressive, from gentle to aggressive):

Pass 1: Keep last 20 turns, remove older
  ├── Fits? → Done
  └── Doesn't fit? → Pass 2

Pass 2: Keep last 10 turns, remove older
  ├── Fits? → Done
  └── Doesn't fit? → Pass 3

Pass 3: Keep last 5 turns + critical tool results
  ├── Fits? → Done
  └── Doesn't fit? → Pass 4

Pass 4: Truncate tool results (keep first 5KB + last 2KB, "[truncated]" middle)
  ├── Fits? → Done
  └── Doesn't fit? → Pass 5

Pass 5: Emergency — prompt user to /reset
```

### Compaction Scoring Algorithm

```typescript
function scoreMessage(msg: TranscriptLine, totalTurns: number, turnIndex: number): number {
  let score = 0;
  const recency = totalTurns - turnIndex;

  // Recency bonus
  if (recency <= 5)  score += 100;
  else if (recency <= 10) score += 50;
  else if (recency <= 20) score += 20;

  // Role bonus
  if (msg.role === "system")      score += 1000;  // Never remove
  if (msg.role === "user")        score += 10;
  if (msg.role === "tool_result") score += 15;
  if (msg.role === "assistant")   score += 5;

  // Size penalty (prefer removing large messages)
  if (msg.tokens > 500) score -= 5;

  return score;
}
```

### Orphan Repair

During compaction, if a `tool_use` message is removed but its `tool_result` remains (or vice versa), the orphan must also be removed:

```
Before compaction:
  [user] "check logs"              score: 25 → KEEP
  [assistant] tool_use: exec       score: 12 → REMOVE
  [tool_result] "no errors"        score: 18 → KEEP (orphan!)

After orphan repair:
  [user] "check logs"              KEEP
  [assistant] tool_use: exec       REMOVE
  [tool_result] "no errors"        REMOVE (orphaned, removed)
```

### Compaction Archive

Removed messages are not deleted — they are moved to an archive file:

```
~/.open-nipper/users/{userId}/sessions/
├── {sessionId}.jsonl              # Active transcript (compacted)
├── {sessionId}.archive.jsonl      # Archived messages (append-only)
└── {sessionId}.jsonl.lock
```

The archive is a forensic log. It's never loaded into the context window. It exists for debugging, auditing, and potential future retrieval.

## Memory Isolation

Each user's memory is completely isolated:

```
User A's memory: ~/.open-nipper/users/user-01/memory/
User B's memory: ~/.open-nipper/users/user-02/memory/
User C's memory: ~/.open-nipper/users/user-03/memory/
```

- Agent processes are scoped to only their user's memory directory.
- The `memory_write` tool writes to the user's memory path, which resolves to the correct user directory.
- The `memory_read` tool can only read from the same mounted path.
- No symlinks allowed in memory directories (validated at startup).

## Memory Search

For searching across durable memory files:

```typescript
interface MemorySearchResult {
  file: string;           // e.g., "2026-02-20.md"
  line: number;
  content: string;        // The matching line
  context: string;        // Surrounding lines for context
}

async function searchMemory(
  userId: string,
  query: string,
  options?: { days?: number; maxResults?: number }
): Promise<MemorySearchResult[]> {
  // Ripgrep through memory files
  // Scoped to user's memory directory
  // Returns ranked results
}
```

## Bootstrap File

The bootstrap file is a user-defined persistent context that is always included in the system prompt, regardless of compaction:

```
~/.open-nipper/users/{userId}/workspace/.nipper/bootstrap.md
```

Example:

```markdown
# Context

You are a DevOps assistant for the payments team.

## Important Context
- Our production cluster is on GKE (us-east1)
- We use ArgoCD for GitOps deployments
- The payments service is in the `payments-v2` namespace
- Always check `kubectl get pods` before deploying

## Preferences
- Use concise output from tools
- Always confirm before destructive operations
- Prefer blue-green over rolling deployments
```

The bootstrap file survives compaction, session reset, and model switches. It's the place for information that must always be available.

## Memory Best Practices

1. **Bootstrap for permanent context** — Things that are always true: team info, infrastructure layout, preferences.
2. **Durable memory for learned facts** — Things discovered during conversations: decisions made, issues found, user preferences discovered.
3. **Keep memory entries atomic** — One fact per bullet point. Easier to search, easier to inject selectively.
4. **Minimal tool output in bootstrap** — Tell the agent to output minimal data from tools. This saves context tokens and delays compaction (tip from the OpenClaw article).
5. **Date-organized memory** — One file per day makes it easy to age out old memories. Memory from 30+ days ago is probably less relevant.
6. **Memory flush is not backup** — It's a best-effort extraction of key facts before compaction. Not everything will be captured. The archive file is the complete record.

## CI/CD

### GitHub Actions

- **Workflow**: `.github/workflows/ci.yml`
- **Triggers**: Push to any branch, pull requests to any branch
- **Jobs**:
  - `test`: Runs `go vet`, `golangci-lint`, and unit tests with race detector
  - `build`: Builds Docker image after tests pass, pushes on main or tags

### Docker Image Tags

- **Branches**: `opennipper/open-nipper:<branch-name>`
- **Pull requests**: `opennipper/open-nipper:pr-<number>`
- **Tags**: `opennipper/open-nipper:<version>`, `opennipper/open-nipper:<major>.<minor>`, `opennipper/open-nipper:<major>`
- **Main branch**: `opennipper/open-nipper:latest`
- **Commit SHA**: `opennipper/open-nipper:<branch>-<sha>`

### Secrets Required

- `DOCKER_USERNAME`: Docker Hub username
- `DOCKER_PASSWORD`: Docker Hub access token
