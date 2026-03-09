package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	readability "github.com/go-shiori/go-readability"
	"github.com/ledongthuc/pdf"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	goexif "github.com/rwcarlsen/goexif/exif"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/s3fetch"
)

const (
	docFetchTimeoutSec = 60
	maxDocBytes        = 200 * 1024 // 200 KB text content limit
	maxDownloadBytes   = 50 * 1024 * 1024 // 50 MB raw download cap
	docFetchUserAgent  = "Open-Nipper-Agent/1.0 (+https://github.com/open-nipper/open-nipper)"
)

// DocFetchParams defines the input for the doc_fetch tool.
type DocFetchParams struct {
	URL string `json:"url" jsonschema:"description=HTTP/HTTPS URL or s3:// URI to fetch a document from,required"`
}

// DocFetchResult is the output of the doc_fetch tool.
type DocFetchResult struct {
	Content   string `json:"content"`
	MimeType  string `json:"mime_type"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url"`
	BytesRead int    `json:"bytes_read"`
}

// DocFetchExecutor holds the S3 config needed for s3:// URI support.
type DocFetchExecutor struct {
	s3Cfg          config.S3DefaultConfig
	allowLoopback  bool // only for unit tests with httptest servers
}

// NewDocFetchExecutor creates a DocFetchExecutor with the given S3 config.
func NewDocFetchExecutor(s3Cfg config.S3DefaultConfig) *DocFetchExecutor {
	return &DocFetchExecutor{s3Cfg: s3fetch.NormalizeS3Config(s3Cfg)}
}

// NewDocFetchExecutorForTest creates a DocFetchExecutor that allows loopback
// connections. This is strictly for unit tests using httptest.Server.
func NewDocFetchExecutorForTest(s3Cfg config.S3DefaultConfig) *DocFetchExecutor {
	return &DocFetchExecutor{s3Cfg: s3fetch.NormalizeS3Config(s3Cfg), allowLoopback: true}
}

// ExecDocFetch is the tool executor function registered with Eino.
func (e *DocFetchExecutor) ExecDocFetch(ctx context.Context, params DocFetchParams) (*DocFetchResult, error) {
	if params.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	if strings.HasPrefix(params.URL, "s3://") {
		return e.fetchS3(ctx, params.URL)
	}

	// If the URL hostname matches the configured S3 endpoint, route through
	// the authenticated S3 client instead of raw HTTP. Minio served over
	// HTTPS returns 403 for unauthenticated requests.
	if e.isS3EndpointURL(params.URL) {
		return e.fetchS3ByHTTPURL(ctx, params.URL)
	}

	return e.fetchHTTP(ctx, params.URL)
}

// isS3EndpointURL returns true if the URL's host matches the configured S3 endpoint.
func (e *DocFetchExecutor) isS3EndpointURL(rawURL string) bool {
	f := s3fetch.NewFetcher(e.s3Cfg)
	return f.IsS3EndpointURL(rawURL)
}

// fetchS3ByHTTPURL converts an HTTPS Minio URL into an authenticated S3 GetObject call.
// URL format: https://{endpoint}/{bucket}/{key...}
func (e *DocFetchExecutor) fetchS3ByHTTPURL(ctx context.Context, rawURL string) (*DocFetchResult, error) {
	bucket, key, err := s3fetch.ParseHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}

	if err := s3fetch.ValidateS3Path(bucket, key); err != nil {
		return nil, err
	}

	if e.s3Cfg.AccessKey == "" || e.s3Cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 credentials not configured; set agent.s3.access_key and agent.s3.secret_key")
	}

	minioClient, err := minio.New(e.s3Cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(e.s3Cfg.AccessKey, e.s3Cfg.SecretKey, ""),
		Secure: e.s3Cfg.UseSSL,
		Region: e.s3Cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("creating S3 client: %w", err)
	}

	return e.getS3Object(ctx, minioClient, bucket, key, rawURL)
}

