package agent

import (
	"strings"
	"testing"
	"time"
)

func TestUsageTrackerRecord(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)

	tracker.Record("session-1", 100, 50, 100)
	usage := tracker.Get("session-1")
	if usage == nil {
		t.Fatal("expected usage data")
	}
	if usage.TotalInputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", usage.TotalInputTokens)
	}
	if usage.TotalOutputTokens != 50 {
		t.Errorf("expected 50 output tokens, got %d", usage.TotalOutputTokens)
	}
	if usage.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", usage.TotalRequests)
	}

	tracker.Record("session-1", 200, 100, 200)
	usage = tracker.Get("session-1")
	if usage.TotalInputTokens != 300 {
		t.Errorf("expected 300 cumulative input tokens, got %d", usage.TotalInputTokens)
	}
	if usage.TotalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", usage.TotalRequests)
	}
}

func TestUsageTrackerContextUsage(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 100000)

	tracker.Record("session-1", 50000, 10000, 50000)
	usage := tracker.Get("session-1")
	if usage.ContextWindowSize != 100000 {
		t.Errorf("expected context window 100000, got %d", usage.ContextWindowSize)
	}
	if usage.LastUsagePercent <= 0 {
		t.Errorf("expected positive usage percent, got %f", usage.LastUsagePercent)
	}
}

func TestUsageTrackerReset(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)

	tracker.Record("session-1", 100, 50, 100)
	tracker.Reset("session-1")

	usage := tracker.Get("session-1")
	if usage != nil {
		t.Error("expected nil after reset")
	}
}

func TestUsageTrackerGetNonExistent(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)
	usage := tracker.Get("nonexistent")
	if usage != nil {
		t.Error("expected nil for nonexistent session")
	}
}

func TestUsageTrackerFormatUsage(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)
	tracker.Record("session-1", 1000, 500, 1000)

	formatted := tracker.FormatUsage("session-1")
	if formatted == "" {
		t.Fatal("expected non-empty formatted output")
	}
	if !strings.Contains(formatted, "gpt-4o") {
		t.Error("expected model name in output")
	}
	if !strings.Contains(formatted, "1000") {
		t.Error("expected input token count")
	}
	if !strings.Contains(formatted, "500") {
		t.Error("expected output token count")
	}
}

func TestUsageTrackerFormatUsageNoData(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)
	formatted := tracker.FormatUsage("nonexistent")
	if !strings.Contains(formatted, "No usage data") {
		t.Errorf("expected 'no usage data' message, got: %s", formatted)
	}
}

func TestEstimateCostKnownModel(t *testing.T) {
	cost := estimateCost("gpt-4o", 1000, 1000)
	if cost <= 0 {
		t.Errorf("expected positive cost for gpt-4o, got %f", cost)
	}
}

func TestEstimateCostUnknownModel(t *testing.T) {
	cost := estimateCost("my-local-model", 1000, 1000)
	if cost != 0 {
		t.Errorf("expected zero cost for unknown model, got %f", cost)
	}
}

func TestEstimateCostCaseInsensitive(t *testing.T) {
	cost := estimateCost("GPT-4O-Mini", 1000, 1000)
	if cost <= 0 {
		t.Errorf("expected positive cost for GPT-4O-Mini, got %f", cost)
	}
}

func TestUsageTrackerMultipleSessions(t *testing.T) {
	tracker := NewUsageTracker("gpt-4o", 128000)

	tracker.Record("session-a", 100, 50, 100)
	tracker.Record("session-b", 200, 100, 200)

	usageA := tracker.Get("session-a")
	usageB := tracker.Get("session-b")

	if usageA.TotalInputTokens != 100 {
		t.Errorf("session-a: expected 100, got %d", usageA.TotalInputTokens)
	}
	if usageB.TotalInputTokens != 200 {
		t.Errorf("session-b: expected 200, got %d", usageB.TotalInputTokens)
	}
}

