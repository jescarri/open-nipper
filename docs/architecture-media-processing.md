# Media Processing Architecture: Speech Recognition & Vision

## Status: PROPOSAL
## Date: 2026-03-06

---

## Problem Statement

When a user sends an audio message (WhatsApp voice note, Slack audio file), the agent currently
sees only a media annotation like `[Audio: <url>]` and cannot understand its content. We need
speech-to-text transcription so the agent can reason about audio messages. The solution must be
extensible to support vision (image analysis) and future modalities.

---

## Current State

### What exists today

1. **Gateway normalization** already produces well-typed `ContentPart` structs:
   ```go
   ContentPart{Type: "audio", URL: "s3://...", MimeType: "audio/ogg"}
   ContentPart{Type: "image", URL: "s3://...", MimeType: "image/jpeg"}
   ```

2. **Image inlining** (`multimodal_user.go`) fetches the first image, downscales it, and inlines
   it as base64 for vision-capable models. EXIF is extracted separately via `doc_fetch`.

3. **Media annotations** (`convert.go:buildMediaAnnotations`) appends text like
   `"[Image: <url> - use doc_fetch to inspect]"` for non-vision models.

4. **EXIF extraction** (`tools/docfetch.go`) already parses image metadata.

5. **Speech LLM** available at a configurable endpoint (e.g. `https://speech.example.com`) (external service).

### Gaps

- No speech-to-text pipeline for audio `ContentPart`s.
- Image analysis is tightly coupled to `multimodal_user.go` rather than being a composable stage.
- No unified pattern for "enrich media before the LLM sees it."

---

## Proposed Architecture: Media Enrichment Pipeline

### Core Idea

Introduce a **media enrichment pipeline** that runs between message normalization and the LLM
call. Each media type gets an **enricher** that transforms a `ContentPart` into richer content
the LLM can reason about. The main LLM always receives text (or text + inline image for vision
models) -- it never needs to "know" how to call a speech API.

```
Gateway                    Agent Runtime
  |                            |
  |  NipperMessage             |
  |  (with ContentParts)       |
  +--------------------------->|
                               |
                     +---------v----------+
                     | Media Enrichment   |
                     | Pipeline           |
                     |                    |
                     |  [speech enricher] | audio  --> transcript text
                     |  [vision enricher] | image  --> description + EXIF
                     |  [video enricher]  | video  --> (future)
                     |                    |
                     +---------+----------+
                               |
                     Enriched NipperMessage
                     (ContentParts now carry
                      .Transcript, .Description,
                      .EnrichedText fields)
                               |
                     +---------v----------+
                     | Convert to Eino    |
                     | Messages           |
                     | (convert.go)       |
                     +---------+----------+
                               |
                     +---------v----------+
                     | LLM (ReAct Agent)  |
                     +--------------------+
```

### Why NOT a sub-agent or tool?

| Approach | Pros | Cons |
|----------|------|------|
| **Pre-processing pipeline** (proposed) | Deterministic, no wasted LLM tokens, fast, always runs | Less flexible if LLM should decide *whether* to transcribe |
| **Tool (`speech_recognize`)** | LLM decides when to use it | Wastes a tool-call round-trip; LLM may forget to call it; audio content invisible until tool is called |
| **Sub-agent** | Clean separation | Over-engineered for a stateless API call; adds latency; sub-agent needs its own LLM call just to decide to transcribe |

**Verdict:** Pre-processing pipeline is the right choice because:
- Transcription is *always* needed when audio arrives -- there's no decision to make.
- It keeps the main LLM context clean (text in, text out).
- It's the same pattern as the existing image inlining in `multimodal_user.go`.
- Vision can follow the same pattern: always enrich images before the LLM sees them.

### When a tool IS the right choice

A `speech_recognize` tool remains useful for **on-demand** transcription of audio URLs
discovered mid-conversation (e.g., a user pastes a link to a podcast). The enrichment pipeline
handles inbound media; the tool handles URLs the LLM encounters during reasoning.

