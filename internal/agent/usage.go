package agent

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// requestTokenAccumulator tracks cumulative token usage across multiple LLM
// calls within a single ReAct agent request (model → tool → model → …).
// Safe for concurrent use from model callbacks.
type requestTokenAccumulator struct {
	promptTokens     atomic.Int64
	completionTokens atomic.Int64
	steps            atomic.Int32
	lastPromptTokens atomic.Int64 // most recent call's prompt tokens (context fill indicator)
}

// Add records one LLM call's usage.
func (a *requestTokenAccumulator) Add(prompt, completion int) {
	a.promptTokens.Add(int64(prompt))
	a.completionTokens.Add(int64(completion))
	a.steps.Add(1)
	a.lastPromptTokens.Store(int64(prompt))
}

// Totals returns the accumulated usage across all steps.
func (a *requestTokenAccumulator) Totals() (prompt, completion int, steps int, lastPrompt int) {
	return int(a.promptTokens.Load()), int(a.completionTokens.Load()), int(a.steps.Load()), int(a.lastPromptTokens.Load())
}

// SessionUsage tracks cumulative token usage and cost for a session.
type SessionUsage struct {
	TotalInputTokens       int       `json:"totalInputTokens"`
	TotalOutputTokens      int       `json:"totalOutputTokens"`
	TotalRequests          int       `json:"totalRequests"`
	EstimatedCostUSD       float64   `json:"estimatedCostUsd"`
	ContextWindowSize      int       `json:"contextWindowSize"`
	LastUsagePercent       float64   `json:"lastUsagePercent"`
	LastPromptTokens       int       `json:"lastPromptTokens"`       // prompt size of the most recent LLM call (last ReAct step)
	LastRequestInputTokens int       `json:"lastRequestInputTokens"` // total prompt tokens for the last request (all ReAct steps); used for compaction
	StartedAt              time.Time `json:"startedAt"`
}

// UsageTracker maintains per-session LLM usage statistics.
type UsageTracker struct {
	mu       sync.Mutex
	sessions map[string]*SessionUsage

	model            string
	contextWindowMax int
}

// NewUsageTracker creates a tracker for the given model.
// contextWindowMax is the model's total context window size in tokens (0 = unknown).
func NewUsageTracker(model string, contextWindowMax int) *UsageTracker {
	return &UsageTracker{
		sessions:         make(map[string]*SessionUsage),
		model:            model,
		contextWindowMax: contextWindowMax,
	}
}

// Record accumulates token usage for a session after an LLM request.
// inputTokens and outputTokens are the totals for this request (summed across
// all ReAct steps). lastPromptTokens is the prompt token count from the most
// recent LLM call, which represents the current context fill level.
func (t *UsageTracker) Record(sessionKey string, inputTokens, outputTokens, lastPromptTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	usage, ok := t.sessions[sessionKey]
	if !ok {
		usage = &SessionUsage{StartedAt: time.Now().UTC()}
		t.sessions[sessionKey] = usage
	}

	usage.TotalInputTokens += inputTokens
	usage.TotalOutputTokens += outputTokens
	usage.TotalRequests++
	usage.EstimatedCostUSD += estimateCost(t.model, inputTokens, outputTokens)

	// Aggregate for this request (all ReAct steps); used for compaction threshold.
	usage.LastRequestInputTokens = inputTokens

	if t.contextWindowMax > 0 {
		usage.ContextWindowSize = t.contextWindowMax
		usage.LastPromptTokens = lastPromptTokens
		// Context fill = last call's prompt tokens / context window.
		usage.LastUsagePercent = (float64(lastPromptTokens) / float64(t.contextWindowMax)) * 100.0
		if usage.LastUsagePercent > 100.0 {
			usage.LastUsagePercent = 100.0
		}
	}
}

// Get returns a copy of the usage for a session. Returns nil if no data exists.
func (t *UsageTracker) Get(sessionKey string) *SessionUsage {
	t.mu.Lock()
	defer t.mu.Unlock()

	usage, ok := t.sessions[sessionKey]
	if !ok {
		return nil
	}
	cp := *usage
	return &cp
}

// Reset clears usage data for a session.
func (t *UsageTracker) Reset(sessionKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, sessionKey)
}