func TestFormatResponseFooter(t *testing.T) {
	// Single step, compaction threshold 60%
	footer := FormatResponseFooter("gpt-4o", 800, 400, 1, 800, 128000, 60, 2*time.Second, nil)
	if footer == "" {
		t.Fatal("expected non-empty footer")
	}
	if !strings.Contains(footer, "2.00s") {
		t.Errorf("expected completion time (2.00s) in footer, got: %s", footer)
	}
	if !strings.Contains(footer, "1200") {
		t.Errorf("expected total tokens 1200 in footer, got: %s", footer)
	}
	if !strings.Contains(footer, "800") || !strings.Contains(footer, "400") {
		t.Errorf("expected in/out token counts in footer, got: %s", footer)
	}
	if !strings.Contains(footer, "tok/s") {
		t.Errorf("expected tok/s in footer, got: %s", footer)
	}
	// Should show fill%/threshold% format
	if !strings.Contains(footer, "Ctx:") {
		t.Errorf("expected Ctx: in footer, got: %s", footer)
	}
	if !strings.Contains(footer, "/60%") {
		t.Errorf("expected compaction threshold /60%% in footer, got: %s", footer)
	}
}

func TestFormatResponseFooter_MultiStep(t *testing.T) {
	footer := FormatResponseFooter("gpt-4o", 2000, 800, 3, 1500, 128000, 60, 5*time.Second, nil)
	if !strings.Contains(footer, "3 steps") {
		t.Errorf("expected '3 steps' in footer, got: %s", footer)
	}
}

func TestFormatResponseFooter_ZeroTokens(t *testing.T) {
	footer := FormatResponseFooter("gpt-4o", 0, 0, 0, 0, 128000, 60, time.Second, nil)
	if footer == "" {
		t.Fatal("expected footer with timing even when tokens are zero")
	}
	if !strings.Contains(footer, "1.00s") {
		t.Errorf("expected completion time in zero-token footer, got: %s", footer)
	}
	if strings.Contains(footer, "tokens") {
		t.Errorf("expected no token counts in zero-token footer, got: %s", footer)
	}
}

func TestFormatResponseFooter_LocalModel(t *testing.T) {
	// Local model: no cost should appear.
	footer := FormatResponseFooter("my-local-llama", 500, 200, 1, 500, 32000, 60, 3*time.Second, nil)
	if strings.Contains(footer, "$") {
		t.Errorf("expected no cost for local model, got: %s", footer)
	}
}

func TestFormatResponseFooter_NoCompactionThreshold(t *testing.T) {
	// compactionThresholdPct=0 means disabled; should show plain percentage
	footer := FormatResponseFooter("gpt-4o", 800, 400, 1, 800, 128000, 0, 2*time.Second, nil)
	if !strings.Contains(footer, "Ctx:") {
		t.Errorf("expected Ctx: in footer, got: %s", footer)
	}
	if strings.Contains(footer, "/0%") {
		t.Errorf("should not show /0%% when compaction is disabled, got: %s", footer)
	}
}

func TestFormatResponseFooter_WithTiming(t *testing.T) {
	timing := &FooterTiming{
		TTFT:               1500 * time.Millisecond,
		GenerationDuration: 3 * time.Second,
	}
	footer := FormatResponseFooter("gpt-4o", 800, 300, 1, 800, 128000, 60, 5*time.Second, timing)
	// TTFT should appear
	if !strings.Contains(footer, "TTFT: 1.50s") {
		t.Errorf("expected TTFT: 1.50s in footer, got: %s", footer)
	}
	// tok/s should use generation duration (300/3=100) not total elapsed (300/5=60)
	if !strings.Contains(footer, "100 tok/s") {
		t.Errorf("expected 100 tok/s (generation-based) in footer, got: %s", footer)
	}
}

func TestRequestTokenAccumulator_Timing(t *testing.T) {
	var accum requestTokenAccumulator
	accum.Add(100, 50)
	accum.AddTiming(500*time.Millisecond, 2*time.Second)

	ttft, genDur := accum.LastTiming()
	if ttft != 500*time.Millisecond {
		t.Errorf("expected ttft=500ms, got %v", ttft)
	}
	if genDur != 2*time.Second {
		t.Errorf("expected genDuration=2s, got %v", genDur)
	}

	// Second call overwrites
	accum.Add(200, 100)
	accum.AddTiming(300*time.Millisecond, 1*time.Second)
	ttft, genDur = accum.LastTiming()
	if ttft != 300*time.Millisecond {
		t.Errorf("expected ttft=300ms after second call, got %v", ttft)
	}
	if genDur != 1*time.Second {
		t.Errorf("expected genDuration=1s after second call, got %v", genDur)
	}
}