---

## Detailed Design

### 1. Enricher Interface

```go
// internal/agent/enrich/enricher.go

package enrich

import "context"

// Enricher processes a ContentPart and returns enriched metadata.
// It does NOT modify the original part; it returns supplemental data.
type Enricher interface {
    // Supports returns true if this enricher handles the given content type.
    Supports(contentType string) bool

    // Enrich fetches/processes the media and returns structured results.
    Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error)
}

// ContentPartView is a read-only view of a ContentPart for enrichers.
type ContentPartView struct {
    Type     string
    URL      string
    MimeType string
    Caption  string
}

// EnrichmentResult carries the output of an enricher.
type EnrichmentResult struct {
    // Transcript is the speech-to-text output (for audio).
    Transcript string

    // Description is a generated description (for images via vision model).
    Description string

    // Metadata is structured key-value metadata (e.g., EXIF fields).
    Metadata map[string]string

    // Error is a human-readable error if enrichment failed but is non-fatal.
    Error string
}
```

### 2. Speech Enricher

```go
// internal/agent/enrich/speech.go

package enrich

import (
    "context"
    "fmt"
    "io"
    "net/http"
)

type SpeechEnricher struct {
    Endpoint   string       // e.g., "https://speech.example.com"
    HTTPClient *http.Client
}

func (s *SpeechEnricher) Supports(contentType string) bool {
    return contentType == "audio"
}

func (s *SpeechEnricher) Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error) {
    // 1. Fetch audio bytes from part.URL (S3 pre-signed or HTTP)
    // 2. POST to speech endpoint for transcription
    // 3. Return transcript
    // (Implementation details depend on the speech API contract)
    return &EnrichmentResult{
        Transcript: transcribedText,
    }, nil
}
```

### 3. Vision Enricher (future, same pattern)

```go
// internal/agent/enrich/vision.go

package enrich

type VisionEnricher struct {
    Endpoint   string       // vision model endpoint
    HTTPClient *http.Client
}

func (v *VisionEnricher) Supports(contentType string) bool {
    return contentType == "image"
}

func (v *VisionEnricher) Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error) {
    // 1. Fetch image bytes from part.URL
    // 2. Extract EXIF metadata (reuse existing extractEXIF)
    // 3. Send to vision model for description
    // 4. Return description + EXIF metadata
    return &EnrichmentResult{
        Description: imageDescription,
        Metadata:    exifFields,
    }, nil
}
```

### 4. Pipeline Coordinator

```go
// internal/agent/enrich/pipeline.go

package enrich

import (
    "context"
    "sync"
)

// Pipeline runs enrichers against all ContentParts in a message.
type Pipeline struct {
    enrichers []Enricher
}

func NewPipeline(enrichers ...Enricher) *Pipeline {
    return &Pipeline{enrichers: enrichers}
}

// Run enriches all parts concurrently. Returns a map of part-index -> result.
func (p *Pipeline) Run(ctx context.Context, parts []ContentPartView) map[int]*EnrichmentResult {
    results := make(map[int]*EnrichmentResult)
    var mu sync.Mutex
    var wg sync.WaitGroup

    for i, part := range parts {
        for _, e := range p.enrichers {
            if e.Supports(part.Type) {
                wg.Add(1)
                go func(idx int, enricher Enricher, pv ContentPartView) {
                    defer wg.Done()
                    result, err := enricher.Enrich(ctx, pv)
                    if err != nil {
                        result = &EnrichmentResult{Error: err.Error()}
                    }
                    mu.Lock()
                    results[idx] = result
                    mu.Unlock()
                }(i, e, part)
                break // one enricher per part
            }
        }
    }

    wg.Wait()
    return results
}
```

### 5. Integration Point: How the Transcript Becomes the Prompt

This is the critical design point. The transcript must **replace the audio part as user input**,
not sit alongside it as metadata. The agent should process a voice note identically to a typed
message.

#### Current flow (text message)

