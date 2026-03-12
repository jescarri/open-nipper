# Proposal: Use `/props` for model metadata discovery

## Goal

Use the llama.cpp / identitylabs **`/props`** endpoint as the **first** source for model metadata when available. `/props` exposes the **actual runtime** context size (`default_generation_settings.n_ctx`), which can be smaller than the model’s training context exposed by `/models` (e.g. `meta.n_ctx_train`). Using the real `n_ctx` avoids sending prompts that exceed the server’s KV cache and prevents garbled output.

## Current behaviour

- **Probe order:** `GET {base_url}/models/{model_id}` → then `GET {base_url}/models`.
- **Context size:** Taken from `max_context_length`, `max_model_len`, or `meta.n_ctx_train`. These can be larger than the **runtime** context when the server uses `-c 0` and `--fit on`.
- **Result:** Agent may believe the context window is e.g. 262144 while the server actually uses 131072 → no compaction in time → prompt exceeds real limit → garbled output.

## Proposed behaviour

- **Probe order:**  
  1. **`GET {origin}/props`** (see URL derivation below).  
  2. If that fails (4xx/5xx, parse error, or missing `n_ctx`) → fall back to existing order: **`GET {base_url}/models/{model_id}`** then **`GET {base_url}/models`**.
- **When `/props` succeeds:**  
  - **MaxContextLength** ← `default_generation_settings.n_ctx` (actual runtime context).  
  - **ID** ← `model_alias` (or basename of `model_path` if `model_alias` is empty).  
  - **Source** ← `"GET /props"`.  
  - **State / Architecture / Quantization:** leave empty (props does not expose them in the current shape).
- **URL for `/props`:**  
  - Derive **origin** from `base_url`: parse as URL and use `Scheme + "://" + Host` (e.g. `https://llm.identitylabs.mx/v1` → `https://llm.identitylabs.mx`).  
  - **Props URL** = `origin + "/props"` (e.g. `https://llm.identitylabs.mx/props`).  
  - Rationale: `/props` is a llama-server root endpoint; OpenAI-style APIs live under `/v1`, so we must not use `base_url` + "/props" (that would be `/v1/props`).

## API shape used from `/props`

Minimal struct we rely on (other fields ignored):

```json
{
  "default_generation_settings": {
    "n_ctx": 131072
  },
  "model_alias": "gpt-oss-120b-F16.gguf",
  "model_path": "/models/gpt-oss-120b-F16.gguf"
}
```

- **n_ctx** is required for “props success” for discovery; if 0 or missing we fall back to OpenAI-style endpoints.
- **model_alias** preferred for `ModelCapabilities.ID`; if empty, use filepath.Base(**model_path**).

## Model matching (optional)

- We do **not** require `cfg.Model == model_alias` to use props. If `/props` returns 200 and `n_ctx > 0`, we use it. The important part is the **actual** context size; the loaded model name can be logged for clarity when it differs from config.

## Files to change

| File | Change |
|------|--------|
| **internal/agent/llm/probe.go** | 1) Add `propsOriginFromBaseURL(baseURL string) (string, error)` using `net/url` to derive origin. 2) Add `propsResponse` struct and `probeProps(ctx, client, propsURL, apiKey)` that GETs `/props`, decodes JSON, and returns `*ModelCapabilities` when `default_generation_settings.n_ctx > 0`. 3) In `ProbeModelCapabilities`, try `probeProps` first; on success return its result; on failure fall back to current `probeModelEndpoint` then `probeModelsList`. 4) Doc comment: update probe order to list `/props` first. |
| **internal/agent/llm/probe_test.go** | 1) Add **TestProbeModelCapabilities_PropsEndpoint**: server that responds to `GET /props` with `n_ctx: 131072` and `model_alias: "gpt-oss-120b-F16.gguf"`; assert `MaxContextLength == 131072`, `ID` matches, `Source == "GET /props"`. 2) Add **TestProbeModelCapabilities_PropsFallback**: server that returns 404 for `/props` and valid response for `/v1/models`; assert probe falls back and returns capabilities from `/models`. 3) Optionally **TestProbeModelCapabilities_PropsOrigin**: unit test for `propsOriginFromBaseURL` (e.g. `https://llm.identitylabs.mx/v1` → `https://llm.identitylabs.mx`). |

## Behaviour summary

- **llama.cpp / identitylabs** (with `/props`): Agent gets **actual** `n_ctx` (e.g. 131072) → compaction and usage % based on real limit → no overflow, no garbled output.
- **LM Studio / vLLM / others** (no `/props` or 404): Unchanged; probe uses `/models/{id}` or `/models` as today.
- **No code changes** in `cli/agent.go` or config types; they already use `probedCap.MaxContextLength` and `probedCap.Source`.

## Edge cases

- **base_url with path:** `https://host/path/v1` → origin `https://host` → props `https://host/props`. We use URL.Host, so only scheme+host, no path.
- **props returns n_ctx 0:** Treat as “no usable props” and fall back to OpenAI-style endpoints.
- **props parse error or non-JSON:** Same; fall back.

---

**Status:** Proposal only; implementation will follow after your approval.