// fetchHTTP downloads a document over HTTP/HTTPS and extracts text content.
func (e *DocFetchExecutor) fetchHTTP(ctx context.Context, rawURL string) (*DocFetchResult, error) {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", rawURL, err)
	}

	if !e.allowLoopback {
		if err := validateURLSafety(parsed); err != nil {
			return nil, err
		}
	}

	client := &http.Client{
		Timeout: docFetchTimeoutSec * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects (max %d)", maxRedirects)
			}
			if !e.allowLoopback {
				for _, prev := range via {
					if err := validateURLSafety(prev.URL); err != nil {
						return fmt.Errorf("redirect to unsafe URL: %w", err)
					}
				}
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", docFetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/pdf;q=0.8,text/plain;q=0.7,*/*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &DocFetchResult{
			URL:      resp.Request.URL.String(),
			MimeType: resp.Header.Get("Content-Type"),
			Content:  fmt.Sprintf("HTTP error: %d %s", resp.StatusCode, resp.Status),
		}, nil
	}

	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	finalURL := resp.Request.URL.String()

	return processContent(bodyBytes, contentType, finalURL)
}

// fetchS3 downloads a document from S3/Minio using the configured credentials.
func (e *DocFetchExecutor) fetchS3(ctx context.Context, s3URI string) (*DocFetchResult, error) {
	bucket, key, err := s3fetch.ParseS3URI(s3URI)
	if err != nil {
		return nil, err
	}

	if err := s3fetch.ValidateS3Path(bucket, key); err != nil {
		return nil, err
	}

	if e.s3Cfg.Endpoint == "" {
		return nil, fmt.Errorf("S3 endpoint not configured; set agent.s3.endpoint in agent config")
	}
	if e.s3Cfg.AccessKey == "" || e.s3Cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 credentials not configured; set agent.s3.access_key and agent.s3.secret_key")
	}

	effectiveBucket := bucket
	if effectiveBucket == "" {
		effectiveBucket = e.s3Cfg.Bucket
	}
	if effectiveBucket == "" {
		return nil, fmt.Errorf("no bucket specified in URL or config")
	}

	minioClient, err := minio.New(e.s3Cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(e.s3Cfg.AccessKey, e.s3Cfg.SecretKey, ""),
		Secure: e.s3Cfg.UseSSL,
		Region: e.s3Cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("creating S3 client: %w", err)
	}

	return e.getS3Object(ctx, minioClient, effectiveBucket, key, s3URI)
}

// s3ObjectGetter abstracts S3 GetObject for testing.
type s3ObjectGetter interface {
	GetObject(ctx context.Context, bucket, key string, opts minio.GetObjectOptions) (*minio.Object, error)
}

func (e *DocFetchExecutor) getS3Object(ctx context.Context, client s3ObjectGetter, bucket, key, originalURI string) (*DocFetchResult, error) {
	obj, err := client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject(%s/%s): %w", bucket, key, err)
	}
	defer obj.Close()

	info, err := obj.Stat()
	if err != nil {
		return nil, fmt.Errorf("S3 Stat(%s/%s): %w", bucket, key, err)
	}

	if info.Size > maxDownloadBytes {
		return &DocFetchResult{
			URL:       originalURI,
			MimeType:  info.ContentType,
			BytesRead: 0,
			Content:   fmt.Sprintf("[File too large: %d bytes (max %d). MIME: %s, Key: %s]", info.Size, maxDownloadBytes, info.ContentType, key),
		}, nil
	}

	limited := io.LimitReader(obj, maxDownloadBytes+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading S3 object: %w", err)
	}

	contentType := info.ContentType
	if contentType == "" {
		contentType = detectMIME(bodyBytes, key)
	}

	return processContent(bodyBytes, contentType, originalURI)
}

