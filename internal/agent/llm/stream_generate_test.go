package llm

import (
	"context"
	"testing"
	"time"
)

func TestLLMTimingContext(t *testing.T) {
	ctx := context.Background()

	// No timing in context → nil
	if got := LLMTimingFromContext(ctx); got != nil {
		t.Fatalf("expected nil timing from empty context, got %v", got)
	}

	// With timing in context
	timing := &LLMTiming{}
	ctx = ContextWithLLMTiming(ctx, timing)

	got := LLMTimingFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil timing from context")
	}

	// Mutate and verify pointer identity
	got.TTFT = 500 * time.Millisecond
	got.GenerationDuration = 2 * time.Second
	if timing.TTFT != 500*time.Millisecond {
		t.Errorf("expected pointer identity: TTFT = 500ms, got %v", timing.TTFT)
	}
	if timing.GenerationDuration != 2*time.Second {
		t.Errorf("expected pointer identity: GenerationDuration = 2s, got %v", timing.GenerationDuration)
	}
}

func TestTTFTFromContext(t *testing.T) {
	ctx := context.Background()
	if got := TTFTFromContext(ctx); got != 0 {
		t.Fatalf("expected zero, got %v", got)
	}

	ctx = context.WithValue(ctx, ttftKey{}, 123*time.Millisecond)
	if got := TTFTFromContext(ctx); got != 123*time.Millisecond {
		t.Fatalf("expected 123ms, got %v", got)
	}
}

func TestGenerationDurationFromContext(t *testing.T) {
	ctx := context.Background()
	if got := GenerationDurationFromContext(ctx); got != 0 {
		t.Fatalf("expected zero, got %v", got)
	}

	ctx = context.WithValue(ctx, generationDurationKey{}, 2*time.Second)
	if got := GenerationDurationFromContext(ctx); got != 2*time.Second {
		t.Fatalf("expected 2s, got %v", got)
	}
}
