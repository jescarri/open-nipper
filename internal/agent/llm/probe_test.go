package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jescarri/open-nipper/internal/agent/llm"
	"github.com/jescarri/open-nipper/internal/config"
)

// lmStudioModelList mimics the LM Studio /v1/models response.
var lmStudioModelList = map[string]any{
	"object": "list",
	"data": []map[string]any{
		{
			"id":                 "google/gemma-3-4b",
			"object":             "model",
			"type":               "llm",
			"publisher":          "google",
			"arch":               "gemma3",
			"compatibility_type": "gguf",
			"quantization":       "Q4_K_M",
			"state":              "loaded",
			"max_context_length": 131072,
		},
	},
}

func TestProbeModelCapabilities_ListEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models/google/gemma-3-4b":
			// Simulate 404 so the probe falls back to the list endpoint.
			http.NotFound(w, r)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(lmStudioModelList)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "google/gemma-3-4b",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "local",
	}

	cap, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.ID != "google/gemma-3-4b" {
		t.Errorf("expected ID google/gemma-3-4b, got %q", cap.ID)
	}
	if cap.MaxContextLength != 131072 {
		t.Errorf("expected MaxContextLength 131072, got %d", cap.MaxContextLength)
	}
	if cap.State != "loaded" {
		t.Errorf("expected State loaded, got %q", cap.State)
	}
	if cap.Architecture != "gemma3" {
		t.Errorf("expected Architecture gemma3, got %q", cap.Architecture)
	}
	if cap.Quantization != "Q4_K_M" {
		t.Errorf("expected Quantization Q4_K_M, got %q", cap.Quantization)
	}
	if cap.Source == "" {
		t.Error("expected non-empty Source")
	}
}

func TestProbeModelCapabilities_ModelEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models/mymodel" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                 "mymodel",
				"object":             "model",
				"max_context_length": 8192,
				"state":              "loaded",
			})
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "mymodel",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "test",
	}

	cap, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.MaxContextLength != 8192 {
		t.Errorf("expected 8192, got %d", cap.MaxContextLength)
	}
	if cap.State != "loaded" {
		t.Errorf("expected loaded, got %q", cap.State)
	}
}

// llamaCppStyleList mimics a /models response with data[].meta.n_ctx_train (llama.cpp / identitylabs style).
var llamaCppStyleList = map[string]any{
	"object": "list",
	"data": []map[string]any{
		{
			"id":     "Qwen3-Next-80B-A3B-Instruct-UD-Q8_K_XL-00001-of-00002.gguf",
			"object": "model",
			"meta": map[string]any{
				"n_ctx_train": 262144,
				"n_vocab":     151936,
			},
		},
	},
}

func TestProbeModelCapabilities_MetaNCtxTrain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(llamaCppStyleList)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "Qwen3-Next-80B-A3B-Instruct-UD-Q8_K_XL-00001-of-00002.gguf",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "local",
	}

	cap, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.MaxContextLength != 262144 {
		t.Errorf("expected MaxContextLength 262144 from meta.n_ctx_train, got %d", cap.MaxContextLength)
	}
}

func TestProbeModelCapabilities_PropsEndpoint(t *testing.T) {
	propsBody := map[string]any{
		"default_generation_settings": map[string]any{
			"n_ctx": 131072,
		},
		"model_alias": "gpt-oss-120b-F16.gguf",
		"model_path": "/models/gpt-oss-120b-F16.gguf",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(propsBody)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "gpt-oss-120b-F16.gguf",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "local",
	}

	cap, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.MaxContextLength != 131072 {
		t.Errorf("expected MaxContextLength 131072 from /props n_ctx, got %d", cap.MaxContextLength)
	}
	if cap.ID != "gpt-oss-120b-F16.gguf" {
		t.Errorf("expected ID gpt-oss-120b-F16.gguf from model_alias, got %q", cap.ID)
	}
	if cap.Source != "GET /props" {
		t.Errorf("expected Source GET /props, got %q", cap.Source)
	}
	if cap.State != "" || cap.Architecture != "" || cap.Quantization != "" {
		t.Errorf("props does not set state/arch/quantization; got state=%q arch=%q quant=%q", cap.State, cap.Architecture, cap.Quantization)
	}
}

func TestProbeModelCapabilities_PropsFallback(t *testing.T) {
	// Server has no /props (404); /v1/models returns one model. Probe should fall back to /models.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			http.NotFound(w, r)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "fallback-model", "object": "model", "max_context_length": 8192},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "fallback-model",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "local",
	}

	cap, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.Source != "GET /models" {
		t.Errorf("expected fallback Source GET /models, got %q", cap.Source)
	}
	if cap.MaxContextLength != 8192 {
		t.Errorf("expected MaxContextLength 8192 from fallback, got %d", cap.MaxContextLength)
	}
	if cap.ID != "fallback-model" {
		t.Errorf("expected ID fallback-model, got %q", cap.ID)
	}
}

func TestProbeModelCapabilities_NoBaseURL(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "openai",
		Model:    "gpt-4o",
		APIKey:   "sk-test",
	}
	_, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when no base_url is set")
	}
}

func TestProbeModelCapabilities_ServerUnreachable(t *testing.T) {
	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "some-model",
		BaseURL:  "http://127.0.0.1:19999/v1", // nothing listening here
		APIKey:   "local",
	}
	_, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

func TestProbeModelCapabilities_ModelNotInList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			// Return a list that does NOT contain the requested model.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "other-model", "object": "model"},
					{"id": "another-model", "object": "model"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.InferenceConfig{
		Provider: "local",
		Model:    "missing-model",
		BaseURL:  srv.URL + "/v1",
		APIKey:   "local",
	}
	_, err := llm.ProbeModelCapabilities(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when model is not in list")
	}
}
