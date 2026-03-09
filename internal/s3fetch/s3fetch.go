// Package s3fetch provides a unified way to fetch bytes from S3/Minio URLs.
// It handles both s3:// URIs and HTTPS endpoints that match a configured S3
// endpoint, creating authenticated minio clients as needed. For URLs that do
// not match the S3 endpoint it falls back to a plain HTTP GET.
package s3fetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/open-nipper/open-nipper/internal/config"
)

// Fetcher downloads bytes from S3-compatible or plain HTTP URLs.
type Fetcher struct {
	s3Cfg      config.S3DefaultConfig
	httpClient *http.Client
	maxBytes   int64
}

// Option configures a Fetcher.
type Option func(*Fetcher)

// WithHTTPClient sets a custom HTTP client for plain-HTTP fallback.
func WithHTTPClient(c *http.Client) Option {
	return func(f *Fetcher) { f.httpClient = c }
}

// WithMaxBytes sets the maximum number of bytes to download.
func WithMaxBytes(n int64) Option {
	return func(f *Fetcher) { f.maxBytes = n }
}

const defaultMaxBytes = 50 * 1024 * 1024 // 50 MB

// NewFetcher creates a Fetcher with the given S3 config and options.
func NewFetcher(s3Cfg config.S3DefaultConfig, opts ...Option) *Fetcher {
	f := &Fetcher{
		s3Cfg:      NormalizeS3Config(s3Cfg),
		httpClient: &http.Client{},
		maxBytes:   defaultMaxBytes,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Fetch downloads bytes from rawURL. It routes through the S3 client when the
// URL is an s3:// URI or its host matches the configured S3 endpoint. Otherwise
// it falls back to a plain HTTP GET.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	if strings.HasPrefix(rawURL, "s3://") {
		return f.fetchS3URI(ctx, rawURL)
	}
	if f.IsS3EndpointURL(rawURL) {
		return f.fetchS3ByHTTPURL(ctx, rawURL)
	}
	return f.fetchHTTP(ctx, rawURL)
}

// IsS3EndpointURL returns true if the URL's host matches the configured S3 endpoint.
func (f *Fetcher) IsS3EndpointURL(rawURL string) bool {
	if f.s3Cfg.Endpoint == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return hostsMatch(parsed.Hostname(), f.s3Cfg.Endpoint)
}

// fetchS3URI handles s3://bucket/key URIs.
func (f *Fetcher) fetchS3URI(ctx context.Context, s3URI string) ([]byte, error) {
	bucket, key, err := ParseS3URI(s3URI)
	if err != nil {
		return nil, err
	}
	if bucket == "" {
		bucket = f.s3Cfg.Bucket
	}
	if err := ValidateS3Path(bucket, key); err != nil {
		return nil, err
	}
	return f.getObject(ctx, bucket, key)
}

// fetchS3ByHTTPURL converts https://{endpoint}/{bucket}/{key} into an S3 GetObject.
func (f *Fetcher) fetchS3ByHTTPURL(ctx context.Context, rawURL string) ([]byte, error) {
	bucket, key, err := ParseHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := ValidateS3Path(bucket, key); err != nil {
		return nil, err
	}
	return f.getObject(ctx, bucket, key)
}

// fetchHTTP does a plain HTTP GET.
func (f *Fetcher) fetchHTTP(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating fetch request: %w", err)
	}
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("URL returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return data, nil
}

// getObject downloads an object from S3/Minio using authenticated credentials.
func (f *Fetcher) getObject(ctx context.Context, bucket, key string) ([]byte, error) {
	if f.s3Cfg.Endpoint == "" {
		return nil, fmt.Errorf("S3 endpoint not configured")
	}
	if f.s3Cfg.AccessKey == "" || f.s3Cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 credentials not configured")
	}

	client, err := minio.New(f.s3Cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(f.s3Cfg.AccessKey, f.s3Cfg.SecretKey, ""),
		Secure: f.s3Cfg.UseSSL,
		Region: f.s3Cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("creating S3 client: %w", err)
	}

	obj, err := client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject(%s/%s): %w", bucket, key, err)
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		return nil, fmt.Errorf("S3 Stat(%s/%s): %w", bucket, key, err)
	}
	if info.Size > f.maxBytes {
		return nil, fmt.Errorf("S3 object too large: %d bytes (max %d)", info.Size, f.maxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(obj, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading S3 object: %w", err)
	}
	return data, nil
}

// --- Exported helpers ---

// NormalizeS3Config strips scheme prefixes from the endpoint and infers UseSSL.
func NormalizeS3Config(cfg config.S3DefaultConfig) config.S3DefaultConfig {
	ep := strings.TrimSpace(cfg.Endpoint)
	if strings.HasPrefix(ep, "https://") {
		cfg.Endpoint = strings.TrimPrefix(ep, "https://")
		cfg.UseSSL = true
	} else if strings.HasPrefix(ep, "http://") {
		cfg.Endpoint = strings.TrimPrefix(ep, "http://")
		cfg.UseSSL = false
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	return cfg
}

// ParseS3URI splits "s3://bucket/key/path" into bucket and key.
func ParseS3URI(uri string) (bucket, key string, err error) {
	if !strings.HasPrefix(uri, "s3://") {
		return "", "", fmt.Errorf("not an S3 URI: %q", uri)
	}
	rest := strings.TrimPrefix(uri, "s3://")
	if rest == "" {
		return "", "", fmt.Errorf("empty S3 URI")
	}
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return rest, "", fmt.Errorf("S3 URI has bucket but no key: %q", uri)
	}
	return rest[:idx], rest[idx+1:], nil
}

// ParseHTTPURL extracts bucket and key from https://{endpoint}/{bucket}/{key...}.
func ParseHTTPURL(rawURL string) (bucket, key string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	trimmed := strings.TrimPrefix(parsed.Path, "/")
	if trimmed == "" {
		return "", "", fmt.Errorf("S3 URL has no path: %q", rawURL)
	}
	idx := strings.Index(trimmed, "/")
	if idx < 0 {
		return "", "", fmt.Errorf("S3 URL has bucket but no key: %q", rawURL)
	}
	return trimmed[:idx], trimmed[idx+1:], nil
}

// ValidateS3Path checks bucket name and key for safety.
func ValidateS3Path(bucket, key string) error {
	if bucket != "" {
		if len(bucket) < 3 || len(bucket) > 63 {
			return fmt.Errorf("invalid S3 bucket name length: %d (must be 3-63)", len(bucket))
		}
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("path traversal in S3 key is forbidden")
	}
	if key == "" {
		return fmt.Errorf("S3 key is empty")
	}
	return nil
}

// HostsMatch returns true if urlHost matches the (possibly port-bearing) endpoint.
func hostsMatch(urlHost, endpoint string) bool {
	epHost := endpoint
	if h, _, err := net.SplitHostPort(epHost); err == nil {
		epHost = h
	}
	return strings.EqualFold(urlHost, epHost)
}
