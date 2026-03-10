// Package tokens provides token counting for prompt/context estimation,
// using the model's tokenizer when available (OpenAI-compatible) or a heuristic fallback.
package tokens

import (
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/tiktoken-go/tokenizer"
)

// We use cl100k_base for OpenAI-compatible servers (GPT-4, GPT-3.5, and most local endpoints).
const encodingCl100k = tokenizer.Cl100kBase

var (
	cl100kEnc tokenizer.Codec
	cl100kErr error
	cl100kOnce sync.Once
)

func getCl100k() (tokenizer.Codec, error) {
	cl100kOnce.Do(func() {
		cl100kEnc, cl100kErr = tokenizer.Get(encodingCl100k)
	})
	return cl100kEnc, cl100kErr
}

// isOpenAICompatible returns true when the provider uses an OpenAI-compatible API
// and we can use tiktoken for token counting.
func isOpenAICompatible(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "openai", "local", "lmstudio", "vllm", "localai":
		return true
	default:
		return false
	}
}

// messageTextLen returns the total character length of content we use for token counting.
func messageTextLen(m *schema.Message) int {
	if m == nil {
		return 0
	}
	n := len(m.Content)
	for _, p := range m.UserInputMultiContent {
		if p.Text != "" {
			n += len(p.Text)
		}
	}
	for _, p := range m.AssistantGenMultiContent {
		if p.Text != "" {
			n += len(p.Text)
		}
	}
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	if m.ToolCallID != "" {
		n += len(m.ToolCallID)
	}
	return n
}

// textForCounting builds a single string from system prompt and messages for tiktoken.
func textForCounting(systemPrompt string, messages []*schema.Message) string {
	var sb strings.Builder
	sb.WriteString(systemPrompt)
	for _, m := range messages {
		if m == nil {
			continue
		}
		sb.WriteString(m.Content)
		for _, p := range m.UserInputMultiContent {
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		for _, p := range m.AssistantGenMultiContent {
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		for _, tc := range m.ToolCalls {
			sb.WriteString(tc.Function.Name)
			sb.WriteString(tc.Function.Arguments)
		}
		if m.ToolCallID != "" {
			sb.WriteString(m.ToolCallID)
		}
	}
	return sb.String()
}

// heuristicEstimate returns a token count using a character-based heuristic.
// Uses ~3.5 chars per token (more accurate for mixed content than 4).
func heuristicEstimate(systemPrompt string, messages []*schema.Message) int {
	n := len(systemPrompt)
	for _, m := range messages {
		n += messageTextLen(m)
	}
	// 3.5 chars/token: (n * 2 + 6) / 7 is a safe integer approximation
	return (n*2 + 6) / 7
}

// EstimateInputTokens returns an estimated token count for system prompt + messages.
// When provider is OpenAI or OpenAI-compatible (local, lmstudio, vllm, localai),
// uses the model's tokenizer (tiktoken cl100k_base). Otherwise uses a character-based heuristic.
func EstimateInputTokens(provider, _ string, systemPrompt string, messages []*schema.Message) int {
	if isOpenAICompatible(provider) {
		enc, err := getCl100k()
		if err != nil {
			return heuristicEstimate(systemPrompt, messages)
		}
		text := textForCounting(systemPrompt, messages)
		ids, _, err := enc.Encode(text)
		if err != nil {
			return heuristicEstimate(systemPrompt, messages)
		}
		return len(ids)
	}
	return heuristicEstimate(systemPrompt, messages)
}
