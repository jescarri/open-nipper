package llm_test

import (
	"context"
	"testing"

	"github.com/jescarri/open-nipper/internal/agent/llm"
	"github.com/jescarri/open-nipper/internal/config"
)

func TestNewChatModel_OpenAI(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider:    "openai",
		Model:       "gpt-4o",
		APIKey:      "sk-test-key",
		Temperature: 0.7,
		MaxTokens:   1024,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_OpenAICompatible(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "openai",
		Model:    "llama3",
		APIKey:   "ignored",
		BaseURL:  "http://localhost:11434/v1",
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_Ollama(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "ollama",
		Model:    "llama3",
		BaseURL:  "http://localhost:11434",
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_MissingAPIKey(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "openai",
		Model:    "gpt-4o",
		APIKey:   "",
	}
	_, err := llm.NewChatModel(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error with missing API key, got nil")
	}
}

func TestNewChatModel_LocalProvider(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "qwen2.5-7b",
		BaseURL:  "http://192.168.2.73:1234/v1",
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_LMStudioProvider(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider:    "lmstudio",
		Model:       "llama3",
		BaseURL:     "http://localhost:1234/v1",
		Temperature: 0.7,
		MaxTokens:   4096,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_LocalProvider_MissingBaseURL(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "llama3",
	}
	_, err := llm.NewChatModel(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when local provider has no base_url")
	}
}

func TestNewChatModel_OpenAI_LocalEndpoint_NoAPIKey(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "openai",
		Model:    "llama3",
		BaseURL:  "http://localhost:8080/v1",
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected api_key to be auto-set for local endpoint, got error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_DefaultsToOpenAI(t *testing.T) {
	cfg := config.InferenceConfig{
		Model:  "gpt-4o",
		APIKey: "sk-test",
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_ReasoningModel(t *testing.T) {
	// Reasoning models (o1, o3, gpt-5+) should not fail even with temperature set.
	cfg := config.InferenceConfig{
		Provider:    "openai",
		Model:       "gpt-5.2",
		APIKey:      "sk-test",
		Temperature: 0.7,
		MaxTokens:   4096,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error creating reasoning model: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_O1Model(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider:    "openai",
		Model:       "o1-mini",
		APIKey:      "sk-test",
		Temperature: 0.5,
		MaxTokens:   2048,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_O3Model(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider:    "openai",
		Model:       "o3",
		APIKey:      "sk-test",
		Temperature: 0.7,
		MaxTokens:   8192,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestNewChatModel_LocalAI_KeepsLegacyParams(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider:    "openai",
		Model:       "gpt-5.2",
		APIKey:      "not-needed",
		BaseURL:     "http://localhost:8080/v1",
		Temperature: 0.7,
		MaxTokens:   4096,
	}
	m, err := llm.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil model")
	}
}

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-3.5-turbo", false},
		{"o1", true},
		{"o1-mini", true},
		{"o1-preview", true},
		{"o3", true},
		{"o3-mini", true},
		{"gpt-5", true},
		{"gpt-5.2", true},
		{"gpt-5-turbo", true},
		{"llama3", false},
		{"claude-3-sonnet", false},
		{"mistral-7b", false},
	}
	for _, tt := range tests {
		got := llm.IsReasoningModel(tt.model)
		if got != tt.expected {
			t.Errorf("IsReasoningModel(%q) = %v, want %v", tt.model, got, tt.expected)
		}
	}
}
