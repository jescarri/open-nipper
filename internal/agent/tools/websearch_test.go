package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jescarri/open-nipper/internal/agent/tools"
	"github.com/jescarri/open-nipper/internal/config"
)

// ddgConfig returns a WebSearchConfig with only DuckDuckGo enabled (for tests).
func ddgConfig() config.WebSearchConfig {
	return config.WebSearchConfig{DuckDuckGo: config.WebSearchEngineDuckDuckGo{Enabled: true}}
}

// googleConfig returns a WebSearchConfig with only Google enabled (for tests).
func googleConfig(apiKey, cx string) config.WebSearchConfig {
	return config.WebSearchConfig{
		Google: config.WebSearchEngineGoogle{Enabled: true, GoogleAPIKey: apiKey, GoogleCX: cx},
	}
}

// --- DuckDuckGo tests ---

func TestWebSearch_DDG_ParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakeDDGHTML))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query:      "golang testing",
		MaxResults: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Engine != "duckduckgo" {
		t.Errorf("expected engine 'duckduckgo', got %q", result.Engine)
	}
	if result.Query != "golang testing" {
		t.Errorf("expected query 'golang testing', got %q", result.Query)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}

	r0 := result.Results[0]
	if r0.Title != "ESP32 Series - Mouser Canada" {
		t.Errorf("result[0] title = %q, want 'ESP32 Series - Mouser Canada'", r0.Title)
	}
	if r0.URL != "https://www.mouser.ca/c/?series=ESP32" {
		t.Errorf("result[0] URL = %q, want 'https://www.mouser.ca/c/?series=ESP32'", r0.URL)
	}
	if !strings.Contains(r0.Snippet, "ESP32 Series are available") {
		t.Errorf("result[0] snippet = %q, want to contain 'ESP32 Series are available'", r0.Snippet)
	}

	r1 := result.Results[1]
	if r1.Title != "Espressif Systems Distributor | DigiKey" {
		t.Errorf("result[1] title = %q, want 'Espressif Systems Distributor | DigiKey'", r1.Title)
	}
	if r1.URL != "https://www.digikey.ca/en/supplier-centers/espressif-systems" {
		t.Errorf("result[1] URL = %q", r1.URL)
	}
}

func TestWebSearch_DDG_DefaultEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakeDDGHTML))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: "test query",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Engine != "duckduckgo" {
		t.Errorf("expected default engine 'duckduckgo', got %q", result.Engine)
	}
}

func TestWebSearch_DDG_MaxResultsCapped(t *testing.T) {
	resultCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakeDDGHTMLManyResults))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query:      "many results",
		MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resultCount = len(result.Results)
	if resultCount > 2 {
		t.Errorf("expected at most 2 results, got %d", resultCount)
	}
}

