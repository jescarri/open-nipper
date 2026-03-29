# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.1.0] - 2026-03-27

### Added

- **Embedding-based tool matching** (`agent.embeddings` config section)
  - `EmbeddingToolMatcher` uses cosine similarity against tool description embeddings for semantic and multilingual matching.
  - `HybridToolMatcher` composes embedding + keyword matchers using reciprocal rank fusion (configurable `hybrid_alpha` blend weight).
  - `OpenAIEmbedder` calls any OpenAI-compatible `/v1/embeddings` endpoint (Ollama, LocalAI, llama.cpp, vLLM, OpenAI).
  - Catalog vectors cached with FNV fingerprint detection — only re-embedded when the tool catalog changes.
  - Graceful degradation: if the embedding server is unavailable at startup or fails at runtime, falls back to keyword matching.
  - New config fields: `embeddings.enabled`, `base_url`, `model`, `api_key`, `similarity_threshold`, `hybrid_alpha`.
  - New telemetry metrics: `nipper_agent_embedding_requests_total`, `nipper_agent_embedding_duration_seconds`, `nipper_agent_tool_match_duration_seconds`.
  - `WithToolMatcher` runtime option for pluggable matcher injection.

- **Embedding accuracy test tool** (`cmd/embedding-test`)
  - CLI tool for validating embedding accuracy against a YAML-defined tool catalog and test suite.
  - Two modes: batch test suite (CI-friendly, exits 1 on failure) and single query (interactive exploration).
  - Loads catalog and test cases from YAML files — no recompilation needed to add tools or tests.
  - Reports cosine similarity scores, expected/missed/false-positive status per test case.
  - See `docs/EMBEDDING_TEST.md` for usage and examples.

## [2.0.1] - 2026-03-22

### Fixed

- **Timezone support in Docker image**: Added `tzdata` package to Alpine image so the `TZ` environment variable is respected. Without it, `get_datetime` and all time-based tools always returned UTC regardless of the configured timezone.

## [2.0.0] - 2026-03-22

### Added

- **Lean MCP Tools** (`inference.lean_mcp_tools` config flag)
  - On-demand MCP tool loading that reduces tool schema tokens by ~65%.
  - Keyword matching on user messages resolves only the MCP tools needed per request.
  - Skills declare `mcp_tools` in config.yaml — when a skill activates, its MCP dependencies are bound automatically.
  - Fallback: when no tools match, only native tools are bound (zero MCP overhead).
  - `ToolMatcher` interface for pluggable matching (keyword now, embedding-ready for future).
  - `search_tools` native tool for LLM-driven tool discovery (fallback path).
  - `ToolsByNames()` method on MCP Loader for subset tool resolution.

- **Skill config-driven keywords**
  - Skill activation keywords moved from hardcoded Go map to `keywords` field in each skill's `config.yaml`.
  - Adding a new skill no longer requires code changes.
  - `ActivationKeywords()` method on Skill type.

- **Skill config `mcp_tools` field**
  - Skills declare which MCP tools they need (e.g. `mcp_tools: [GetLiveContext, HassTurnOn]`).
  - Used by lean mode to bind the right tools when a skill is activated.
  - `MCPToolNames()` method on Skill type.

- **Skill config `prompt_hint` field**
  - Compact LLM-facing summary used instead of full SKILL.md body.
  - Reduces skill prompt injection from ~2,500 chars to ~400 chars per skill.

- **Sandbox `extra_capabilities` config**
  - New `sandbox.extra_capabilities` field (e.g. `["NET_RAW"]`) to grant additional Linux capabilities.
  - Enables nmap raw socket scans, ping sweeps, and OS detection inside the sandbox.

- **WhatsApp message chunking**
  - Long messages are split into ~4KB chunks at newline boundaries before delivery.
  - Fixes Wuzapi 500 errors on large tool outputs (e.g. network scan reports).

- **Control character stripping**
  - Strips ASCII control chars (0x00–0x1F except \n, \t) from user messages.
  - Prevents stray bytes (e.g. ETX 0x03 from WhatsApp) from confusing LLM tokenizers.

- **Tool call JSON sanitization**
  - `sanitizeToolCallJSON()` in toolwrap.go fixes escaped quotes and trailing special tokens from GPT-OSS.
  - Prevents infinite retry loops on malformed tool call JSON.

- **LLM call timing metrics**
  - TTFT (time to first token), generation duration, and phase timing logged per LLM call.

- **Full prompt debug dump**
  - Complete system prompt + tool schemas + messages logged at DEBUG level for prompt engineering.

- **MCP non-blocking startup**
  - SSE/Streamable MCP connections no longer block agent startup.
  - Background reconnection with exponential backoff.

- **CHANGELOG.md**
  - This file.

### Changed

- **System prompt reduction (~70% smaller)**
  - WhatsApp formatting directive compacted from 28 lines to 3 lines (formatting.WhatsApp() post-processor catches any Markdown that slips through).
  - Safety preamble rewritten: sandbox is free-form with full LAN access, LLM encouraged to execute any user command.
  - Skills show slim 1-line summaries when not activated; full prompt_hint only when keyword-matched.
  - MCP tool catalog (name + 1-line description) replaces full tool name listing in lean mode.

- **Skill keyword matching reads from config.yaml**
  - Hardcoded `skillKeywords` map in runtime.go replaced with `keywords` field in each skill's config.yaml.
  - `matchSkillsByMessage()` reads keywords from skills directly.

- **Skill timeout override**
  - Skill config `timeout` now overrides the default **upward** (not downward).
  - A skill declaring `timeout: 300` gets 300s even if the default is 120s.

- **Bash tool description**
  - Updated to encourage sandbox use: "install packages, run network tools, perform any operation the user requests."

- **Cron timezone support**
  - Gateway cron scheduler uses configured timezone instead of defaulting to UTC.

### Removed

- **Agent memory system**
  - Removed `memory_write` / `memory_read` tools, `MemoryStore`, `MemoryConfig`.
  - Reduces native tool count from 15 to 13 and eliminates memory prompt injection (~500–2000 tokens saved).
  - Files deleted: `internal/agent/memory/`, `internal/agent/tools/memory.go`.

### Fixed

- **MCP boolean schema normalization**
  - Converts bare `true`/`false` JSON schemas to `{"type":"object"}` to prevent llama.cpp/vLLM rejection.

- **Special token leakage recovery**
  - Detects GPT-OSS chat-template tokens (`<|channel|>`, `<|message|>`, etc.) in LLM output.
  - Retries with a recovery hint that instructs the model to use proper JSON tool calls.

- **MCP transport error handling**
  - `isMCPTransportError` detection for closed SSE sessions.
  - Agent waits for Loader reconnection before retrying instead of burning MaxSteps.

- **Data race in lifecycle shutdown**
  - Fixed race condition when phase times out during ordered shutdown.

- **RabbitMQ reconnection**
  - Improved reconnection logic for dropped AMQP connections.

- **WhatsApp delivery**
  - Long messages split into chunks to prevent Wuzapi 500 errors.
  - Control characters stripped from incoming messages.
