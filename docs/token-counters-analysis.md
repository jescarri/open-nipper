# Token counters: how they are populated

## Where the numbers come from

### 1. Source of truth: LLM API response

Token counts are **not** estimated by the agent. They come from the inference server’s **usage** field on each LLM response (OpenAI‑compatible `usage.prompt_tokens`, `usage.completion_tokens`).

- **Path:** Server response → Eino/schema `Message.ResponseMeta.Usage` (or callback `output.TokenUsage`) → **model callback OnEnd** in `runtime.go` (around 1424–1479) → `accum.Add(usage.PromptTokens, usage.CompletionTokens)`.
- So if the server (e.g. llama.cpp) returns wrong or partial usage, the agent’s numbers will match that.

### 2. Per-request accumulator (`requestTokenAccumulator`)

For **one** user message, the ReAct agent can make **multiple** LLM calls (e.g. prompt → tool → prompt → tool → final prompt). Each call’s usage is passed to the model callback and added to a **per-request** accumulator:

- **`usage.go`:** `requestTokenAccumulator` has:
  - `promptTokens` = **sum** of prompt tokens for all LLM calls in this request
  - `completionTokens` = **sum** of completion tokens for all LLM calls
  - `steps` = number of LLM calls
  - `lastPromptTokens` = prompt tokens of the **last** LLM call only (used for context % and compaction)

So for a single user message, “in” and “out” in the **Completion** line are the **totals across all ReAct steps** for that message; **Ctx %** is based only on the **last** step’s prompt size.

### 3. Session tracker (`UsageTracker`)

After each request, the agent calls:

```text
usageTracker.Record(sessionKey, accumPrompt, accumCompletion, accumLastPrompt)
```

- **`usage.go` `Record()`:**
  - `TotalInputTokens`  += **accumPrompt**   (running sum over all requests in the session)
  - `TotalOutputTokens` += **accumCompletion**
  - `LastPromptTokens`   = **accumLastPrompt** (overwritten each request)
  - `LastUsagePercent`   = lastPromptTokens / contextWindowMax * 100

So **Session** is **cumulative over the whole session**; it does **not** decrease when you compact. Compaction only reduces the **next** request’s prompt size (fewer history lines). The **current** request’s usage is still added to the session total.

Session totals are **reset** only when the session is reset (e.g. `/new`), via `usageTracker.Reset(sessionKey)` in `commands.go`.

---

## What each line in the footer means

### Completion line (every response when skill level > intermediate)

```text
Completion: 8.88 s · Tokens: 15167 (in: 15046, out: 121) · 14 tok/s · Ctx: 11%
```

| Part | Meaning |
|------|--------|
| **Completion: 8.88 s** | Wall time for this single user message (all ReAct steps). |
| **Tokens: 15167** | Total tokens this request: **in + out** (15046 + 121). |
| **in: 15046** | Sum of **prompt** tokens for every LLM call in this request (all steps). |
| **out: 121** | Sum of **completion** tokens for every LLM call in this request. |
| **Ctx: 11%** | **Last** LLM call’s prompt tokens / **context window size** (e.g. 131072). So ~11% of the context window was used by the final prompt. |

So “in” here is **not** “tokens in the whole session”; it’s “tokens sent to the model in **this** request (all steps)”. Ctx % is about the **last** call’s prompt only.

### Session line (only when skill level = expert)

```text
Session: 92188 in / 2651 out · Ctx: 11%
```

| Part | Meaning |
|------|--------|
| **Session: 92188 in** | **Cumulative** prompt tokens for **all** requests in this session so far. |
| **2651 out** | **Cumulative** completion tokens for the session. |
| **Ctx: 11%** | Same as in the Completion line: last request’s last prompt size / context window (from the same `SessionUsage`). |

So after the next message you get something like:

- **Session: 107278 in / 3254 out** = 92188 + 15090 and 2651 + 603 (current request’s in/out added to the previous session totals).

Session **in** and **out** only go up (or to zero after `/new`). Compaction does **not** subtract from them.

---

## Why numbers can look “wrong”

1. **Session keeps growing after compaction**  
   Compaction archives old transcript lines so the **next** prompt is smaller. It does **not** change the fact that the **current** request already used e.g. 15090 prompt tokens; that 15090 is still added to `TotalInputTokens`. So Session reflects “total tokens ever sent in this session,” not “tokens in the current context window.”

2. **“in” didn’t drop after compaction**  
   The footer you see is for the request that **triggered** compaction (or the one right after). Compaction runs **before** building the prompt for that message. So:
   - If you see “Context compacted” and “in: 15090”, that 15090 is the prompt size **after** compacting (e.g. system prompt + tools + 20 kept lines + your new message). With only 2 messages archived, the prompt can still be large; the **next** request might have a smaller “in” if the history is now shorter.

3. **Ctx % vs “in”**  
   Ctx % uses **lastPromptTokens** (last LLM call in the request). “in” in the Completion line is the **sum** over all calls. So in a multi-step turn, “in” can be much larger than what Ctx % suggests (e.g. 15k total in, but last call only 2k → Ctx 1.5%). If there is only one step, last prompt ≈ total “in” and Ctx % will match.

4. **Server-reported usage**  
   If the inference server (e.g. llama.cpp) under- or over-reports `prompt_tokens` / `completion_tokens`, or counts reasoning/thinking differently, the agent’s numbers will follow that. The agent does not recompute tokens; it trusts the API usage.

---

## Your config (no code change)

You have:

```yaml
prompt:
  auto_compaction_threshold_percent: 5
  compaction_level: "full"
```

- **auto_compaction_threshold_percent: 5**  
  Compaction runs when **lastPromptTokens** (from the **previous** request) is **> 5%** of the context window. With context 131072, that’s ~6554 tokens. So compaction can trigger often; “Context compacted. Archived 2 messages” means the previous request had last prompt > 5% and the compactor then archived 2 transcript lines.

- **compaction_level**  
  Affects **how** the transcript is summarized when compacting (e.g. “full” vs “compact”); it does **not** change how token counts or Session totals are computed.

---

## Summary

| Metric | Scope | How it’s populated |
|--------|--------|-------------------|
| **Completion “in” / “out”** | This request (all ReAct steps) | Sum of each LLM call’s `usage.prompt_tokens` / `usage.completion_tokens` from the server. |
| **Ctx %** | Last LLM call in this request | `lastPromptTokens / contextWindowMax * 100`; `lastPromptTokens` from server usage of the last call. |
| **Session “in” / “out”** | Whole session (cumulative) | After each request: `TotalInputTokens += accumPrompt`, `TotalOutputTokens += accumCompletion`. Reset only on `/new`. |
| **Session Ctx %** | Same as Completion Ctx % | Same `LastUsagePercent` from the last request. |

So the behaviour you see (Session going up after compaction, “in” not dropping in that same message) is consistent with the current design: **Completion** = this request only; **Session** = lifetime of the session; **compaction** only reduces future prompt size and does not alter past or cumulative counts.

No code has been changed; this is analysis only. If you want different semantics (e.g. Session reflecting “current context size” or resetting on compaction), that would require a design/approval step.
