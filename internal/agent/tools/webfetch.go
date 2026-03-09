// Package tools provides Eino-compatible tools for the Open-Nipper agent.
package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	readability "github.com/go-shiori/go-readability"
)

const (
	webFetchTimeoutSec = 30
	maxBodyBytes       = 100 * 1024 // 100 KB
	maxRedirects       = 5
	webFetchUserAgent  = "curl/8.14.0"
)

// WebFetchParams defines the input for the web_fetch tool.
type WebFetchParams struct {
	URL     string            `json:"url"     jsonschema:"description=URL to fetch,required"`
	Headers map[string]string `json:"headers" jsonschema:"description=Optional HTTP headers to include"`
}

// WebFetchResult is the output of the web_fetch tool.
type WebFetchResult struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

// ExecWebFetch performs an HTTP fetch. It is exported for testing.
// In production use BuildTools / the Eino tool interface instead.
func ExecWebFetch(ctx context.Context, params WebFetchParams) (*WebFetchResult, error) {
	return webFetchFn(ctx, params)
}

// webFetchFn is the underlying function registered as an Eino tool.
func webFetchFn(ctx context.Context, params WebFetchParams) (*WebFetchResult, error) {
	if params.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	if _, err := url.ParseRequestURI(params.URL); err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", params.URL, err)
	}

	client := &http.Client{
		Timeout: webFetchTimeoutSec * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects (max %d)", maxRedirects)
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", webFetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8")
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", params.URL, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	// Truncate if over limit.
	if len(bodyBytes) > maxBodyBytes {
		bodyBytes = bodyBytes[:maxBodyBytes]
		// Trim to valid UTF-8.
		for !utf8.Valid(bodyBytes) {
			bodyBytes = bodyBytes[:len(bodyBytes)-1]
		}
	}

	contentType := resp.Header.Get("Content-Type")
	result := &WebFetchResult{
		StatusCode: resp.StatusCode,
		URL:        resp.Request.URL.String(),
	}

	// Use readability for HTML, plain text otherwise.
	if isHTML(contentType, bodyBytes) {
		article, err := readability.FromReader(strings.NewReader(string(bodyBytes)), req.URL)
		if err == nil && strings.TrimSpace(article.TextContent) != "" {
			result.Title = article.Title
			result.Body = article.TextContent
		} else {
			// Readability failed or returned empty content (e.g. JS-rendered page).
			// Fall back to raw tag stripping.
			if err == nil && article.Title != "" {
				result.Title = article.Title
			}
			stripped := stripHTMLTags(string(bodyBytes))
			if strings.TrimSpace(stripped) != "" {
				result.Body = stripped
			} else {
				result.Body = "[page content could not be extracted — site may require JavaScript]"
			}
		}
	} else {
		result.Body = string(bodyBytes)
	}

	// Ensure body is valid UTF-8 and truncate if necessary after processing.
	if len(result.Body) > maxBodyBytes {
		result.Body = result.Body[:maxBodyBytes]
	}

	return result, nil
}

// isHTML returns true if the content type or content looks like HTML.
func isHTML(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	prefix := strings.ToLower(strings.TrimSpace(string(body[:min(512, len(body))])))
	return strings.HasPrefix(prefix, "<!doctype") || strings.Contains(prefix, "<html")
}

var reScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
var reStyle = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
var reNoscript = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript\s*>`)

// stripHTMLTags removes <script>, <style>, and <noscript> blocks entirely,
// then strips remaining HTML tags and collapses whitespace.
func stripHTMLTags(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	s = reStyle.ReplaceAllString(s, " ")
	s = reNoscript.ReplaceAllString(s, " ")

	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteRune(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	result := strings.Join(strings.Fields(b.String()), " ")
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
