package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jescarri/open-nipper/internal/config"
)

const probeTimeoutSec = 10

// ModelCapabilities holds the server-reported properties of the loaded model.
// Fields are best-effort: different servers expose different subsets.
type ModelCapabilities struct {
	// ID is the model identifier returned by the server.
	ID string
	// MaxContextLength is the maximum total token context (prompt + completion).
	// 0 means the server did not report it.
	MaxContextLength int
	// State is the load state as reported by LM Studio ("loaded", "not-loaded", etc.).
	// Empty on servers that do not report state.
	State string
	// Architecture is the model architecture (e.g. "gemma3", "llama").
	// Empty on servers that do not report it.
	Architecture string
	// Quantization is the GGUF quantization level (e.g. "Q4_K_M").
	// Empty on servers that do not report it.
	Quantization string
	// Source identifies which API endpoint delivered this info.
	Source string
}

// ProbeModelCapabilities queries the inference server for information about the
// configured model. It tries, in order:
//
//  1. GET {base_url}/models/{model_id}   — model-specific endpoint
//  2. GET {base_url}/models              — list endpoint (picks the entry matching cfg.Model)
//
// Both are standard OpenAI-compatible endpoints. LM Studio additionally returns
// max_context_length, state, arch and quantization in the list endpoint.
//
// This is a best-effort probe: a non-nil error means nothing could be reached;
// a nil error with partial zero-value fields means the server responded but
// did not expose those fields.
func ProbeModelCapabilities(ctx context.Context, cfg config.InferenceConfig) (*ModelCapabilities, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		// OpenAI and Azure don't expose context sizes publicly; skip silently.
		return nil, fmt.Errorf("no base_url configured (OpenAI/Azure); skipping probe")
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local"
	}

	client := &http.Client{Timeout: probeTimeoutSec * time.Second}

	// --- attempt 1: /models/{model_id} ---
	cap, err := probeModelEndpoint(ctx, client, baseURL, cfg.Model, apiKey)
	if err == nil {
		cap.Source = "GET /models/" + cfg.Model
		return cap, nil
	}

	// --- attempt 2: /models list ---
	cap, err = probeModelsList(ctx, client, baseURL, cfg.Model, apiKey)
	if err == nil {
		cap.Source = "GET /models"
		return cap, nil
	}

	return nil, fmt.Errorf("model probe failed (tried /models/{id} and /models): %w", err)
}

// --- raw API response shapes ---

// openAIModelObject covers the common OpenAI fields plus vendor extensions.
type openAIModelObject struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	// LM Studio extensions
	State            string `json:"state"`
	Arch             string `json:"arch"`
	Quantization     string `json:"quantization"`
	MaxContextLength int    `json:"max_context_length"`
	// vLLM extension
	MaxModelLen int `json:"max_model_len"`
	// llama.cpp / Ollama-style: context size in data[].meta.n_ctx_train
	Meta *struct {
		NCtxTrain int `json:"n_ctx_train"`
	} `json:"meta"`
}

type openAIModelList struct {
	Data []openAIModelObject `json:"data"`
}

func probeModelEndpoint(ctx context.Context, client *http.Client, baseURL, modelID, apiKey string) (*ModelCapabilities, error) {
	url := baseURL + "/models/" + modelID
	body, err := doGet(ctx, client, url, apiKey)
	if err != nil {
		return nil, err
	}
	var obj openAIModelObject
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("parse /models/{id} response: %w", err)
	}
	if obj.ID == "" {
		return nil, fmt.Errorf("model not found in /models/{id} response")
	}
	return modelObjectToCapabilities(obj), nil
}

func probeModelsList(ctx context.Context, client *http.Client, baseURL, modelID, apiKey string) (*ModelCapabilities, error) {
	url := baseURL + "/models"
	body, err := doGet(ctx, client, url, apiKey)
	if err != nil {
		return nil, err
	}
	var list openAIModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse /models response: %w", err)
	}
	// Prefer exact match; fall back to first entry if the list has exactly one model.
	for _, obj := range list.Data {
		if obj.ID == modelID {
			return modelObjectToCapabilities(obj), nil
		}
	}
	if len(list.Data) == 1 {
		return modelObjectToCapabilities(list.Data[0]), nil
	}
	return nil, fmt.Errorf("model %q not found in /models list (%d entries)", modelID, len(list.Data))
}

func modelObjectToCapabilities(obj openAIModelObject) *ModelCapabilities {
	maxCtx := obj.MaxContextLength
	// vLLM reports max_model_len instead of max_context_length.
	if maxCtx == 0 && obj.MaxModelLen > 0 {
		maxCtx = obj.MaxModelLen
	}
	// llama.cpp / Ollama-style: data[].meta.n_ctx_train
	if maxCtx == 0 && obj.Meta != nil && obj.Meta.NCtxTrain > 0 {
		maxCtx = obj.Meta.NCtxTrain
	}
	return &ModelCapabilities{
		ID:               obj.ID,
		MaxContextLength: maxCtx,
		State:            obj.State,
		Architecture:     obj.Arch,
		Quantization:     obj.Quantization,
	}
}

func doGet(ctx context.Context, client *http.Client, url, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}