```
User types: "What's the weather?"
  → msg.Content.Text = "What's the weather?"
  → NipperMessageToEinoMessage() → schema.Message{Role: User, Content: "What's the weather?"}
  → LLM sees: "What's the weather?"
```

#### Current flow (voice note -- broken)

```
User sends voice note saying "What's the weather?"
  → msg.Content.Text = ""  (voice notes have no text)
  → msg.Content.Parts = [{Type: "audio", URL: "s3://bucket/audio.ogg"}]
  → NipperMessageToEinoMessage() → buildMediaAnnotations()
  → LLM sees: "⚠️ ATTACHED FILE: AUDIO ... → call doc_fetch"
  → LLM calls doc_fetch on an audio file it can't interpret
```

#### New flow (voice note -- with enrichment)

```
User sends voice note saying "What's the weather?"
  → msg.Content.Text = ""
  → msg.Content.Parts = [{Type: "audio", URL: "s3://bucket/audio.ogg"}]

  [Step 1.5: Enrichment pipeline runs BEFORE convert.go]
  → SpeechEnricher fetches audio, calls speech API/model
  → Gets transcript: "What's the weather?"
  → MUTATES the message:
      msg.Content.Text = "What's the weather?"
      msg.Content.Parts[0].Type = "audio"  (unchanged, kept for reference)
      msg.Content.Parts[0].Transcript = "What's the weather?"

  [Step 2: convert.go runs as normal]
  → NipperMessageToEinoMessage() sees msg.Content.Text = "What's the weather?"
  → buildMediaAnnotations() SKIPS audio parts that have a Transcript (no doc_fetch directive)
  → LLM sees: "What's the weather?"
```

The agent has no idea it was a voice note. It just sees text. This is the goal.

#### Voice note WITH caption

WhatsApp doesn't support captions on voice notes, but Slack file uploads can have accompanying
text. In this case:

```
User sends audio file with text "listen to this"
  → msg.Content.Text = "listen to this"
  → msg.Content.Parts = [{Type: "audio", URL: "s3://..."}]

  [Enrichment]
  → Transcript: "Hey, remind me to buy groceries tomorrow"
  → MUTATES:
      msg.Content.Text = "listen to this\n\n[Voice message]: Hey, remind me to buy groceries tomorrow"

  → LLM sees: "listen to this\n\n[Voice message]: Hey, remind me to buy groceries tomorrow"
```

When there's already text, the transcript is appended with a `[Voice message]` prefix so the
LLM knows which part was spoken vs typed.

#### Implementation in runtime.go

The enrichment runs in `handleMessage()` at a new **Step 1.5**, after the message is received
but before `NipperMessageToEinoMessage()` is called at Step 2 (line 456).

```go
// runtime.go -- handleMessage(), between step 1c and step 2

// 1.5. Enrich media: transcribe audio, describe images (future).
if r.enrichPipeline != nil {
    if err := r.enrichPipeline.EnrichMessage(ctx, msg); err != nil {
        // Non-fatal: log and continue. The LLM will see the raw media annotation
        // and can still call doc_fetch as a fallback.
        r.logger.Warn("media enrichment failed",
            zap.String("sessionKey", msg.SessionKey),
            zap.Error(err),
        )
    }
}

// 2. Build input: history + current user message.
// NipperMessageToEinoMessage() now sees msg.Content.Text populated with the transcript.
```

#### Implementation of EnrichMessage

The pipeline mutates the `NipperMessage` in-place. This is intentional -- the enriched
content flows through to transcript persistence, so replayed sessions also see the transcript.

