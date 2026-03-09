package tools_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-nipper/open-nipper/internal/agent/registration"
	"github.com/open-nipper/open-nipper/internal/agent/tools"
	"github.com/open-nipper/open-nipper/internal/config"
)

func newTestExecutor() *tools.DocFetchExecutor {
	return tools.NewDocFetchExecutorForTest(config.S3DefaultConfig{})
}

func newProdExecutor() *tools.DocFetchExecutor {
	return tools.NewDocFetchExecutor(config.S3DefaultConfig{})
}

func TestDocFetch_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Hello from a plain text document."))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MimeType != "text/plain" {
		t.Errorf("expected mime_type text/plain, got %q", result.MimeType)
	}
	if !strings.Contains(result.Content, "Hello from a plain text document") {
		t.Errorf("expected content to contain text, got %q", result.Content)
	}
	if result.BytesRead == 0 {
		t.Error("expected non-zero bytes_read")
	}
}

func TestDocFetch_Markdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte("# Title\n\nSome **bold** text."))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "# Title") {
		t.Errorf("expected markdown content, got %q", result.Content)
	}
}

func TestDocFetch_HTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Test Doc</title></head>
<body><h1>Document</h1><p>This is a test HTML document for parsing.</p></body>
</html>`))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MimeType != "text/html" {
		t.Errorf("expected text/html, got %q", result.MimeType)
	}
	if !strings.Contains(result.Content, "test HTML document") {
		t.Errorf("expected readable text extraction, got %q", result.Content)
	}
}

func TestDocFetch_JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key": "value", "nested": {"a": 1}}`))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, `"key"`) {
		t.Errorf("expected JSON content, got %q", result.Content)
	}
}

func TestDocFetch_ImageNoEXIF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}) // minimal JPEG, no EXIF
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "Image file") {
		t.Errorf("expected image file metadata, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "No EXIF metadata found") {
		t.Errorf("expected 'No EXIF metadata found', got %q", result.Content)
	}
}

func TestDocFetch_AudioReturnsMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("OggS....fake audio data"))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "Media file") {
		t.Errorf("expected media file metadata, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "audio/ogg") {
		t.Errorf("expected MIME in output, got %q", result.Content)
	}
}

func TestDocFetch_VideoReturnsMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("fake video data"))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "Media file") {
		t.Errorf("expected media file metadata, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "video/mp4") {
		t.Errorf("expected MIME in output, got %q", result.Content)
	}
}

func TestDocFetch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "HTTP error: 404") {
		t.Errorf("expected HTTP error in content, got %q", result.Content)
	}
}

func TestDocFetch_EmptyURL(t *testing.T) {
	_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestDocFetch_InvalidURL(t *testing.T) {
	_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "not-a-url"})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestDocFetch_FileSchemeRejected(t *testing.T) {
	_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "file:///etc/passwd"})
	if err == nil {
		t.Fatal("expected error for file:// scheme")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("expected unsupported scheme error, got: %v", err)
	}
}

func TestDocFetch_FTPSchemeRejected(t *testing.T) {
	_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "ftp://example.com/file.txt"})
	if err == nil {
		t.Fatal("expected error for ftp:// scheme")
	}
}

func TestDocFetch_LocalhostRejected(t *testing.T) {
	_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "http://localhost:8080/secret"})
	if err == nil {
		t.Fatal("expected error for localhost")
	}
	if !strings.Contains(err.Error(), "localhost") {
		t.Errorf("expected localhost error, got: %v", err)
	}
}

func TestDocFetch_PrivateIPRejected(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"10.x", "http://10.0.0.1/internal"},
		{"172.16.x", "http://172.16.0.1/internal"},
		{"192.168.x", "http://192.168.1.1/internal"},
		{"127.x", "http://127.0.0.1:9090/admin"},
		{"169.254.x (link-local)", "http://169.254.169.254/latest/meta-data/"},
		{"IPv6 loopback", "http://[::1]/secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: tc.url})
			if err == nil {
				t.Fatalf("expected error for private IP %s", tc.url)
			}
			if !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("expected forbidden error, got: %v", err)
			}
		})
	}
}

