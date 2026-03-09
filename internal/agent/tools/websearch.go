package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jescarri/open-nipper/internal/config"
)

const (
	webSearchTimeoutSec      = 15
	webSearchMaxResults      = 10
	webSearchDefaultResults  = 5
	webSearchMaxQueryLen     = 500
	webSearchUserAgent       = "Open-Nipper-Agent/1.0 (+https://github.com/jescarri/open-nipper)"
	ddgHTMLEndpoint          = "https://html.duckduckgo.com/html/"
	googleCSEEndpoint        = "https://www.googleapis.com/customsearch/v1"
	ddgMaxResponseBytes      = 512 * 1024 // 512 KB
)

// WebSearchParams defines the input for the web_search tool.
// Engine is ignored at runtime; the engine is set by config (mutually exclusive google vs duck_duck_go).
type WebSearchParams struct {
	Query      string `json:"query"       jsonschema:"description=Search query,required"`
	Engine     string `json:"engine"      jsonschema:"description=Ignored; search engine is configured server-side (duckduckgo or google)"`
	MaxResults int    `json:"max_results" jsonschema:"description=Maximum number of results to return (default 5, max 10)"`
}

// SearchResult represents a single search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// WebSearchResult is the output of the web_search tool.
type WebSearchResult struct {
	Engine  string         `json:"engine"`
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// WebSearchExecutor holds configuration for executing web searches.
// effectiveEngine is the only engine used ("google" or "duckduckgo"), set at build time from config.
type WebSearchExecutor struct {
	cfg             config.WebSearchConfig
	effectiveEngine string // "google" or "duckduckgo"
	client          *http.Client
	ddgEndpoint     string
	googleEndpoint  string
}

// NewWebSearchExecutor creates a new executor with the given config and engine. effectiveEngine must be "google" or "duckduckgo".
func NewWebSearchExecutor(cfg config.WebSearchConfig, effectiveEngine string) *WebSearchExecutor {
	e := &WebSearchExecutor{
		cfg:             cfg,
		effectiveEngine: effectiveEngine,
		ddgEndpoint:     ddgHTMLEndpoint,
		googleEndpoint:  googleCSEEndpoint,
		client: &http.Client{
			Timeout: webSearchTimeoutSec * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
	return e
}

// NewWebSearchExecutorWithEndpoint creates an executor with a custom DuckDuckGo endpoint (for testing).
func NewWebSearchExecutorWithEndpoint(cfg config.WebSearchConfig, effectiveEngine, ddgURL string) *WebSearchExecutor {
	e := NewWebSearchExecutor(cfg, effectiveEngine)
	if ddgURL != "" {
		e.ddgEndpoint = ddgURL
	}
	return e
}

// NewWebSearchExecutorWithGoogleEndpoint creates an executor with custom endpoints (for testing).
func NewWebSearchExecutorWithGoogleEndpoint(cfg config.WebSearchConfig, effectiveEngine, ddgURL, googleURL string) *WebSearchExecutor {
	e := NewWebSearchExecutor(cfg, effectiveEngine)
	if ddgURL != "" {
		e.ddgEndpoint = ddgURL
	}
	if googleURL != "" {
		e.googleEndpoint = googleURL
	}
	return e
}

// ExecWebSearch validates and executes a web search. Exported for testing.
func (e *WebSearchExecutor) ExecWebSearch(ctx context.Context, params WebSearchParams) (*WebSearchResult, error) {
	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if len(params.Query) > webSearchMaxQueryLen {
		return nil, fmt.Errorf("query too long (max %d characters)", webSearchMaxQueryLen)
	}

	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = webSearchDefaultResults
	}
	if maxResults > webSearchMaxResults {
		maxResults = webSearchMaxResults
	}

	// Use only the engine configured at build time (mutually exclusive in config).
	engine := e.effectiveEngine
	if engine == "" {
		engine = "duckduckgo"
	}

	switch engine {
	case "duckduckgo", "ddg":
		results, err := e.searchDuckDuckGo(ctx, params.Query, maxResults)
		if err != nil {
			return nil, fmt.Errorf("duckduckgo search: %w", err)
		}
		return &WebSearchResult{
			Engine:  "duckduckgo",
			Query:   params.Query,
			Results: results,
		}, nil

	case "google":
		if e.cfg.Google.GoogleAPIKey == "" || e.cfg.Google.GoogleCX == "" {
			return nil, fmt.Errorf("google search: google_api_key and google_cx must be set when google engine is enabled")
		}
		results, err := e.searchGoogle(ctx, params.Query, maxResults)
		if err != nil {
			return nil, fmt.Errorf("google search: %w", err)
		}
		return &WebSearchResult{
			Engine:  "google",
			Query:   params.Query,
			Results: results,
		}, nil

	default:
		return nil, fmt.Errorf("web search engine %q is not configured; use google or duckduckgo", engine)
	}
}

// searchDuckDuckGo performs a search via DuckDuckGo's HTML endpoint and parses the results.
func (e *WebSearchExecutor) searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	form := url.Values{}
	form.Set("q", query)
	form.Set("b", "")
	form.Set("kl", "")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.ddgEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching search results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, ddgMaxResponseBytes)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	body := string(bodyBytes)
	return parseDDGResults(body, maxResults), nil
}