```go
// internal/agent/enrich/pipeline.go

// EnrichMessage processes all media parts in the message and injects results
// back into the message content. It mutates msg in-place.
func (p *Pipeline) EnrichMessage(ctx context.Context, msg *models.NipperMessage) error {
    var transcripts []string

    for i := range msg.Content.Parts {
        part := &msg.Content.Parts[i]

        for _, e := range p.enrichers {
            if !e.Supports(part.Type) {
                continue
            }

            result, err := e.Enrich(ctx, ContentPartView{
                Type:     part.Type,
                URL:      part.URL,
                MimeType: part.MimeType,
                Caption:  part.Caption,
            })
            if err != nil {
                return fmt.Errorf("enriching %s part %d: %w", part.Type, i, err)
            }

            // Store the raw transcript on the part (for persistence/debugging).
            if result.Transcript != "" {
                part.Transcript = result.Transcript
                transcripts = append(transcripts, result.Transcript)
            }
            break // one enricher per part
        }
    }

    // Inject transcripts into the message text.
    // This is what makes the transcript "become the prompt."
    if len(transcripts) == 0 {
        return nil
    }

    joined := strings.Join(transcripts, "\n")
    if msg.Content.Text == "" {
        // Audio-only message: transcript IS the text. No prefix needed.
        msg.Content.Text = joined
    } else {
        // Mixed message (text + audio): append with a label.
        msg.Content.Text = msg.Content.Text + "\n\n[Voice message]: " + joined
    }

    return nil
}
```

#### Required change to convert.go

`buildMediaAnnotations()` must skip audio parts that already have a transcript.
Otherwise the LLM sees both the transcript AND a `doc_fetch` directive for the same audio.

```go
// convert.go -- buildMediaAnnotations(), add at the top of the loop:

func buildMediaAnnotations(parts []models.ContentPart) string {
    var annotations []string
    for _, p := range parts {
        if p.Type == "text" || p.URL == "" {
            continue
        }
        // Skip audio parts that were already transcribed by the enrichment pipeline.
        if p.Type == "audio" && p.Transcript != "" {
            continue
        }
        // ... rest unchanged
    }
    // ...
}
```

#### What gets persisted to transcript

The transcript line stored in the session uses `userMsgText.Content`, which comes from
`NipperMessageToEinoMessage(msg)`. Since we mutated `msg.Content.Text` before that call,
the stored transcript line contains the speech transcript as text. This means:

- Session replay works correctly (no need to re-transcribe).
- The `/compact` command sees the transcript text, not audio URLs.
- Memory search (`memory_read`) can find content from voice messages.

### 6. Configuration

```yaml
# agent.yaml

agent:
  media_enrichment:
    speech:
      enabled: true
      endpoint: "https://speech.example.com"
      timeout: 30s       # voice notes can be long
      max_duration: 300s  # refuse audio longer than 5 min
    vision:
      enabled: false      # future
      endpoint: ""
      timeout: 30s
```

### 7. ContentPart Extension

Add a single `Transcript` field to `ContentPart`. This is a passive field -- it stores the
enrichment output for debugging and for `buildMediaAnnotations()` to check, but the actual
prompt injection happens by mutating `msg.Content.Text` (see Step 5 above).

```go
// internal/models/message.go

type ContentPart struct {
    Type     string `json:"type"`
    Text     string `json:"text,omitempty"`
    URL      string `json:"url,omitempty"`
    MimeType string `json:"mimeType,omitempty"`
    Caption  string `json:"caption,omitempty"`

    // Transcript is populated by the media enrichment pipeline for audio parts.
    // It is NOT the primary way the transcript reaches the LLM -- that happens
    // via msg.Content.Text mutation. This field exists so that:
    // 1. buildMediaAnnotations() can skip already-transcribed audio parts.
    // 2. The raw transcript is available for logging/debugging.
    // 3. Future: persisted alongside the part for session replay without re-transcription.
    Transcript string `json:"transcript,omitempty"`

    // Location fields
    Latitude  float64 `json:"latitude,omitempty"`
    Longitude float64 `json:"longitude,omitempty"`
    Address   string  `json:"address,omitempty"`
}
```

For vision (future), add `Description` and `Metadata` fields using the same pattern.

---

## Message Flow: Voice Note from WhatsApp