// FormatUsage returns a human-readable summary of session usage.
func (t *UsageTracker) FormatUsage(sessionKey string) string {
	usage := t.Get(sessionKey)
	if usage == nil {
		return "No usage data for this session."
	}

	var sb strings.Builder
	sb.WriteString("*Session Usage*\n\n")
	sb.WriteString(fmt.Sprintf("- Model: %s\n", t.model))
	sb.WriteString(fmt.Sprintf("- Session started: %s\n", usage.StartedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("- Total requests: %d\n", usage.TotalRequests))
	sb.WriteString(fmt.Sprintf("- Input tokens: %d\n", usage.TotalInputTokens))
	sb.WriteString(fmt.Sprintf("- Output tokens: %d\n", usage.TotalOutputTokens))
	sb.WriteString(fmt.Sprintf("- Total tokens: %d\n", usage.TotalInputTokens+usage.TotalOutputTokens))

	if usage.ContextWindowSize > 0 {
		sb.WriteString(fmt.Sprintf("- Context window: %d tokens\n", usage.ContextWindowSize))
		sb.WriteString(fmt.Sprintf("- Last context usage: %.1f%%\n", usage.LastUsagePercent))
	}

	sb.WriteString(fmt.Sprintf("- Estimated cost: $%.6f USD\n", usage.EstimatedCostUSD))

	return sb.String()
}

// FormatResponseFooter returns a compact footer with completion time, token
// counts, tokens/second, context fill, and estimated cost. Used when the
// user's skill level is above intermediate. Plain text so it works on any channel.
//
// promptTokens and completionTokens are the accumulated totals across all
// ReAct steps. steps is the number of LLM calls. lastPromptTokens is the
// prompt size of the final LLM call (context fill indicator). contextWindowMax
// is the model's context window (0 = unknown).
func FormatResponseFooter(model string, promptTokens, completionTokens, steps, lastPromptTokens, contextWindowMax int, elapsed time.Duration) string {
	secs := elapsed.Seconds()
	total := promptTokens + completionTokens

	var sb strings.Builder
	sb.WriteString("\n\n—\n")
	sb.WriteString(fmt.Sprintf("Completion: %.2f s", secs))
	if total > 0 {
		var tokPerSec float64
		if secs > 0 && completionTokens > 0 {
			tokPerSec = float64(completionTokens) / secs
		}
		cost := estimateCost(model, promptTokens, completionTokens)
		sb.WriteString(fmt.Sprintf(" · Tokens: %d (in: %d, out: %d)", total, promptTokens, completionTokens))
		if steps > 1 {
			sb.WriteString(fmt.Sprintf(" · %d steps", steps))
		}
		if tokPerSec > 0 {
			sb.WriteString(fmt.Sprintf(" · %.0f tok/s", tokPerSec))
		}
		if contextWindowMax > 0 && lastPromptTokens > 0 {
			pct := float64(lastPromptTokens) / float64(contextWindowMax) * 100.0
			sb.WriteString(fmt.Sprintf(" · Ctx: %.0f%%", pct))
		}
		if cost > 0 {
			sb.WriteString(fmt.Sprintf(" · Est. $%.6f USD", cost))
		}
	}
	return sb.String()
}

// FormatSessionUsageLine returns a one-line summary of cumulative session usage
// (total in/out tokens, context fill). Used when skill level is expert.
func FormatSessionUsageLine(usage *SessionUsage) string {
	if usage == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %d in / %d out", usage.TotalInputTokens, usage.TotalOutputTokens))
	if usage.ContextWindowSize > 0 && usage.LastUsagePercent > 0 {
		sb.WriteString(fmt.Sprintf(" · Ctx: %.0f%%", usage.LastUsagePercent))
	}
	if usage.EstimatedCostUSD > 0 {
		sb.WriteString(fmt.Sprintf(" · $%.6f USD", usage.EstimatedCostUSD))
	}
	return sb.String()
}

// modelCostPerToken maps model names/prefixes to (input, output) costs per token in USD.
// Prices as of early 2026.
var modelCostPerToken = map[string][2]float64{
	"gpt-4o":               {0.0000025, 0.000010},
	"gpt-4o-mini":          {0.00000015, 0.0000006},
	"gpt-4-turbo":          {0.000010, 0.000030},
	"gpt-4":                {0.000030, 0.000060},
	"gpt-3.5-turbo":        {0.0000005, 0.0000015},
	"claude-sonnet":        {0.000003, 0.000015},
	"claude-haiku":         {0.00000025, 0.00000125},
	"claude-opus":          {0.000015, 0.000075},
	"o1":                   {0.000015, 0.000060},
	"o1-mini":              {0.000003, 0.000012},
	"o3-mini":              {0.0000011, 0.0000044},
	"gemini-1.5-pro":       {0.00000125, 0.000005},
	"gemini-1.5-flash":     {0.000000075, 0.0000003},
	"mistral-large":        {0.000002, 0.000006},
	"mistral-small":        {0.0000002, 0.0000006},
}

func estimateCost(model string, inputTokens, outputTokens int) float64 {
	modelLower := strings.ToLower(model)

	for prefix, costs := range modelCostPerToken {
		if strings.Contains(modelLower, prefix) {
			return float64(inputTokens)*costs[0] + float64(outputTokens)*costs[1]
		}
	}

	// For local/unknown models, assume zero cost.
	return 0.0
}
