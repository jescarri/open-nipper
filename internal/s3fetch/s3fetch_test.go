package s3fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jescarri/open-nipper/internal/config"
)

func TestNormalizeS3Config(t *testing.T) {
	tests := []struct {
		name     string
		input    config.S3DefaultConfig
		wantEP   string
		wantSSL  bool
	}{
		{
			name:    "https prefix stripped",
			input:   config.S3DefaultConfig{Endpoint: "https://minio.example.com"},
			wantEP:  "minio.example.com",
			wantSSL: true,
		},
		{
			name:    "http prefix stripped",
			input:   config.S3DefaultConfig{Endpoint: "http://minio.example.com"},
			wantEP:  "minio.example.com",
			wantSSL: false,
		},
		{
			name:    "trailing slash stripped",
			input:   config.S3DefaultConfig{Endpoint: "minio.example.com/"},
			wantEP:  "minio.example.com",
			wantSSL: false,
		},
		{
			name:    "no scheme kept as-is",
			input:   config.S3DefaultConfig{Endpoint: "minio.example.com:9000"},
			wantEP:  "minio.example.com:9000",
			wantSSL: false,
		},
		{
			name:    "https with port",
			input:   config.S3DefaultConfig{Endpoint: "https://minio.example.com:9000/"},
			wantEP:  "minio.example.com:9000",
			wantSSL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeS3Config(tt.input)
			if got.Endpoint != tt.wantEP {
				t.Errorf("endpoint = %q, want %q", got.Endpoint, tt.wantEP)
			}
			if got.UseSSL != tt.wantSSL {
				t.Errorf("useSSL = %v, want %v", got.UseSSL, tt.wantSSL)
			}
		})
	}
}

func TestParseS3URI(t *testing.T) {
	tests := []struct {
		uri        string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{"s3://mybucket/path/to/file.ogg", "mybucket", "path/to/file.ogg", false},
		{"s3://mybucket/file.txt", "mybucket", "file.txt", false},
		{"s3://mybucket", "mybucket", "", true},
		{"s3://", "", "", true},
		{"https://not-s3", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			bucket, key, err := ParseS3URI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
			}
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
		})
	}
}

func TestParseHTTPURL(t *testing.T) {
	tests := []struct {
		url        string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{"https://minio.local:9000/mybucket/path/file.ogg", "mybucket", "path/file.ogg", false},
		{"https://minio.local:9000/mybucket/file.txt", "mybucket", "file.txt", false},
		{"https://minio.local:9000/mybucket", "", "", true},  // no key
		{"https://minio.local:9000/", "", "", true},           // no path
		{"https://minio.local:9000", "", "", true},            // empty path
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			bucket, key, err := ParseHTTPURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if bucket != tt.wantBucket {
				t.Errorf("bucket = %q, want %q", bucket, tt.wantBucket)
			}
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
		})
	}
}

func TestValidateS3Path(t *testing.T) {
	tests := []struct {
		name    string
		bucket  string
		key     string
		wantErr bool
	}{
		{"valid", "mybucket", "path/file.ogg", false},
		{"bucket too short", "ab", "file.ogg", true},
		{"empty key", "mybucket", "", true},
		{"path traversal", "mybucket", "../etc/passwd", true},
		{"empty bucket valid if key set", "", "file.ogg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateS3Path(tt.bucket, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFetcher_IsS3EndpointURL(t *testing.T) {
	f := NewFetcher(config.S3DefaultConfig{
		Endpoint: "https://minio.example.com:9000",
	})

	tests := []struct {
		url  string
		want bool
	}{
		{"https://minio.example.com:9000/bucket/key", true},
		{"https://minio.example.com/bucket/key", true},
		{"https://other.example.com/bucket/key", false},
		{"s3://bucket/key", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := f.IsS3EndpointURL(tt.url)
			if got != tt.want {
				t.Errorf("IsS3EndpointURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestFetcher_IsS3EndpointURL_EmptyConfig(t *testing.T) {
	f := NewFetcher(config.S3DefaultConfig{})
	if f.IsS3EndpointURL("https://anything.com/bucket/key") {
		t.Error("expected false with empty endpoint config")
	}
}

func TestFetcher_Fetch_PlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello-from-http"))
	}))
	defer server.Close()

	f := NewFetcher(config.S3DefaultConfig{
		Endpoint: "not-matching.example.com",
	})

	data, err := f.Fetch(context.Background(), server.URL+"/audio.ogg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello-from-http" {
		t.Errorf("got %q, want %q", string(data), "hello-from-http")
	}
}

func TestFetcher_Fetch_PlainHTTP_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	f := NewFetcher(config.S3DefaultConfig{})

	_, err := f.Fetch(context.Background(), server.URL+"/audio.ogg")
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if got := err.Error(); !contains(got, "403") {
		t.Errorf("expected error to mention 403, got: %v", err)
	}
}

func TestFetcher_Fetch_S3URI_NoCreds(t *testing.T) {
	f := NewFetcher(config.S3DefaultConfig{
		Endpoint: "minio.local:9000",
	})

	_, err := f.Fetch(context.Background(), "s3://mybucket/file.ogg")
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestFetcher_Fetch_S3EndpointURL_NoCreds(t *testing.T) {
	f := NewFetcher(config.S3DefaultConfig{
		Endpoint: "https://minio.local:9000",
	})

	_, err := f.Fetch(context.Background(), "https://minio.local:9000/mybucket/file.ogg")
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