func TestWebSearch_DDG_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: "test",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestWebSearch_DDG_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	_, err := executor.ExecWebSearch(ctx, tools.WebSearchParams{
		Query: "test",
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Google tests ---

func TestWebSearch_Google_ParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		q := r.URL.Query()
		if q.Get("key") != "test-api-key" {
			t.Errorf("expected key=test-api-key, got %q", q.Get("key"))
		}
		if q.Get("cx") != "test-cx" {
			t.Errorf("expected cx=test-cx, got %q", q.Get("cx"))
		}
		if q.Get("q") != "golang channels" {
			t.Errorf("expected q='golang channels', got %q", q.Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(fakeGoogleResponse)
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithGoogleEndpoint(
		googleConfig("test-api-key", "test-cx"),
		"google",
		"",
		srv.URL,
	)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: "golang channels",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Engine != "google" {
		t.Errorf("expected engine 'google', got %q", result.Engine)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	if result.Results[0].Title != "Go Channels Tutorial" {
		t.Errorf("expected first title 'Go Channels Tutorial', got %q", result.Results[0].Title)
	}
	if result.Results[0].URL != "https://example.com/go-channels" {
		t.Errorf("unexpected URL: %q", result.Results[0].URL)
	}
	if result.Results[1].Snippet != "Advanced channel patterns in Go." {
		t.Errorf("unexpected snippet: %q", result.Results[1].Snippet)
	}
}

func TestWebSearch_Google_ErrorsWhenNoCredentials(t *testing.T) {
	// When Google is the configured engine but API key/cx are missing, executor returns an error (no fallback).
	executor := tools.NewWebSearchExecutor(googleConfig("", ""), "google")
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected error when Google is enabled but credentials are missing")
	}
	if !strings.Contains(err.Error(), "google_api_key") {
		t.Errorf("error should mention google_api_key, got: %v", err)
	}
}

func TestWebSearch_Google_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"API key invalid"}}`))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithGoogleEndpoint(
		googleConfig("bad-key", "test-cx"),
		"google",
		"",
		srv.URL,
	)
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: "test",
	})
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

// --- Validation tests ---

func TestWebSearch_EmptyQuery(t *testing.T) {
	executor := tools.NewWebSearchExecutor(ddgConfig(), "duckduckgo")
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestWebSearch_QueryTooLong(t *testing.T) {
	executor := tools.NewWebSearchExecutor(ddgConfig(), "duckduckgo")
	longQuery := strings.Repeat("a", 501)
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: longQuery,
	})
	if err == nil {
		t.Fatal("expected error for query over 500 characters")
	}
}

func TestWebSearch_UnknownEngineUsesConfiguredEngine(t *testing.T) {
	// Executor uses only the configured engine (duckduckgo); params.Engine is ignored.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakeDDGHTML))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query:  "test",
		Engine: "bing", // ignored; configured engine is duckduckgo
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Engine != "duckduckgo" {
		t.Errorf("expected engine 'duckduckgo', got %q", result.Engine)
	}
}

func TestWebSearch_DefaultMaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fakeDDGHTMLManyResults))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query: "test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) > 5 {
		t.Errorf("default max should be 5, got %d results", len(result.Results))
	}
}

func TestWebSearch_MaxResultsClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		num := q.Get("num")
		if num != "10" {
			t.Errorf("expected num=10 (clamped from 50), got %q", num)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fakeGoogleResponse)
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithGoogleEndpoint(
		googleConfig("key", "cx"),
		"google",
		"",
		srv.URL,
	)
	_, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{
		Query:      "test",
		MaxResults: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- BuildTools integration test ---

func TestBuildWebSearchTool(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{
			WebSearch: true,
			WebSearchConfig: config.WebSearchConfig{
				DuckDuckGo: config.WebSearchEngineDuckDuckGo{Enabled: true},
			},
		},
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
	if !names["web_search"] {
		t.Error("missing web_search tool")
	}
}

func TestBuildWebSearchTool_MutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{
			WebSearch: true,
			WebSearchConfig: config.WebSearchConfig{
				Google:    config.WebSearchEngineGoogle{Enabled: true, GoogleAPIKey: "k", GoogleCX: "c"},
				DuckDuckGo: config.WebSearchEngineDuckDuckGo{Enabled: true},
			},
		},
	}
	_, err := tools.BuildTools(ctx, cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error when both google and duck_duck_go are enabled")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutually exclusive, got: %v", err)
	}
}

func TestBuildAllThreeTools(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{
			WebFetch:  true,
			WebSearch: true,
			WebSearchConfig: config.WebSearchConfig{
				DuckDuckGo: config.WebSearchEngineDuckDuckGo{Enabled: true},
			},
			Bash: true,
		},
		Sandbox: config.SandboxConfig{TimeoutSeconds: 120},
	}
	opts := &tools.BuildToolsOptions{Logger: nil}
	builtTools, err := tools.BuildTools(ctx, cfg, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := make(map[string]bool)
	for _, bt := range builtTools {
		info, _ := bt.Info(ctx)
		names[info.Name] = true
	}
	for _, expected := range []string{"web_fetch", "web_search", "bash", "get_datetime"} {
		if !names[expected] {
			t.Errorf("missing %s tool", expected)
		}
	}
}

// --- HTML parsing helpers tests ---

func TestCleanHTMLText(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"<b>Hello</b> World", "Hello World"},
		{"no tags", "no tags"},
		{"<a href='x'>Link &amp; Text</a>", "Link & Text"},
		{"  multiple   spaces  ", "multiple spaces"},
		{"", ""},
		{"&lt;script&gt;alert(1)&lt;/script&gt;", "<script>alert(1)</script>"},
	}
	for _, tc := range cases {
		got := tools.CleanHTMLText(tc.input)
		if got != tc.want {
			t.Errorf("CleanHTMLText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractHref(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`<a href="https://example.com">Link</a>`, "https://example.com"},
		{`<a class="foo" href="bar">text</a>`, "bar"},
		{`no href here`, ""},
		{`href="unclosed`, ""},
	}
	for _, tc := range cases {
		got := tools.ExtractHref(tc.input)
		if got != tc.want {
			t.Errorf("ExtractHref(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractCurrentTagContent(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			"simple a tag",
			`class="result__a" href="https://example.com">Hello World</a>`,
			"Hello World",
		},
		{
			"a tag with bold inside",
			`class="result__a" href="https://example.com"><b>ESP32</b> Providers</a>`,
			"ESP32 Providers",
		},
		{
			"snippet tag",
			`class="result__snippet" href="url"><b>ESP32</b> Series are available.</a>`,
			"ESP32 Series are available.",
		},
		{
			"no closing angle bracket",
			`class="result__a" href="url"`,
			"",
		},
		{
			"no closing tag",
			`class="result__a" href="url">text without close`,
			"",
		},
	}
	for _, tc := range cases {
		got := tools.CleanHTMLText(tools.ExtractCurrentTagContent(tc.input))
		if got != tc.want {
			t.Errorf("ExtractCurrentTagContent(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWebSearch_DDG_WithRedirectURLs(t *testing.T) {
	html := `<!DOCTYPE html><html><body><div class="results">
  <div class="result results_links results_links_deep web-result">
    <div class="result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc">Example Page</a>
      </h2>
      <a class="result__snippet" href="#">A page about examples.</a>
    </div>
  </div>
</div></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer srv.Close()

	executor := tools.NewWebSearchExecutorWithEndpoint(ddgConfig(), "duckduckgo", srv.URL)
	result, err := executor.ExecWebSearch(context.Background(), tools.WebSearchParams{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].URL != "https://example.com/page" {
		t.Errorf("expected decoded URL 'https://example.com/page', got %q", result.Results[0].URL)
	}
	if result.Results[0].Title != "Example Page" {
		t.Errorf("expected title 'Example Page', got %q", result.Results[0].Title)
	}
}

func TestDecodeRedirectURL(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=abc", "https://example.com"},
		{"https://direct-link.com", "https://direct-link.com"},
		{"//duckduckgo.com/l/?uddg=&rut=abc", "//duckduckgo.com/l/?uddg=&rut=abc"},
	}
	for _, tc := range cases {
		got := tools.DecodeRedirectURL(tc.input)
		if got != tc.want {
			t.Errorf("DecodeRedirectURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- Fake data ---

// fakeDDGHTML mirrors the real DuckDuckGo HTML structure as observed in production.
// Key differences from the original (broken) fixture:
// - <a> tags have rel="nofollow" before class
// - href values are direct URLs (not always uddg= redirects)
// - results are inside <h2> tags, followed by extras, then snippet
var fakeDDGHTML = `<!DOCTYPE html>
<html>
<body>
<div id="links" class="results">
  <div class="result results_links results_links_deep web-result">
    <div class="result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="https://www.mouser.ca/c/?series=ESP32">ESP32 Series - Mouser Canada</a>
      </h2>
      <div class="result__extras">
        <div class="result__extras__url">
          <span class="result__icon">
            <a rel="nofollow" href="https://www.mouser.ca/c/?series=ESP32">
              <img class="result__icon__img" width="16" height="16" alt="" src="//external-content.duckduckgo.com/ip3/www.mouser.ca.ico" />
            </a>
          </span>
          <a class="result__url" href="https://www.mouser.ca/c/?series=ESP32">www.mouser.ca/c/?series=ESP32</a>
        </div>
      </div>
      <a class="result__snippet" href="https://www.mouser.ca/c/?series=ESP32"><b>ESP32</b> Series are available at Mouser Electronics. Mouser offers inventory, pricing, &amp; datasheets for <b>ESP32</b> Series.</a>
      <div class="clear"></div>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result">
    <div class="result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="https://www.digikey.ca/en/supplier-centers/espressif-systems">Espressif Systems Distributor | DigiKey</a>
      </h2>
      <div class="result__extras">
        <div class="result__extras__url">
          <span class="result__icon">
            <a rel="nofollow" href="https://www.digikey.ca/en/supplier-centers/espressif-systems">
              <img class="result__icon__img" width="16" height="16" alt="" src="//external-content.duckduckgo.com/ip3/www.digikey.ca.ico" />
            </a>
          </span>
          <a class="result__url" href="https://www.digikey.ca/en/supplier-centers/espressif-systems">www.digikey.ca</a>
        </div>
      </div>
      <a class="result__snippet" href="https://www.digikey.ca/en/supplier-centers/espressif-systems">Espressif Systems is a world-leading IoT company providing <b>ESP32</b> and other chips.</a>
      <div class="clear"></div>
    </div>
  </div>
</div>
</body>
</html>`

var fakeDDGHTMLManyResults string

func init() {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body><div id="links" class="results">`)
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&b, `
  <div class="result results_links results_links_deep web-result">
    <div class="result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="https://example.com/result-%d">Result %d Title</a>
      </h2>
      <div class="result__extras">
        <div class="result__extras__url">
          <span class="result__icon">
            <a rel="nofollow" href="https://example.com/result-%d">
              <img class="result__icon__img" width="16" height="16" alt="" />
            </a>
          </span>
        </div>
      </div>
      <a class="result__snippet" href="https://example.com/result-%d">Snippet for result %d.</a>
      <div class="clear"></div>
    </div>
  </div>`, i, i, i, i, i)
	}
	b.WriteString(`</div></body></html>`)
	fakeDDGHTMLManyResults = b.String()
}

type fakeGoogleItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

type fakeGoogleResp struct {
	Items []fakeGoogleItem `json:"items"`
}

var fakeGoogleResponse = fakeGoogleResp{
	Items: []fakeGoogleItem{
		{Title: "Go Channels Tutorial", Link: "https://example.com/go-channels", Snippet: "Learn about Go channels and concurrency."},
		{Title: "Advanced Go Patterns", Link: "https://example.com/advanced-go", Snippet: "Advanced channel patterns in Go."},
	},
}