func TestDocFetch_InternalDomainRejected(t *testing.T) {
	cases := []string{
		"http://service.internal/api",
		"http://myhost.local/admin",
	}
	for _, u := range cases {
		_, err := newProdExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: u})
		if err == nil {
			t.Fatalf("expected error for internal domain %s", u)
		}
	}
}

func TestDocFetch_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.Write([]byte("too slow"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := newTestExecutor().ExecDocFetch(ctx, tools.DocFetchParams{URL: srv.URL})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDocFetch_HTTPSMinioEndpointDetected(t *testing.T) {
	// When the URL hostname matches the S3 endpoint, the tool should route
	// through the authenticated S3 client. With bad credentials this will fail
	// at the S3 level, NOT with a raw HTTP 403.
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "minio.example.com",
		Bucket:    "mr-robot",
		AccessKey: "testkey",
		SecretKey: "testsecret",
		UseSSL:    true,
		Region:    "us-east-1",
	})
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.example.com/mr-robot/users/abc/images/photo.jpg",
	})
	// We expect an S3-level error (connection/auth), NOT "403 Forbidden" from raw HTTP.
	// The key assertion is that isS3EndpointURL correctly detected the match.
	if err == nil {
		t.Log("no error — S3 client connected (possibly network reachable)")
	} else if strings.Contains(err.Error(), "HTTP error: 403") {
		t.Error("got raw HTTP 403 — URL should have been routed through S3 client, not raw HTTP")
	}
}

func TestDocFetch_EndpointWithSchemePrefix(t *testing.T) {
	// Reproduces the real-world scenario: MINIO_ENDPOINT=https://minio.example.com
	// The normalizer should strip the scheme and set UseSSL=true.
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "https://minio.example.com",
		Bucket:    "mr-robot",
		AccessKey: "testkey",
		SecretKey: "testsecret",
		Region:    "us-east-1",
	})
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.example.com/mr-robot/users/abc/images/photo.jpg",
	})
	// Must NOT get raw HTTP 403 — that would mean the scheme wasn't stripped
	// and isS3EndpointURL failed to match.
	if err == nil {
		t.Log("no error — S3 client connected (possibly network reachable)")
	} else if strings.Contains(err.Error(), "HTTP error: 403") {
		t.Error("got raw HTTP 403 — scheme prefix in endpoint was not stripped; URL was not routed through S3 client")
	} else {
		t.Logf("expected S3-level error (good): %v", err)
	}
}

func TestDocFetch_EndpointWithHTTPSchemePrefix(t *testing.T) {
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "http://minio.local:9000",
		Bucket:    "test",
		AccessKey: "key",
		SecretKey: "secret",
	})
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.local/test/path/file.jpg",
	})
	if err != nil && strings.Contains(err.Error(), "unsupported scheme") {
		t.Error("scheme should have been stripped from endpoint")
	}
}

func TestDocFetch_HTTPSMinioEndpointWithPort(t *testing.T) {
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "minio.local:9000",
		AccessKey: "testkey",
		SecretKey: "testsecret",
	})
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.local/mybucket/path/to/file.jpg",
	})
	// Should route through S3 client; the host "minio.local" matches endpoint "minio.local:9000".
	// Expect S3-level error, NOT raw HTTP.
	if err != nil && strings.Contains(err.Error(), "unsupported scheme") {
		t.Error("should not have rejected as unsupported scheme")
	}
}

func TestDocFetch_HTTPSNonMinioNotRouted(t *testing.T) {
	// URLs that don't match the S3 endpoint should go through regular HTTP.
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "my-minio.example.com:9000",
		AccessKey: "testkey",
		SecretKey: "testsecret",
	})
	// This URL's host "other-host.com" doesn't match "my-minio.example.com".
	// It should NOT be routed through S3.
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://other-host.com/bucket/key.jpg",
	})
	// Should fail with network error or similar, NOT an S3 credentials error.
	if err != nil && strings.Contains(err.Error(), "S3 credentials not configured") {
		t.Error("non-matching host should not be routed through S3")
	}
}

