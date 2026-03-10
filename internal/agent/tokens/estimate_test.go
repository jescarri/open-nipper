package tokens

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestEstimateInputTokens_OpenAICompatible(t *testing.T) {
	provider := "openai"
	model := "gpt-4o-mini"
	systemPrompt := "You are a helpful assistant."
	messages := []*schema.Message{
		{Role: schema.User, Content: "Hello, world!"},
	}
	got := EstimateInputTokens(provider, model, systemPrompt, messages)
	if got <= 0 {
		t.Errorf("EstimateInputTokens(openai) = %d, want positive", got)
	}
	// tiktoken count for system + "Hello, world!" should be in a reasonable range (roughly 15–30)
	if got > 200 {
		t.Errorf("EstimateInputTokens(openai) = %d, suspect too high", got)
	}
}

func TestEstimateInputTokens_HeuristicFallback(t *testing.T) {
	provider := "ollama"
	model := "llama3"
	systemPrompt := "You are a helpful assistant."
	messages := []*schema.Message{
		{Role: schema.User, Content: "Hello, world!"},
	}
	got := EstimateInputTokens(provider, model, systemPrompt, messages)
	if got <= 0 {
		t.Errorf("EstimateInputTokens(ollama) = %d, want positive", got)
	}
	// heuristic: (len(systemPrompt)+len("Hello, world!"))*2/7 + 6/7 ≈ 12
	expectedApprox := (len(systemPrompt)+13)*2/7 + 1
	if got < expectedApprox/2 || got > expectedApprox*2 {
		t.Logf("heuristic estimate %d (expected approx %d)", got, expectedApprox)
	}
}

func TestHeuristicEstimate(t *testing.T) {
	systemPrompt := "You are helpful."
	messages := []*schema.Message{
		{Role: schema.User, Content: "Hi"},
	}
	got := heuristicEstimate(systemPrompt, messages)
	// 15+2 = 17 chars -> (34+6)/7 = 5
	if got < 3 || got > 15 {
		t.Errorf("heuristicEstimate = %d, want roughly 5", got)
	}
}