// parseDDGResults extracts search results from DuckDuckGo HTML response.
//
// Real DDG HTML structure:
//
//	<a rel="nofollow" class="result__a" href="URL">Title Text</a>
//	...
//	<a class="result__snippet" href="URL">Snippet text with <b>highlights</b>.</a>
//
// The parser finds `class="result__a"`, then extracts content between the
// first `>` (end of opening tag) and `</a>` — this is the title. Same
// approach for snippets. Using ExtractTagContent would look for the NEXT
// <a> tag instead of the current one, which is why titles were always empty.
func parseDDGResults(html string, maxResults int) []SearchResult {
	var results []SearchResult

	remaining := html
	for len(results) < maxResults {
		idx := strings.Index(remaining, `class="result__a"`)
		if idx == -1 {
			break
		}
		remaining = remaining[idx:]

		resultURL := ExtractHref(remaining)
		title := ExtractCurrentTagContent(remaining)

		snippetIdx := strings.Index(remaining, `class="result__snippet"`)
		snippet := ""
		if snippetIdx != -1 {
			snippet = ExtractCurrentTagContent(remaining[snippetIdx:])
		}

		if resultURL != "" {
			decodedURL := DecodeRedirectURL(resultURL)
			results = append(results, SearchResult{
				Title:   CleanHTMLText(title),
				URL:     decodedURL,
				Snippet: CleanHTMLText(snippet),
			})
		}

		// Advance past this result to find the next one.
		nextIdx := strings.Index(remaining[1:], `class="result__a"`)
		if nextIdx == -1 {
			break
		}
		remaining = remaining[nextIdx+1:]
	}

	return results
}

// ExtractCurrentTagContent extracts the inner HTML of the tag we're
// already inside (i.e. s starts after the opening `<tag ` at the class
// attribute). It finds the first `>` (end of the opening tag) and then
// the nearest `</a>` to get the content between them. Falls back to a
// generic `</` if `</a>` isn't found (handles <span>, <div>, etc.).
func ExtractCurrentTagContent(s string) string {
	gt := strings.Index(s, ">")
	if gt == -1 {
		return ""
	}
	rest := s[gt+1:]

	// Prefer </a> since DDG uses <a> for both result__a and result__snippet.
	if end := strings.Index(rest, "</a>"); end != -1 {
		return rest[:end]
	}
	// Fallback: any closing tag.
	if end := strings.Index(rest, "</"); end != -1 {
		return rest[:end]
	}
	return ""
}

// searchGoogle performs a search via Google Custom Search JSON API.
func (e *WebSearchExecutor) searchGoogle(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("key", e.cfg.Google.GoogleAPIKey)
	params.Set("cx", e.cfg.Google.GoogleCX)
	params.Set("q", query)
	params.Set("num", fmt.Sprintf("%d", maxResults))

	reqURL := e.googleEndpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching search results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("google API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var gResp googleSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&gResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	results := make([]SearchResult, 0, len(gResp.Items))
	for _, item := range gResp.Items {
		results = append(results, SearchResult{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
		})
	}

	return results, nil
}

type googleSearchResponse struct {
	Items []googleSearchItem `json:"items"`
}

type googleSearchItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

// --- HTML parsing helpers (exported for testing) ---

// ExtractHref extracts the href value from the first tag in the string.
func ExtractHref(s string) string {
	hrefIdx := strings.Index(s, `href="`)
	if hrefIdx == -1 {
		return ""
	}
	start := hrefIdx + len(`href="`)
	end := strings.Index(s[start:], `"`)
	if end == -1 {
		return ""
	}
	return s[start : start+end]
}

// ExtractTagContent extracts text content between the first <tag> and </tag>.
func ExtractTagContent(s string, tag string) string {
	openTag := "<" + tag
	closeTag := "</" + tag + ">"

	openIdx := strings.Index(s, openTag)
	if openIdx == -1 {
		return ""
	}

	// Find the end of the opening tag.
	tagEnd := strings.Index(s[openIdx:], ">")
	if tagEnd == -1 {
		return ""
	}
	contentStart := openIdx + tagEnd + 1

	closeIdx := strings.Index(s[contentStart:], closeTag)
	if closeIdx == -1 {
		return ""
	}

	return s[contentStart : contentStart+closeIdx]
}

// DecodeRedirectURL extracts the actual URL from DuckDuckGo's redirect wrapper.
func DecodeRedirectURL(rawURL string) string {
	// DDG wraps URLs like //duckduckgo.com/l/?uddg=<encoded_url>&...
	if strings.Contains(rawURL, "uddg=") {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return rawURL
		}
		uddg := parsed.Query().Get("uddg")
		if uddg != "" {
			return uddg
		}
	}
	return rawURL
}

// CleanHTMLText strips HTML tags and normalises whitespace.
func CleanHTMLText(s string) string {
	if s == "" {
		return ""
	}

	// Strip HTML tags.
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	result := strings.Join(strings.Fields(b.String()), " ")

	// Decode common HTML entities.
	result = strings.ReplaceAll(result, "&amp;", "&")
	result = strings.ReplaceAll(result, "&lt;", "<")
	result = strings.ReplaceAll(result, "&gt;", ">")
	result = strings.ReplaceAll(result, "&quot;", `"`)
	result = strings.ReplaceAll(result, "&#39;", "'")
	result = strings.ReplaceAll(result, "&apos;", "'")

	if !utf8.ValidString(result) {
		result = strings.ToValidUTF8(result, "")
	}

	return result
}