// processContent routes raw bytes through the appropriate parser based on MIME type.
func processContent(data []byte, contentType, sourceURL string) (*DocFetchResult, error) {
	mime := normalizeMIME(contentType)

	result := &DocFetchResult{
		URL:       sourceURL,
		MimeType:  mime,
		BytesRead: len(data),
	}

	switch {
	case isPDFContent(mime, data):
		text, title, err := extractPDFText(data)
		if err != nil {
			result.Content = fmt.Sprintf("[PDF parsing failed: %v. File size: %d bytes, MIME: %s]", err, len(data), mime)
		} else {
			result.Content = truncateUTF8(text, maxDocBytes)
			result.Title = title
		}

	case isHTMLContent(mime, data):
		article, err := readability.FromReader(bytes.NewReader(data), &url.URL{})
		if err == nil {
			result.Title = article.Title
			result.Content = truncateUTF8(article.TextContent, maxDocBytes)
		} else {
			result.Content = truncateUTF8(stripHTMLTags(string(data)), maxDocBytes)
		}

	case isTextContent(mime):
		result.Content = truncateUTF8(string(data), maxDocBytes)

	case isImageContent(mime):
		exifData := extractEXIF(data)
		if exifData != "" {
			result.Content = fmt.Sprintf(
				"Image file (%s, %d bytes)\n\nEXIF Metadata:\n%s\n\n"+
					"IMPORTANT: This tool extracted EXIF metadata only. "+
					"If the image was provided as a multimodal attachment in the conversation, "+
					"you MUST use your vision capabilities to describe the visual content. "+
					"Combine EXIF metadata (location, camera, date) with your visual analysis of the image pixels.",
				mime, len(data), exifData,
			)
		} else {
			result.Content = fmt.Sprintf(
				"Image file (%s, %d bytes). No EXIF metadata found in this file.\n\n"+
					"IMPORTANT: If the image was provided as a multimodal attachment in the conversation, "+
					"you MUST still use your vision capabilities to describe the visual content. "+
					"Ask the user for the location since no GPS data was found.",
				mime, len(data),
			)
		}

	case isAudioVideoContent(mime):
		result.Content = fmt.Sprintf("[Media file. MIME: %s, Size: %d bytes. Audio/video content cannot be transcribed by this tool.]", mime, len(data))

	default:
		result.Content = fmt.Sprintf("[Unsupported content type: %s, Size: %d bytes]", mime, len(data))
	}

	return result, nil
}

// extractPDFText extracts text content from PDF bytes.
func extractPDFText(data []byte) (string, string, error) {
	reader := bytes.NewReader(data)
	pdfReader, err := pdf.NewReader(reader, int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("opening PDF: %w", err)
	}

	numPages := pdfReader.NumPage()
	if numPages == 0 {
		return "", "", fmt.Errorf("PDF has no pages")
	}

	var textBuf strings.Builder
	for i := 1; i <= numPages; i++ {
		page := pdfReader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		textBuf.WriteString(text)
		textBuf.WriteString("\n")

		if textBuf.Len() > maxDocBytes {
			break
		}
	}

	return textBuf.String(), "", nil
}

// extractEXIF reads EXIF metadata from image bytes and returns a human-readable summary.
func extractEXIF(data []byte) string {
	x, err := goexif.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}

	var lines []string

	addField := func(label string, tag goexif.FieldName) {
		val, err := x.Get(tag)
		if err != nil {
			return
		}
		s := strings.Trim(val.String(), "\"")
		if s != "" {
			lines = append(lines, fmt.Sprintf("  %s: %s", label, s))
		}
	}

	addField("Camera Make", goexif.Make)
	addField("Camera Model", goexif.Model)
	addField("Date/Time Original", goexif.DateTimeOriginal)
	addField("Date/Time Digitized", goexif.DateTimeDigitized)
	addField("Exposure Time", goexif.ExposureTime)
	addField("F-Number", goexif.FNumber)
	addField("ISO Speed", goexif.ISOSpeedRatings)
	addField("Focal Length", goexif.FocalLength)
	addField("Focal Length (35mm)", goexif.FocalLengthIn35mmFilm)
	addField("Lens Make", goexif.LensMake)
	addField("Lens Model", goexif.LensModel)
	addField("Flash", goexif.Flash)
	addField("Orientation", goexif.Orientation)
	addField("Image Width", goexif.PixelXDimension)
	addField("Image Height", goexif.PixelYDimension)
	addField("Software", goexif.Software)
	addField("Copyright", goexif.Copyright)
	addField("Artist", goexif.Artist)

	lat, lon, err := x.LatLong()
	if err == nil && !math.IsNaN(lat) && !math.IsNaN(lon) {
		latDir := "N"
		if lat < 0 {
			latDir = "S"
		}
		lonDir := "E"
		if lon < 0 {
			lonDir = "W"
		}
		lines = append(lines, fmt.Sprintf("  GPS Latitude: %.6f° %s", math.Abs(lat), latDir))
		lines = append(lines, fmt.Sprintf("  GPS Longitude: %.6f° %s", math.Abs(lon), lonDir))
		lines = append(lines, fmt.Sprintf("  Google Maps: https://www.google.com/maps?q=%.6f,%.6f", lat, lon))
	}

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// --- URL safety validation ---

