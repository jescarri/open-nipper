// Package llm provides a factory for creating Eino ChatModel instances.
package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einoollama "github.com/cloudwego/eino-ext/components/model/ollama"

	"github.com/jescarri/open-nipper/internal/config"
)

const defaultInferenceTimeoutSeconds = 120

// NewChatModel constructs an Eino model.ChatModel from InferenceConfig.
//
// Provider routing:
//
//	"openai"                          → OpenAI-compatible (api_key required)
//	"ollama"                          → Ollama native API (/api/chat) — for actual Ollama only
//	"local","lmstudio","vllm","localai" → OpenAI-compatible (api_key optional, base_url required)
//	""  (empty / unset)               → defaults to "openai"
//
// LM Studio, vLLM, LocalAI, and text-generation-inference all expose an
// OpenAI-compatible API (/v1/chat/completions). Use provider "local" (or
// any alias above) with base_url pointing to the server. The "ollama"
// provider is reserved for Ollama's native /api/chat endpoint.
func NewChatModel(ctx context.Context, cfg config.InferenceConfig) (model.ChatModel, error) { //nolint:staticcheck // SA1019: return ChatModel for compatibility
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "ollama":
		return newOllamaModel(ctx, cfg)
	case "local", "lmstudio", "vllm", "localai":
		return newLocalModel(ctx, cfg)
	default:
		return newOpenAIModel(ctx, cfg)
	}
}

func newOpenAIModel(ctx context.Context, cfg config.InferenceConfig) (model.ChatModel, error) { //nolint:staticcheck // SA1019: ChatModel deprecated
	apiKey := cfg.APIKey
	if apiKey == "" {
		if isLocalCompatEndpoint(cfg.BaseURL) {
			apiKey = "local"
		} else {
			return nil, fmt.Errorf("inference.api_key is required for provider %q (set OPENAI_API_KEY)", cfg.Provider)
		}
	}

	timeoutSecs := cfg.TimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = defaultInferenceTimeoutSeconds
	}

	ocfg := &einoopenai.ChatModelConfig{
		APIKey:  apiKey,
		Model:   cfg.Model,
		Timeout: time.Duration(timeoutSecs) * time.Second,
	}
	if cfg.BaseURL != "" {
		ocfg.BaseURL = cfg.BaseURL
	}

	localCompat := isLocalCompatEndpoint(cfg.BaseURL)
	reasoning := isReasoningModel(cfg.Model)

	// Temperature: reasoning models (o1, o3, gpt-5+) reject temperature.
	// For local-compatible endpoints we always send temperature, including 0
	// (greedy decoding), because the user may have deliberately set it to 0
	// and the server's default is typically 0.7 or 1.0. For OpenAI we preserve
	// the old behaviour of omitting zero so we don't accidentally override a
	// model-level default with an explicit 0.
	if !reasoning {
		if localCompat || cfg.Temperature != 0 {
			t := float32(cfg.Temperature)
			ocfg.Temperature = &t
		}
	}

	if cfg.FrequencyPenalty != 0 {
		fp := float32(cfg.FrequencyPenalty)
		ocfg.FrequencyPenalty = &fp
	}

	// MaxTokens vs MaxCompletionTokens:
	// OpenAI deprecated max_tokens in favor of max_completion_tokens for newer models.
	// Reasoning models require max_completion_tokens; older models accept both.
	// Local-compatible endpoints (LocalAI, vLLM) typically only support max_tokens.
	if cfg.MaxTokens > 0 {
		if localCompat {
			ocfg.MaxTokens = &cfg.MaxTokens
		} else if reasoning {
			ocfg.MaxCompletionTokens = &cfg.MaxTokens
		} else {
			ocfg.MaxCompletionTokens = &cfg.MaxTokens
		}
	}

	m, err := einoopenai.NewChatModel(ctx, ocfg)
	if err != nil {
		return nil, fmt.Errorf("creating openai chat model: %w", err)
	}
	return m, nil
}

// newLocalModel creates an OpenAI-compatible model for local inference
// servers (LM Studio, vLLM, LocalAI, text-generation-inference).
// api_key is optional (defaults to "local"), base_url is required.
func newLocalModel(ctx context.Context, cfg config.InferenceConfig) (model.ChatModel, error) { //nolint:staticcheck // SA1019: ChatModel deprecated
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("inference.base_url is required for provider %q (e.g. http://localhost:1234/v1)", cfg.Provider)
	}
	localCfg := cfg
	if localCfg.APIKey == "" {
		localCfg.APIKey = "local"
	}
	return newOpenAIModel(ctx, localCfg)
}

func newOllamaModel(ctx context.Context, cfg config.InferenceConfig) (model.ChatModel, error) { //nolint:staticcheck // SA1019: ChatModel deprecated
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	ocfg := &einoollama.ChatModelConfig{
		BaseURL: baseURL,
		Model:   cfg.Model,
	}

	m, err := einoollama.NewChatModel(ctx, ocfg)
	if err != nil {
		return nil, fmt.Errorf("creating ollama chat model: %w", err)
	}
	return m, nil
}

// isReasoningModel returns true for models that reject temperature and
// the deprecated max_tokens parameter (OpenAI o-series and gpt-5+).
func isReasoningModel(modelName string) bool {
	lower := strings.ToLower(modelName)

	// o1, o1-mini, o1-preview, o3, o3-mini, etc.
	if strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") {
		return true
	}

	// gpt-5, gpt-5.2, gpt-5-turbo, etc.
	if strings.HasPrefix(lower, "gpt-5") {
		return true
	}

	return false
}

// IsReasoningModel is exported for testing.
func IsReasoningModel(modelName string) bool {
	return isReasoningModel(modelName)
}

// isLocalCompatEndpoint returns true when the BaseURL points to a
// non-OpenAI endpoint (LocalAI, vLLM, etc.) that uses the legacy
// OpenAI-compatible API surface.
func isLocalCompatEndpoint(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	lower := strings.ToLower(baseURL)
	return !strings.Contains(lower, "openai.com") && !strings.Contains(lower, "azure.com")
}
