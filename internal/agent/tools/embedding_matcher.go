package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/embedding"
)

// ---------- EmbeddingToolMatcher ----------

// EmbeddingToolMatcher implements ToolMatcher using cosine similarity
// between the user intent and tool description embeddings.
type EmbeddingToolMatcher struct {
	embedder  embedding.Embedder
	threshold float64

	mu         sync.RWMutex
	cachedVecs map[string][]float64 // tool name → vector
	cachedFP   uint64               // fingerprint of indexed catalog
}

// NewEmbeddingToolMatcher creates an EmbeddingToolMatcher.
// threshold is the minimum cosine similarity score (0–1) to include a tool.
func NewEmbeddingToolMatcher(embedder embedding.Embedder, threshold float64) *EmbeddingToolMatcher {
	return &EmbeddingToolMatcher{
		embedder:  embedder,
		threshold: threshold,
	}
}

// Match returns tool names whose embeddings are semantically similar to the intent.
func (m *EmbeddingToolMatcher) Match(ctx context.Context, intent string,
	catalog []ToolCatalogEntry, maxResults int) ([]string, error) {

	if intent == "" || len(catalog) == 0 {
		return nil, nil
	}

	// Compute catalog fingerprint; re-index if changed.
	fp := catalogFingerprint(catalog)

	m.mu.RLock()
	needsReindex := m.cachedFP != fp
	m.mu.RUnlock()

	if needsReindex {
		if err := m.indexCatalog(ctx, catalog, fp); err != nil {
			return nil, fmt.Errorf("embedding index: %w", err)
		}
	}

	// Embed user intent.
	vecs, err := m.embedder.EmbedStrings(ctx, []string{intent})
	if err != nil {
		return nil, fmt.Errorf("embed intent: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, nil
	}
	intentVec := vecs[0]

	// Score against cached vectors.
	type scored struct {
		name  string
		score float64
	}

	m.mu.RLock()
	var matches []scored
	for name, vec := range m.cachedVecs {
		sim := cosineSimilarity(intentVec, vec)
		if sim >= m.threshold {
			matches = append(matches, scored{name: name, score: sim})
		}
	}
	m.mu.RUnlock()

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	names := make([]string, len(matches))
	for i, s := range matches {
		names[i] = s.name
	}
	return names, nil
}

func (m *EmbeddingToolMatcher) indexCatalog(ctx context.Context, catalog []ToolCatalogEntry, fp uint64) error {
	texts := make([]string, len(catalog))
	for i, entry := range catalog {
		text := entry.Name + ": " + entry.Description
		if len(entry.Tags) > 0 {
			for _, tag := range entry.Tags {
				text += " " + tag
			}
		}
		texts[i] = text
	}

	vecs, err := m.embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return err
	}

	cached := make(map[string][]float64, len(catalog))
	for i, entry := range catalog {
		if i < len(vecs) {
			cached[entry.Name] = vecs[i]
		}
	}

	m.mu.Lock()
	m.cachedVecs = cached
	m.cachedFP = fp
	m.mu.Unlock()
	return nil
}

// catalogFingerprint computes an FNV hash of sorted tool names.
func catalogFingerprint(catalog []ToolCatalogEntry) uint64 {
	names := make([]string, len(catalog))
	for i, e := range catalog {
		names[i] = e.Name
	}
	sort.Strings(names)
	h := fnv.New64a()
	for _, n := range names {
		_, _ = h.Write([]byte(n))
	}
	return h.Sum64()
}

// cosineSimilarity returns the cosine similarity between two vectors.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ---------- HybridToolMatcher ----------

// HybridToolMatcher implements ToolMatcher by composing a primary (embedding)
// and fallback (keyword) matcher using reciprocal rank fusion.
type HybridToolMatcher struct {
	Primary  ToolMatcher // embedding-based
	Fallback ToolMatcher // keyword-based
	Alpha    float64     // blend weight: 0 = fallback only, 1 = primary only
}

// Match runs both matchers and blends results using reciprocal rank fusion.
// If the primary matcher fails, gracefully degrades to fallback only.
func (m *HybridToolMatcher) Match(ctx context.Context, intent string,
	catalog []ToolCatalogEntry, maxResults int) ([]string, error) {

	primaryResult, primaryErr := m.Primary.Match(ctx, intent, catalog, 0)
	fallbackResult, fallbackErr := m.Fallback.Match(ctx, intent, catalog, 0)

	// Graceful degradation: if primary fails, use fallback.
	if primaryErr != nil {
		if fallbackErr != nil {
			return nil, fmt.Errorf("both matchers failed: primary: %w, fallback: %v", primaryErr, fallbackErr)
		}
		if maxResults > 0 && len(fallbackResult) > maxResults {
			return fallbackResult[:maxResults], nil
		}
		return fallbackResult, nil
	}

	// Blend by reciprocal rank.
	scores := make(map[string]float64)
	for i, name := range primaryResult {
		scores[name] += m.Alpha * (1.0 / float64(i+1))
	}
	for i, name := range fallbackResult {
		scores[name] += (1 - m.Alpha) * (1.0 / float64(i+1))
	}

	type scored struct {
		name  string
		score float64
	}
	merged := make([]scored, 0, len(scores))
	for name, score := range scores {
		merged = append(merged, scored{name: name, score: score})
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	if maxResults > 0 && len(merged) > maxResults {
		merged = merged[:maxResults]
	}

	names := make([]string, len(merged))
	for i, s := range merged {
		names[i] = s.name
	}
	return names, nil
}

// ---------- OpenAI-compatible embedder ----------

// OpenAIEmbedder implements embedding.Embedder for any OpenAI-compatible endpoint.
// Works with Ollama, LocalAI, llama.cpp, vLLM, and OpenAI.
type OpenAIEmbedder struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewOpenAIEmbedder creates an embedder that calls any OpenAI-compatible
// /v1/embeddings endpoint. If baseURL is empty, defaults to OpenAI.
func NewOpenAIEmbedder(baseURL, model, apiKey string) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIEmbedder{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

type openAIEmbeddingRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type openAIEmbeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// EmbedStrings calls the /embeddings endpoint and returns vectors.
func (e *OpenAIEmbedder) EmbedStrings(ctx context.Context, texts []string, _ ...embedding.Option) ([][]float64, error) {
	body, err := json.Marshal(openAIEmbeddingRequest{Input: texts, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	url := e.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding HTTP call: %w", err)
	}
	defer resp.Body.Close()

	var result openAIEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("status %d", resp.StatusCode)
		if result.Error != nil {
			msg += ": " + result.Error.Message
		}
		return nil, fmt.Errorf("embedding API error: %s", msg)
	}

	// Sort by index to preserve input order.
	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	vectors := make([][]float64, len(result.Data))
	for i, item := range result.Data {
		vectors[i] = item.Embedding
	}
	return vectors, nil
}

// Ping verifies connectivity by embedding a test string.
func (e *OpenAIEmbedder) Ping(ctx context.Context) error {
	_, err := e.EmbedStrings(ctx, []string{"ping"})
	return err
}