var privateNetworks = []struct {
	network *net.IPNet
	name    string
}{
	{mustParseCIDR("10.0.0.0/8"), "RFC1918 Class A"},
	{mustParseCIDR("172.16.0.0/12"), "RFC1918 Class B"},
	{mustParseCIDR("192.168.0.0/16"), "RFC1918 Class C"},
	{mustParseCIDR("127.0.0.0/8"), "loopback"},
	{mustParseCIDR("169.254.0.0/16"), "link-local"},
	{mustParseCIDR("::1/128"), "IPv6 loopback"},
	{mustParseCIDR("fc00::/7"), "IPv6 ULA"},
	{mustParseCIDR("fe80::/10"), "IPv6 link-local"},
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// validateURLSafety rejects URLs pointing to private/internal networks or unsafe schemes.
func validateURLSafety(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported scheme %q; only http and https are allowed", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty hostname")
	}

	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("access to localhost is forbidden")
	}

	if strings.HasSuffix(strings.ToLower(host), ".internal") ||
		strings.HasSuffix(strings.ToLower(host), ".local") {
		return fmt.Errorf("access to internal/local domains is forbidden")
	}

	ip := net.ParseIP(host)
	if ip != nil {
		for _, pn := range privateNetworks {
			if pn.network.Contains(ip) {
				return fmt.Errorf("access to %s network (%s) is forbidden", pn.name, host)
			}
		}
	}

	return nil
}


// --- MIME type helpers ---

func normalizeMIME(ct string) string {
	ct = strings.ToLower(ct)
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	return strings.TrimSpace(ct)
}

func isPDFContent(mime string, data []byte) bool {
	if mime == "application/pdf" {
		return true
	}
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

func isHTMLContent(mime string, data []byte) bool {
	if strings.Contains(mime, "html") || strings.Contains(mime, "xhtml") {
		return true
	}
	if len(data) > 0 {
		prefix := strings.ToLower(strings.TrimSpace(string(data[:min(512, len(data))])))
		return strings.HasPrefix(prefix, "<!doctype") || strings.Contains(prefix, "<html")
	}
	return false
}

func isTextContent(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	textMIMEs := []string{
		"application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/yaml",
		"application/x-sh", "application/x-shellscript",
	}
	for _, m := range textMIMEs {
		if mime == m {
			return true
		}
	}
	return false
}

func isImageContent(mime string) bool {
	return strings.HasPrefix(mime, "image/")
}

func isAudioVideoContent(mime string) bool {
	return strings.HasPrefix(mime, "audio/") ||
		strings.HasPrefix(mime, "video/")
}

func detectMIME(data []byte, filename string) string {
	ct := http.DetectContentType(data)
	if ct != "application/octet-stream" {
		return ct
	}

	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		return "application/x-yaml"
	case strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm"):
		return "text/html"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".csv"):
		return "text/csv"
	case strings.HasSuffix(lower, ".xml"):
		return "application/xml"
	}
	return ct
}

// truncateUTF8 truncates a string to at most maxLen bytes, ensuring valid UTF-8.
func truncateUTF8(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	b := []byte(s[:maxLen])
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