```
1. User sends voice note saying "Hey, check the weather in Buenos Aires"
2. Wuzapi webhook fires with AudioMessage
3. WhatsApp normalizer creates:
     msg.Content.Text  = ""  (voice notes have no text)
     msg.Content.Parts = [{Type: "audio", URL: "s3://bucket/audio.ogg", MimeType: "audio/ogg"}]
4. Gateway router publishes NipperMessage to agent queue

5. Agent runtime receives message (runtime.go:handleMessage)
6. Steps 0-1c run as normal (commands, session, location, profile)

7. [NEW] Step 1.5 — enrichPipeline.EnrichMessage(ctx, msg):
     a. SpeechEnricher.Supports("audio") → true
     b. SpeechEnricher.Enrich():
        - Fetches audio bytes from S3
        - Calls speech API/model → "Hey, check the weather in Buenos Aires"
     c. Pipeline sets:
        - msg.Content.Parts[0].Transcript = "Hey, check the weather in Buenos Aires"
        - msg.Content.Text = "Hey, check the weather in Buenos Aires"
          ↑ THIS is the key mutation. The transcript IS the text now.

8. Step 2 — NipperMessageToEinoMessage(msg):
     - msg.Content.Text = "Hey, check the weather in Buenos Aires" ← already populated
     - buildMediaAnnotations() skips audio part (has Transcript)
     - Returns: schema.Message{Role: User, Content: "Hey, check the weather in Buenos Aires"}

9. LLM sees: "Hey, check the weather in Buenos Aires"
   → Calls get_weather tool → responds with weather info

10. Transcript persisted: {role: "user", content: "Hey, check the weather in Buenos Aires"}
    → Session replay, /compact, memory_read all work on the text.

11. Response delivered back via WhatsApp.
```

The LLM never knows it was a voice note. It processes the transcript exactly like typed text.

## Message Flow: Image from Slack (future, same pattern)

```
1. User uploads photo in Slack
2. Slack normalizer creates:
     ContentPart{Type: "image", URL: "https://files.slack.com/...", MimeType: "image/jpeg"}
3. Agent runtime receives message
4. [NEW] VisionEnricher.Enrich():
     - Fetches image, extracts EXIF
     - Sends to vision model → "A sunset over the Rio de la Plata"
     - Mutates msg.Content.Text with the description
5. LLM responds with context about the photo.
```

---

## Relationship to Existing Code

| Existing Code | What Happens To It |
|---------------|-------------------|
| `multimodal_user.go` (image inlining) | **Kept for vision-capable main LLMs.** The enrichment pipeline adds a description for non-vision models. For vision models, the existing inline path continues to work, but now also carries EXIF via `Metadata`. |
| `convert.go:buildMediaAnnotations` | **Updated** to prefer `Transcript`/`Description` fields when present, falling back to URL annotation when enrichment is unavailable. |
| `tools/docfetch.go` EXIF extraction | **Reused** by the VisionEnricher. Extract the function to a shared `media` package. |
| `doc_fetch` tool | **Kept** for on-demand media fetching during LLM reasoning. |

---

## Options Considered

### Option A: Enrichment Pipeline (RECOMMENDED)

As described above. Pre-processes media deterministically before the LLM.

- **Pros:** Clean, fast, no wasted tokens, extensible, follows existing patterns.
- **Cons:** Always runs (even if user says "ignore the voice note"). Adds latency to
  audio messages (transcription time).
- **Mitigation:** Transcription runs concurrently with session loading. Timeout + graceful
  fallback to URL annotation if speech API is down.

### Option B: Tool-Based Approach

Add `speech_recognize` and `vision_analyze` as agent tools. The LLM decides when to call them.

- **Pros:** LLM has full control. Can skip transcription if not needed.
- **Cons:** Extra tool-call round trip (500ms-2s). LLM may forget to call the tool.
  Audio content is invisible until tool is called, so LLM can't make informed decisions
  about *whether* to transcribe without already knowing what was said.
- **When to use:** Good as a *complement* to Option A for URLs discovered mid-conversation.

