package tools_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jescarri/open-nipper/internal/agent/tools"
	"github.com/jescarri/open-nipper/internal/config"
)

func TestWebFetch_HTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
  <head><title>Test Page</title></head>
  <body>
    <h1>Hello World</h1>
    <p>This is a test page with some content for readability extraction.</p>
    <p>Open-Nipper is a multi-channel AI gateway.</p>
  </body>
</html>`))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status code: got %d, want 200", result.StatusCode)
	}
	if result.Body == "" {
		t.Error("expected non-empty body")
	}
	if !strings.Contains(result.Body, "Open-Nipper") {
		t.Errorf("expected body to contain 'Open-Nipper', got: %q", result.Body[:min(200, len(result.Body))])
	}
}

func TestWebFetch_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello from plain text"))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Body, "Hello from plain text") {
		t.Errorf("expected plain text body, got: %q", result.Body)
	}
}

func TestWebFetch_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", result.StatusCode)
	}
}

func TestWebFetch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // longer than context timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := tools.ExecWebFetch(ctx, tools.WebFetchParams{URL: srv.URL})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWebFetch_InvalidURL(t *testing.T) {
	_, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: "not-a-valid-url"})
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

func TestWebFetch_EmptyURL(t *testing.T) {
	_, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{})
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestWebFetch_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{
		URL:     srv.URL,
		Headers: map[string]string{"X-Test-Header": "hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedHeaders.Get("X-Test-Header") != "hello" {
		t.Errorf("expected X-Test-Header=hello, got %q", receivedHeaders.Get("X-Test-Header"))
	}
}

func TestWebFetch_JSRenderedPage_EmptyReadability(t *testing.T) {
	// Simulate a JS-rendered SPA where the body is only script/style/noscript — no
	// readable text at all. Readability returns empty TextContent for these pages.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title></title>
<link rel="stylesheet" href="/s.css"><script defer src="/app.js"></script></head>
<body><div id="root"></div><noscript>Enable JavaScript</noscript></body>
</html>`))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status code: got %d, want 200", result.StatusCode)
	}
	if result.Body == "" {
		t.Error("expected non-empty body (fallback message), got empty")
	}
	// Body should be either the noscript fallback or the JS extraction message.
	if !strings.Contains(result.Body, "Enable JavaScript") &&
		!strings.Contains(result.Body, "could not be extracted") {
		t.Errorf("expected fallback text, got: %q", result.Body)
	}
}

func TestWebFetch_HTML_ReadabilityEmptyButTagsHaveText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
  <head><title>Sparse Page</title></head>
  <body>
    <nav>Home | About | Contact</nav>
    <footer>Copyright 2026</footer>
  </body>
</html>`))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Body == "" {
		t.Error("expected non-empty body from tag stripping fallback")
	}
	if strings.Contains(result.Body, "<nav>") {
		t.Error("body should not contain raw HTML tags")
	}
}

func TestWebFetch_ScriptContentStripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
  <title>What is Kubernetes?</title>
  <script>performance.mark('HEAD Start');var pageData={"pageCategory":"topics","pageName":"rh|what-is-kubernetes"};</script>
  <style>body{font-family:sans-serif}.hidden{display:none}</style>
</head>
<body>
  <script>var app=function(){console.log("init")};</script>
  <div id="content">
    <h1>What is Kubernetes?</h1>
    <p>Kubernetes is an open-source container orchestration platform.</p>
  </div>
  <script src="/analytics.js"></script>
</body>
</html>`))
	}))
	defer srv.Close()

	result, err := tools.ExecWebFetch(context.Background(), tools.WebFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Body, "performance.mark") {
		t.Error("body should not contain script content (performance.mark)")
	}
	if strings.Contains(result.Body, "pageCategory") {
		t.Error("body should not contain script content (pageCategory)")
	}
	if strings.Contains(result.Body, "font-family") {
		t.Error("body should not contain style content")
	}
	if !strings.Contains(result.Body, "Kubernetes") {
		t.Error("body should contain actual page text (Kubernetes)")
	}
}

func TestBuildWebFetchTool(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{WebFetch: true},
	}
	builtTools, err := tools.BuildTools(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error building tools: %v", err)
	}
	names := make(map[string]bool)
	for _, bt := range builtTools {
		info, _ := bt.Info(ctx)
		names[info.Name] = true
	}
	if !names["web_fetch"] {
		t.Error("missing web_fetch tool")
	}
	if !names["get_datetime"] {
		t.Error("missing get_datetime tool (always enabled)")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