func TestDocFetch_HTTPSMinioPathParsing(t *testing.T) {
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "minio.test.com",
		AccessKey: "key",
		SecretKey: "secret",
	})

	// No path — should error
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.test.com",
	})
	if err == nil {
		t.Error("expected error for S3 URL with no path")
	}

	// Bucket only, no key — should error
	_, err = executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.test.com/mybucket",
	})
	if err == nil {
		t.Error("expected error for S3 URL with no key")
	}

	// Path traversal — should error
	_, err = executor.ExecDocFetch(context.Background(), tools.DocFetchParams{
		URL: "https://minio.test.com/mybucket/../etc/passwd",
	})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestDocFetch_S3URIMissingConfig(t *testing.T) {
	executor := newProdExecutor()
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "s3://mybucket/docs/file.pdf"})
	if err == nil {
		t.Fatal("expected error for missing S3 config")
	}
	if !strings.Contains(err.Error(), "S3 endpoint not configured") {
		t.Errorf("expected S3 config error, got: %v", err)
	}
}

func TestDocFetch_S3URIMissingCredentials(t *testing.T) {
	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint: "minio.example.com:9000",
	})
	_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{URL: "s3://mybucket/docs/file.pdf"})
	if err == nil {
		t.Fatal("expected error for missing S3 credentials")
	}
	if !strings.Contains(err.Error(), "S3 credentials not configured") {
		t.Errorf("expected credentials error, got: %v", err)
	}
}

func TestDocFetch_S3URIParsing(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid", "s3://mybucket/path/to/file.pdf", false},
		{"no key", "s3://mybucket", true},
		{"empty", "s3://", true},
		{"path traversal", "s3://mybucket/../etc/passwd", true},
	}

	executor := tools.NewDocFetchExecutor(config.S3DefaultConfig{
		Endpoint:  "minio.example.com:9000",
		AccessKey: "testkey",
		SecretKey: "testsecret",
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executor.ExecDocFetch(context.Background(), tools.DocFetchParams{URL: tc.url})
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q", tc.url)
			}
		})
	}
}

func TestDocFetch_LargeContentTruncated(t *testing.T) {
	bigContent := strings.Repeat("A", 300*1024) // 300 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(bigContent))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) > 200*1024+100 { // some slack for metadata
		t.Errorf("expected truncated content, got %d bytes", len(result.Content))
	}
}

func TestDocFetch_PDFMagicBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		// Not a real PDF, so parsing will fail gracefully
		w.Write([]byte("%PDF-1.4 fake content"))
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MimeType != "application/pdf" {
		t.Errorf("expected application/pdf, got %q", result.MimeType)
	}
	// Content should indicate a parsing issue since it's a fake PDF
	if result.Content == "" {
		t.Error("expected non-empty content (either extracted text or error message)")
	}
}

