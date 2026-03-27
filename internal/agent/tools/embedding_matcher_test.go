package tools

import (
	"context"
	"math"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
)

// mockEmbedder is a test double that returns deterministic vectors.
type mockEmbedder struct {
	vectors map[string][]float64 // text → vector
	calls   int
	err     error
}

func (m *mockEmbedder) EmbedStrings(_ context.Context, texts []string, _ ...embedding.Option) ([][]float64, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	result := make([][]float64, len(texts))
	for i, t := range texts {
		if vec, ok := m.vectors[t]; ok {
			result[i] = vec
		} else {
			// Default: zero vector (no similarity to anything).
			result[i] = make([]float64, 3)
		}
	}
	return result, nil
}

// normalize returns a unit vector.
func normalize(v []float64) []float64 {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float64
		expected float64
	}{
		{"identical", []float64{1, 0, 0}, []float64{1, 0, 0}, 1.0},
		{"orthogonal", []float64{1, 0, 0}, []float64{0, 1, 0}, 0.0},
		{"opposite", []float64{1, 0, 0}, []float64{-1, 0, 0}, -1.0},
		{"empty", []float64{}, []float64{}, 0.0},
		{"zero vector", []float64{0, 0, 0}, []float64{1, 0, 0}, 0.0},
		{"mismatched length", []float64{1, 0}, []float64{1, 0, 0}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.expected) > 1e-9 {
				t.Errorf("cosineSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, got, tt.expected)
			}
		})
	}
}

func TestCatalogFingerprint(t *testing.T) {
	a := []ToolCatalogEntry{{Name: "foo"}, {Name: "bar"}}
	b := []ToolCatalogEntry{{Name: "bar"}, {Name: "foo"}}
	c := []ToolCatalogEntry{{Name: "foo"}, {Name: "baz"}}

	fpA := catalogFingerprint(a)
	fpB := catalogFingerprint(b)
	fpC := catalogFingerprint(c)

	if fpA != fpB {
		t.Error("same names in different order should have same fingerprint")
	}
	if fpA == fpC {
		t.Error("different names should have different fingerprints")
	}
}

func TestEmbeddingToolMatcher_Match(t *testing.T) {
	// Vectors: "light" tools are close to intent; "calendar" is far away.
	lightVec := normalize([]float64{0.9, 0.1, 0.0})
	calendarVec := normalize([]float64{0.0, 0.1, 0.9})
	intentVec := normalize([]float64{0.85, 0.15, 0.0}) // close to light

	emb := &mockEmbedder{
		vectors: map[string][]float64{
			"HassTurnOn: Turns on a device":                   lightVec,
			"HassLightSet: Sets brightness of a light light":  lightVec,
			"manage_event: Create or delete a calendar event": calendarVec,
			"enciende la luz":                                 intentVec,
		},
	}

	catalog := []ToolCatalogEntry{
		{Name: "HassTurnOn", Description: "Turns on a device"},
		{Name: "HassLightSet", Description: "Sets brightness of a light", Tags: []string{"light"}},
		{Name: "manage_event", Description: "Create or delete a calendar event"},
	}

	matcher := NewEmbeddingToolMatcher(emb, 0.3)
	names, err := matcher.Match(context.Background(), "enciende la luz", catalog, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both light tools should match (high similarity), calendar should not.
	if len(names) < 1 {
		t.Fatal("expected at least 1 match")
	}
	found := make(map[string]bool)
	for _, n := range names {
		found[n] = true
	}
	if !found["HassTurnOn"] {
		t.Error("expected HassTurnOn in results")
	}
	if found["manage_event"] {
		t.Error("did not expect manage_event in results")
	}
}

func TestEmbeddingToolMatcher_EmptyInput(t *testing.T) {
	emb := &mockEmbedder{vectors: map[string][]float64{}}
	matcher := NewEmbeddingToolMatcher(emb, 0.3)

	names, err := matcher.Match(context.Background(), "", nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Errorf("expected nil for empty input, got %v", names)
	}
}

func TestEmbeddingToolMatcher_CachesVectors(t *testing.T) {
	vec := normalize([]float64{1, 0, 0})
	emb := &mockEmbedder{
		vectors: map[string][]float64{
			"foo: a tool":  vec,
			"intent text":  vec,
			"intent text2": vec,
		},
	}

	catalog := []ToolCatalogEntry{{Name: "foo", Description: "a tool"}}
	matcher := NewEmbeddingToolMatcher(emb, 0.3)

	// First call: indexes catalog (1 call) + embeds intent (1 call) = 2.
	_, _ = matcher.Match(context.Background(), "intent text", catalog, 10)
	if emb.calls != 2 {
		t.Errorf("expected 2 embed calls on first match, got %d", emb.calls)
	}

	// Second call with same catalog: only embeds intent (1 call) = 3 total.
	_, _ = matcher.Match(context.Background(), "intent text2", catalog, 10)
	if emb.calls != 3 {
		t.Errorf("expected 3 embed calls on second match (cached catalog), got %d", emb.calls)
	}
}

func TestEmbeddingToolMatcher_MaxResults(t *testing.T) {
	vec := normalize([]float64{1, 0, 0})
	emb := &mockEmbedder{
		vectors: map[string][]float64{
			"a: tool a": vec,
			"b: tool b": vec,
			"c: tool c": vec,
			"intent":    vec,
		},
	}

	catalog := []ToolCatalogEntry{
		{Name: "a", Description: "tool a"},
		{Name: "b", Description: "tool b"},
		{Name: "c", Description: "tool c"},
	}
	matcher := NewEmbeddingToolMatcher(emb, 0.3)
	names, err := matcher.Match(context.Background(), "intent", catalog, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(names))
	}
}

func TestHybridToolMatcher_BlendsResults(t *testing.T) {
	// Primary returns: [A, B], Fallback returns: [B, C]
	primary := &staticMatcher{results: []string{"A", "B"}}
	fallback := &staticMatcher{results: []string{"B", "C"}}

	hybrid := &HybridToolMatcher{Primary: primary, Fallback: fallback, Alpha: 0.6}
	names, err := hybrid.Match(context.Background(), "test", nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// B appears in both, should be ranked first.
	if len(names) < 1 || names[0] != "B" {
		t.Errorf("expected B first (appears in both), got %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 unique results, got %d", len(names))
	}
}

func TestHybridToolMatcher_FallbackOnPrimaryError(t *testing.T) {
	primary := &errorMatcher{err: context.DeadlineExceeded}
	fallback := &staticMatcher{results: []string{"X", "Y"}}

	hybrid := &HybridToolMatcher{Primary: primary, Fallback: fallback, Alpha: 0.5}
	names, err := hybrid.Match(context.Background(), "test", nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 || names[0] != "X" {
		t.Errorf("expected fallback results [X Y], got %v", names)
	}
}

func TestHybridToolMatcher_MaxResults(t *testing.T) {
	primary := &staticMatcher{results: []string{"A", "B", "C"}}
	fallback := &staticMatcher{results: []string{"D", "E"}}

	hybrid := &HybridToolMatcher{Primary: primary, Fallback: fallback, Alpha: 0.5}
	names, err := hybrid.Match(context.Background(), "test", nil, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(names))
	}
}

// --- test helpers ---

type staticMatcher struct {
	results []string
}

func (m *staticMatcher) Match(_ context.Context, _ string, _ []ToolCatalogEntry, _ int) ([]string, error) {
	return m.results, nil
}

type errorMatcher struct {
	err error
}

func (m *errorMatcher) Match(_ context.Context, _ string, _ []ToolCatalogEntry, _ int) ([]string, error) {
	return nil, m.err
}