### Option C: Sub-Agent Approach

Dedicated speech/vision sub-agent that receives media messages and returns enriched text.

- **Pros:** Full isolation. Sub-agent could do multi-step reasoning (e.g., transcribe + translate + summarize).
- **Cons:** Over-engineered for what is essentially a single API call. Requires its own LLM
  invocation just to orchestrate. Adds significant latency. EINO's `DeepAgent` pattern is
  designed for complex multi-step workflows, not simple transformations.
- **When to use:** If media processing ever requires multi-step reasoning (e.g., "transcribe
  this audio, detect the language, translate to the user's preferred language, then summarize").
  Not justified for v1.

### Option D: Hybrid (RECOMMENDED for completeness)

Use Option A (enrichment pipeline) for inbound media + Option B (tools) for on-demand use.

- Pipeline handles automatic enrichment of incoming messages.
- `speech_recognize` tool available for audio URLs the LLM discovers during reasoning.
- Both share the same underlying client code.

---

## Implementation Plan

### Phase 1: Speech Recognition (this PR)

1. Create `internal/agent/enrich/` package with `Enricher` interface and `Pipeline`.
2. Implement `SpeechEnricher` targeting `https://speech.example.com`.
3. Add `Transcript` field to `ContentPart`.
4. Update `convert.go` to use `Transcript` when available.
5. Wire pipeline into `runtime.go:handleMessage()`.
6. Add configuration to `agent.yaml`.
7. Add `speech_recognize` tool (optional, for on-demand use).

### Phase 2: Vision Model (next PR)

1. Implement `VisionEnricher`.
2. Extract EXIF logic to shared `internal/media/exif.go`.
3. Add `Description` and `Metadata` fields to `ContentPart` (or confirm they exist from Phase 1).
4. Update `convert.go` and `multimodal_user.go` to use enrichment results.
5. Add `vision_analyze` tool for on-demand use.

### Phase 3: Consolidation

1. Refactor `multimodal_user.go` image inlining to use the enrichment pipeline.
2. Add metrics/observability (enrichment duration, success rate per type).
3. Support enrichment caching (avoid re-transcribing the same audio on session replay).

---

## Enricher Backend: Dedicated API vs. Multimodal LLM

A critical design decision is **what powers each enricher**. The enricher interface is
backend-agnostic -- it can call a raw HTTP API or a full LLM. Here are the three options:

### Backend A: Dedicated Speech API (e.g., Whisper endpoint)

```
Audio bytes --POST--> https://speech.example.com/v1/transcribe --> { "text": "..." }
```

- Simple HTTP call, no LLM overhead.
- Lowest latency (~1-5s for a voice note).
- No reasoning, no summarization -- just raw transcript.
- Configuration: single `endpoint` + optional `api_key`.

### Backend B: Multimodal LLM (audio-capable model)

Send the audio as a multimodal message to a second LLM instance (e.g., Gemini 2.5, GPT-4o-audio,
a local Whisper+LLaMA pipeline). The existing `llm.NewChatModel()` factory already supports
OpenAI, Ollama, and local providers -- we reuse it with a separate `InferenceConfig`.

```
Audio bytes --> Eino MessageInputAudio --> ChatModel.Generate() --> transcript + interpretation
```

- Can do more than transcribe: summarize, translate, extract intent, detect language.
- Uses the same `InferenceConfig` pattern the main agent already uses.
- Higher latency and cost (full LLM inference).
- The enricher gets a system prompt like: "Transcribe the following audio. Return only the
  transcript. If the language is not {user.Language}, also provide a translation."

**Implementation sketch:**

```go
// internal/agent/enrich/speech_llm.go

type SpeechLLMEnricher struct {
    model  model.ChatModel   // created via llm.NewChatModel(ctx, cfg.MediaModels.Speech)
    prompt string            // system prompt for the speech model
}

func (s *SpeechLLMEnricher) Supports(contentType string) bool {
    return contentType == "audio"
}

func (s *SpeechLLMEnricher) Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error) {
    // 1. Fetch audio bytes from part.URL
    // 2. Build Eino message with MessageInputAudio part
    // 3. Call s.model.Generate() with system prompt + audio message
    // 4. Parse response text as transcript
    audioMsg := &schema.Message{
        Role: schema.User,
        MultiContent: []schema.ChatMessagePart{
            {Type: schema.ChatMessagePartTypeAudioURL, AudioURL: &schema.AudioURL{URL: part.URL}},
            {Type: schema.ChatMessagePartTypeText, Text: "Transcribe this audio message."},
        },
    }
    resp, err := s.model.Generate(ctx, []*schema.Message{systemMsg, audioMsg})
    if err != nil {
        return nil, err
    }
    return &EnrichmentResult{Transcript: resp.Content}, nil
}
```

### Backend C: Hybrid (RECOMMENDED)

Use the dedicated API as the **fast path** and the multimodal LLM as an **optional enhancement**.

```yaml
# agent.yaml
agent:
  media_enrichment:
    speech:
      enabled: true
      # Backend selection: "api" or "model"
      backend: "api"
      # For backend: "api"
      endpoint: "https://speech.example.com"
      timeout: 30s
      # For backend: "model" -- reuses the InferenceConfig pattern
      model:
        provider: "openai"
        model: "gpt-4o-audio-preview"
        api_key: "${OPENAI_API_KEY}"
        base_url: ""
        timeout_seconds: 60
    vision:
      enabled: false
      backend: "model"
      model:
        provider: "openai"
        model: "gpt-4o"
        api_key: "${OPENAI_API_KEY}"
        timeout_seconds: 30
```

This gives you:
- **Speech**: Start with the fast dedicated API. Switch to a multimodal LLM when you need
  translation/summarization without changing any code -- just flip `backend: "model"`.
- **Vision**: Always uses a model (vision requires reasoning, not just extraction).
- **Future modalities**: Same pattern. Each enricher declares its backend preference.

### How this fits the existing code

The `InferenceConfig` struct already has everything needed:

```go
// config.go -- existing struct, unchanged
type InferenceConfig struct {
    Provider         string  `yaml:"provider"`
    Model            string  `yaml:"model"`
    BaseURL          string  `yaml:"base_url"`
    APIKey           string  `yaml:"api_key"`
    Temperature      float64 `yaml:"temperature"`
    MaxTokens        int     `yaml:"max_tokens"`
    TimeoutSeconds   int     `yaml:"timeout_seconds"`
    // ...
}
```

The new config adds a `MediaModels` map that reuses `InferenceConfig`:

```go
// config.go -- addition to AgentRuntimeConfig
type AgentRuntimeConfig struct {
    // ... existing fields ...
    MediaEnrichment MediaEnrichmentConfig `yaml:"media_enrichment" mapstructure:"media_enrichment"`
}

type MediaEnrichmentConfig struct {
    Speech MediaEnricherConfig `yaml:"speech" mapstructure:"speech"`
    Vision MediaEnricherConfig `yaml:"vision" mapstructure:"vision"`
}

type MediaEnricherConfig struct {
    Enabled  bool            `yaml:"enabled"  mapstructure:"enabled"`
    Backend  string          `yaml:"backend"  mapstructure:"backend"`  // "api" | "model"
    Endpoint string          `yaml:"endpoint" mapstructure:"endpoint"` // for backend: "api"
    Timeout  int             `yaml:"timeout"  mapstructure:"timeout"`  // seconds
    Model    InferenceConfig `yaml:"model"    mapstructure:"model"`    // for backend: "model"
}
```

The enricher factory selects the backend:

```go
// internal/agent/enrich/factory.go

func NewSpeechEnricher(ctx context.Context, cfg config.MediaEnricherConfig) (Enricher, error) {
    switch cfg.Backend {
    case "api":
        return &SpeechAPIEnricher{
            Endpoint:   cfg.Endpoint,
            HTTPClient: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
        }, nil
    case "model":
        chatModel, err := llm.NewChatModel(ctx, cfg.Model)
        if err != nil {
            return nil, fmt.Errorf("creating speech model: %w", err)
        }
        return &SpeechLLMEnricher{model: chatModel}, nil
    default:
        return nil, fmt.Errorf("unknown speech backend: %q", cfg.Backend)
    }
}
```

### Comparison Matrix

| Dimension | Dedicated API | Multimodal LLM | Hybrid |
|-----------|--------------|----------------|--------|
| **Latency** | ~1-5s | ~5-15s | Best of both |
| **Cost** | Low (Whisper-class) | High (GPT-4o-class) | Configurable |
| **Capabilities** | Transcribe only | Transcribe + translate + summarize + intent | Configurable |
| **Config complexity** | 1 endpoint | Full InferenceConfig | Both available |
| **Vision reuse** | N/A | Same pattern | Same pattern |
| **Offline/local** | Whisper.cpp server | Ollama multimodal | Both |
| **EINO integration** | HTTP client only | Full ChatModel + schema.MessageInputAudio | Both |

### When to use which

- **Dedicated API** when you have a fast, reliable speech service and just need transcripts.
- **Multimodal LLM** when you need the model to reason about the audio (summarize a meeting,
  detect sentiment, translate, extract action items).
- **Vision always uses a model** -- image analysis inherently requires reasoning.

---

## Architecture Diagram (with Model Backend)

```
                         agent.yaml
                            |
                  +---------v-----------+
                  | MediaEnrichmentConfig|
                  |                     |
                  |  speech:            |
                  |    backend: "model" |
                  |    model:           |
                  |      provider: openai|
                  |      model: gpt-4o  |
                  |                     |
                  |  vision:            |
                  |    backend: "model" |
                  |    model:           |
                  |      provider: ollama|
                  |      model: llava   |
                  +---------+-----------+
                            |
                            v
  NipperMessage -----> [Enrichment Pipeline]
  (audio part)              |
                            |  SpeechLLMEnricher
                            |    |
                            |    +---> llm.NewChatModel(cfg.Speech.Model)
                            |    |         |
                            |    |         v
                            |    |    model.Generate([system, audio_msg])
                            |    |         |
                            |    |         v
                            |    +<--- "Hey, check the weather..."
                            |
                            v
                  Enriched NipperMessage
                  (part.Transcript = "Hey, check the weather...")
                            |
                            v
                  [Main Agent LLM]
                  (sees transcript as text)
```

This means the system can run **up to 3 different models simultaneously**:

1. **Main agent LLM** -- reasoning, tool use, conversation (e.g., Claude, GPT-4o, local LLaMA)
2. **Speech model** -- transcription (e.g., Whisper API, GPT-4o-audio, local Whisper)
3. **Vision model** -- image analysis (e.g., GPT-4o, LLaVA via Ollama, local model)

Each configured independently via the same `InferenceConfig` pattern. Mix and match providers
freely (main agent on Claude, speech on local Whisper, vision on GPT-4o).

---

## Open Questions

1. **Speech API contract**: What is the exact API shape of `https://speech.example.com`?
   (endpoint path, auth, request/response format, supported audio codecs)
2. **Audio size limits**: WhatsApp voice notes can be up to 30 minutes. Should we cap at some
   duration and tell the user "voice note too long"?
3. **Language detection**: Does the speech API auto-detect language, or do we pass the user's
   profile language as a hint?
4. **Fallback behavior**: If transcription fails, should the agent see
   `"[Voice message - transcription unavailable, use doc_fetch <url> to access raw audio]"`?
5. **Vision model selection**: Will the vision enricher use the same model as the main LLM
   (if vision-capable), or a dedicated vision endpoint?
6. **Model backend for speech**: Should the speech LLM enricher include a system prompt that
   instructs translation to the user's profile language, or just raw transcription?
7. **Cost controls**: Should there be a per-message or per-user budget for media model calls?