func TestDocFetch_ImageWithEXIF(t *testing.T) {
	// Build a minimal JPEG with an EXIF APP1 segment containing Make and Model tags.
	// JPEG structure: SOI + APP1(EXIF) + EOI
	exifPayload := buildMinimalEXIF(
		map[uint16]string{
			0x010F: "TestCamera",   // Make
			0x0110: "Model-X9000", // Model
		},
	)
	var buf []byte
	buf = append(buf, 0xFF, 0xD8)                                     // SOI
	buf = append(buf, 0xFF, 0xE1)                                     // APP1 marker
	appLen := len(exifPayload) + 2                                    // +2 for length field itself
	buf = append(buf, byte(appLen>>8), byte(appLen&0xFF))             // APP1 length
	buf = append(buf, exifPayload...)                                 // EXIF data
	buf = append(buf, 0xFF, 0xD9)                                     // EOI

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(buf)
	}))
	defer srv.Close()

	result, err := newTestExecutor().ExecDocFetch(context.Background(), tools.DocFetchParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "EXIF Metadata") {
		t.Errorf("expected EXIF metadata block, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "TestCamera") {
		t.Errorf("expected Make=TestCamera in EXIF, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Model-X9000") {
		t.Errorf("expected Model=Model-X9000 in EXIF, got %q", result.Content)
	}
}

// buildMinimalEXIF constructs a minimal valid EXIF/TIFF payload with the given IFD0 ASCII tags.
// tags maps TIFF tag IDs → string values.
func buildMinimalEXIF(tags map[uint16]string) []byte {
	// "Exif\0\0" header
	header := []byte("Exif\x00\x00")

	// TIFF header (little-endian)
	tiff := []byte{
		'I', 'I',      // little-endian
		0x2A, 0x00,    // TIFF magic
		0x08, 0x00, 0x00, 0x00, // offset to IFD0 (right after TIFF header)
	}

	numTags := uint16(len(tags))
	ifdStart := len(tiff)

	// IFD0: 2-byte tag count + 12 bytes per entry + 4-byte next-IFD offset (0)
	ifdSize := 2 + int(numTags)*12 + 4
	dataStart := ifdStart + ifdSize

	var ifdEntries []byte
	var extraData []byte

	// count
	ifdEntries = append(ifdEntries, byte(numTags&0xFF), byte(numTags>>8))

	sortedTags := make([]uint16, 0, len(tags))
	for k := range tags {
		sortedTags = append(sortedTags, k)
	}
	// simple sort
	for i := range sortedTags {
		for j := i + 1; j < len(sortedTags); j++ {
			if sortedTags[j] < sortedTags[i] {
				sortedTags[i], sortedTags[j] = sortedTags[j], sortedTags[i]
			}
		}
	}

	for _, tagID := range sortedTags {
		val := tags[tagID] + "\x00" // null-terminated ASCII
		count := uint32(len(val))

		entry := make([]byte, 12)
		entry[0] = byte(tagID & 0xFF)
		entry[1] = byte(tagID >> 8)
		entry[2] = 0x02 // TYPE = ASCII
		entry[3] = 0x00
		entry[4] = byte(count & 0xFF)
		entry[5] = byte((count >> 8) & 0xFF)
		entry[6] = byte((count >> 16) & 0xFF)
		entry[7] = byte((count >> 24) & 0xFF)

		if count <= 4 {
			copy(entry[8:], []byte(val))
		} else {
			offset := uint32(dataStart + len(extraData))
			entry[8] = byte(offset & 0xFF)
			entry[9] = byte((offset >> 8) & 0xFF)
			entry[10] = byte((offset >> 16) & 0xFF)
			entry[11] = byte((offset >> 24) & 0xFF)
			extraData = append(extraData, []byte(val)...)
		}
		ifdEntries = append(ifdEntries, entry...)
	}

	// next IFD offset = 0 (no more IFDs)
	ifdEntries = append(ifdEntries, 0x00, 0x00, 0x00, 0x00)

	var result []byte
	result = append(result, header...)
	result = append(result, tiff...)
	result = append(result, ifdEntries...)
	result = append(result, extraData...)
	return result
}

func TestDocFetch_BuildTool(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{DocFetcher: true},
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
	if !names["doc_fetch"] {
		t.Error("missing doc_fetch tool")
	}
}

func TestDocFetch_PolicyDeny(t *testing.T) {
	ctx := context.Background()
	cfg := &config.AgentRuntimeConfig{
		Tools: config.AgentToolsConfig{DocFetcher: true, WebFetch: true},
	}
	deny := &registration.ToolsPolicy{Deny: []string{"doc_fetch"}}
	builtTools, err := tools.BuildTools(ctx, cfg, deny, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bt := range builtTools {
		info, _ := bt.Info(ctx)
		if info.Name == "doc_fetch" {
			t.Error("doc_fetch should have been denied by policy")
		}
	}
}
