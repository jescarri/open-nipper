package enrich

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jescarri/open-nipper/internal/config"
)

func TestSpeechEnricher_Supports(t *testing.T) {
	e := NewSpeechEnricher("http://localhost:9090", 0, config.S3DefaultConfig{})

	if !e.Supports("audio") {
		t.Error("expected Supports(\"audio\") to be true")
	}
	if e.Supports("image") {
		t.Error("expected Supports(\"image\") to be false")
	}
	if e.Supports("text") {
		t.Error("expected Supports(\"text\") to be false")
	}
}

func TestSpeechEnricher_Enrich_Success(t *testing.T) {
	// Set up a fake whisper.cpp server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			t.Errorf("expected /inference path, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("expected multipart/form-data, got %s", ct)
		}

		// Parse multipart to verify fields.
		if err := r.ParseMultipartForm(10 * 1024 * 1024); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("expected file field: %v", err)
		}
		fileBytes, _ := io.ReadAll(file)
		file.Close()

		if string(fileBytes) != "fake-audio-data" {
			t.Errorf("unexpected file content: %q", string(fileBytes))
		}

		if r.FormValue("temperature") != "0.0" {
			t.Errorf("expected temperature=0.0, got %s", r.FormValue("temperature"))
		}
		if r.FormValue("response_format") != "json" {
			t.Errorf("expected response_format=json, got %s", r.FormValue("response_format"))
		}

		resp := whisperResponse{Text: " Hello, what is the weather in Buenos Aires? "}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := NewSpeechEnricher(server.URL, 0, config.S3DefaultConfig{})
	e.FetchAudioFunc = func(_ context.Context, url string) ([]byte, error) {
		return []byte("fake-audio-data"), nil
	}

	result, err := e.Enrich(context.Background(), ContentPartView{
		Type:     "audio",
		URL:      "s3://bucket/voice.ogg",
		MimeType: "audio/ogg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Hello, what is the weather in Buenos Aires?"
	if result.Transcript != expected {
		t.Errorf("expected %q, got %q", expected, result.Transcript)
	}
}

func TestSpeechEnricher_Enrich_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer server.Close()

	e := NewSpeechEnricher(server.URL, 0, config.S3DefaultConfig{})
	e.FetchAudioFunc = func(_ context.Context, url string) ([]byte, error) {
		return []byte("fake-audio-data"), nil
	}

	_, err := e.Enrich(context.Background(), ContentPartView{
		Type: "audio",
		URL:  "https://example.com/voice.ogg",
	})
	if err == nil {
		t.Fatal("expected error for server 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention 500, got: %v", err)
	}
}

func TestSpeechEnricher_Enrich_EmptyURL(t *testing.T) {
	e := NewSpeechEnricher("http://localhost:9090", 0, config.S3DefaultConfig{})

	_, err := e.Enrich(context.Background(), ContentPartView{
		Type: "audio",
		URL:  "",
	})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestSpeechEnricher_Enrich_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	e := NewSpeechEnricher(server.URL, 0, config.S3DefaultConfig{})
	e.FetchAudioFunc = func(_ context.Context, url string) ([]byte, error) {
		return []byte("fake-audio-data"), nil
	}

	_, err := e.Enrich(context.Background(), ContentPartView{
		Type: "audio",
		URL:  "https://example.com/voice.ogg",
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSpeechEnricher_Enrich_EmptyTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: "   "})
	}))
	defer server.Close()

	e := NewSpeechEnricher(server.URL, 0, config.S3DefaultConfig{})
	e.FetchAudioFunc = func(_ context.Context, url string) ([]byte, error) {
		return []byte("fake-audio-data"), nil
	}

	result, err := e.Enrich(context.Background(), ContentPartView{
		Type: "audio",
		URL:  "https://example.com/voice.ogg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Transcript != "" {
		t.Errorf("expected empty transcript for whitespace-only response, got %q", result.Transcript)
	}
}

func TestSpeechEnricher_Enrich_UsesS3Fetcher_ForHTTPURL(t *testing.T) {
	// Verify that when no FetchAudioFunc is set, the enricher uses the s3Fetcher.
	// We set up a plain HTTP server and configure S3 endpoint to NOT match,
	// so the fetcher falls back to HTTP GET.
	audioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("audio-bytes-from-http"))
	}))
	defer audioServer.Close()

	whisperServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "audio-bytes-from-http") {
			t.Errorf("expected audio bytes from HTTP, got different content")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(whisperResponse{Text: "transcribed"})
	}))
	defer whisperServer.Close()

	e := NewSpeechEnricher(whisperServer.URL, 0, config.S3DefaultConfig{
		Endpoint: "not-matching.example.com",
	})

	result, err := e.Enrich(context.Background(), ContentPartView{
		Type: "audio",
		URL:  audioServer.URL + "/audio.ogg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Transcript != "transcribed" {
		t.Errorf("expected 'transcribed', got %q", result.Transcript)
	}
}
