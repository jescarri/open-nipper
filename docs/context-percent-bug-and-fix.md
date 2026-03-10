# Ctx % bug: understates real context fill (garbling risk)

## What you’re seeing

- **Context window:** 131072 (from `/props`).
- **Before compaction:** Session 92188 in / 2651 out, **Ctx: 11%**.
- **Reality:** 92188 / 131072 ≈ **70%** of the context window has been sent in this session. So you’re at **70%**, not 11%.
- **When garbling happened:** Completion in: 50427, 3 steps, **Ctx: 14%**. Session: 77142 in. So in that request we sent **50427** prompt tokens (50427/131072 ≈ **38%** of the window in one request), but we showed **14%**.

So **Ctx % is understating** how full the context is, which is misleading for predicting when you’ll hit the limit and get garbling.

---

## Root cause

**Ctx % is based only on `lastPromptTokens`** (the **last** LLM call’s prompt size in that request), not on:

1. **Session cumulative:** total prompt tokens sent in the session (TotalInputTokens).
2. **This request’s total:** sum of prompt tokens for **all** ReAct steps in this request (accumPrompt).

Where it’s set:

- **usage.go** `Record()` (lines 84–89):  
  `LastPromptTokens = lastPromptTokens`  
  `LastUsagePercent = lastPromptTokens / contextWindowMax * 100`
- **usage.go** `FormatResponseFooter()` (171–173):  
  `pct := lastPromptTokens / contextWindowMax * 100` → **Completion** line “Ctx: X%”.
- **usage.go** `FormatSessionUsageLine()` (191):  
  uses `usage.LastUsagePercent` → **Session** line “Ctx: X%”.

So both footer lines show “last step’s prompt as % of window”, not “session fill” or “this request’s total prompt”.

Consequences:

- **Single-step request:** lastPromptTokens ≈ accumPrompt, so Ctx % is close to “this request’s prompt %” but still says nothing about session total (e.g. 92188 → 70%).
- **Multi-step request:** lastPromptTokens can be much smaller than accumPrompt (e.g. last step 18k → 14%, but total request 50k → 38%), so Ctx % understates how much we actually sent in that request.

So the number that would have warned you (“you’re at 70%” / “this request used 38%”) is never shown.

---

## Proposed fix (choose one; no code until you approve)

### Option A – Session fill as main Ctx (recommended for “am I near the limit?”)

- **Session line:** Keep “Session: X in / Y out”. For **Ctx**, show **session fill**:  
  `TotalInputTokens / contextWindowMax * 100`  
  (capped at 100%). So you see e.g. “Session: 92188 in / 2651 out · Ctx: 70%”.
- **Completion line:** For **Ctx**, show **this request’s prompt** as % of window:  
  `accumPrompt / contextWindowMax * 100`  
  So e.g. “in: 50427 … · Ctx: 38%” when that request really sent 50k.
- **Compaction** already uses `lastPromptTokens` (previous request’s last step) for the threshold; that logic can stay as-is for triggering compaction. Only the **displayed** Ctx % changes.

Result: “Ctx” consistently means “how much of the context window this number represents” (session total on Session line, this request’s total on Completion line). No more “11%” when you’re actually at 70%.

### Option B – Show both “last call” and “session”

- **Session line:** e.g. “Session: 92188 in / 2651 out · Ctx: 70% (session)” and keep or add “11% (last call)” if desired.
- **Completion line:** e.g. “Ctx: 38% (this request)” and optionally “14% (last step)”.

More info, more clutter. Same idea as A for the main number.

### Option C – Only fix Session line

- **Session line:** Ctx = session fill (TotalInputTokens / contextWindowMax), so “Ctx: 70%”.
- **Completion line:** Leave as today (lastPromptTokens), so still “Ctx: 11%” / “14%”.

Improves the “how full is my session” signal; Completion line stays “last step” for those who care.

---

## Recommendation

**Option A:** Use **session fill** for the Session line Ctx %, and **this request’s total prompt** (accumPrompt) for the Completion line Ctx %. That way:

- Before compaction you see **Ctx: 70%** (session), which matches 92188/131072.
- When garbling happens you see **Ctx: 38%** on the Completion line (50427/131072), which matches the “in: 50427” for that request.

Internal compaction can continue to use `lastPromptTokens`; only the **displayed** percentages change so they reflect “how full is the context” in a way that matches the numbers you see (Session in, Completion in).

---

**Status:** Analysis and proposal only. No code changes until you approve which option (A, B, or C) you want.
