package mcp

import (
	"os"
	"testing"
	"time"

	"github.com/open-nipper/open-nipper/internal/config"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MCPServerConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty name",
			cfg:     config.MCPServerConfig{Transport: "sse", URL: "http://example.com/sse"},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name:    "invalid transport",
			cfg:     config.MCPServerConfig{Name: "test", Transport: "grpc"},
			wantErr: true,
			errMsg:  "transport must be",
		},
		{
			name:    "sse without url",
			cfg:     config.MCPServerConfig{Name: "test", Transport: "sse"},
			wantErr: true,
			errMsg:  "SSE transport requires url",
		},
		{
			name:    "stdio without command",
			cfg:     config.MCPServerConfig{Name: "test", Transport: "stdio"},
			wantErr: true,
			errMsg:  "STDIO transport requires command",
		},
		{
			name: "valid sse config",
			cfg: config.MCPServerConfig{
				Name:      "test-sse",
				Transport: "sse",
				URL:       "http://example.com/sse",
			},
			wantErr: false,
		},
		{
			name: "valid stdio config",
			cfg: config.MCPServerConfig{
				Name:      "test-stdio",
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-github"},
			},
			wantErr: false,
		},
		{
			name: "sse with headers",
			cfg: config.MCPServerConfig{
				Name:      "test-sse-auth",
				Transport: "sse",
				URL:       "http://example.com/sse",
				Headers:   map[string]string{"Authorization": "Bearer token123"},
			},
			wantErr: false,
		},
		{
			name: "case insensitive transport",
			cfg: config.MCPServerConfig{
				Name:      "test",
				Transport: "SSE",
				URL:       "http://example.com/sse",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !containsStr(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestExpandConfig(t *testing.T) {
	os.Setenv("TEST_MCP_URL", "http://mcp.example.com/sse")
	os.Setenv("TEST_MCP_TOKEN", "secret-token-123")
	os.Setenv("TEST_MCP_CMD", "/usr/local/bin/mcp-server")
	os.Setenv("GITHUB_TOKEN", "ghp_test123")
	defer func() {
		os.Unsetenv("TEST_MCP_URL")
		os.Unsetenv("TEST_MCP_TOKEN")
		os.Unsetenv("TEST_MCP_CMD")
		os.Unsetenv("GITHUB_TOKEN")
	}()

	t.Run("expands URL", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "test",
			Transport: "sse",
			URL:       "${TEST_MCP_URL}",
		}
		expanded := expandConfig(cfg)
		if expanded.URL != "http://mcp.example.com/sse" {
			t.Fatalf("URL not expanded: got %q", expanded.URL)
		}
	})

	t.Run("expands headers", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "test",
			Transport: "sse",
			URL:       "http://example.com/sse",
			Headers: map[string]string{
				"Authorization": "Bearer ${TEST_MCP_TOKEN}",
				"X-Static":     "static-value",
			},
		}
		expanded := expandConfig(cfg)
		if expanded.Headers["Authorization"] != "Bearer secret-token-123" {
			t.Fatalf("Authorization header not expanded: got %q", expanded.Headers["Authorization"])
		}
		if expanded.Headers["X-Static"] != "static-value" {
			t.Fatalf("static header changed: got %q", expanded.Headers["X-Static"])
		}
	})

	t.Run("expands command", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "test",
			Transport: "stdio",
			Command:   "${TEST_MCP_CMD}",
			Args:      []string{"-v", "--token=${TEST_MCP_TOKEN}"},
		}
		expanded := expandConfig(cfg)
		if expanded.Command != "/usr/local/bin/mcp-server" {
			t.Fatalf("command not expanded: got %q", expanded.Command)
		}
		if expanded.Args[1] != "--token=secret-token-123" {
			t.Fatalf("arg not expanded: got %q", expanded.Args[1])
		}
	})

	t.Run("expands env vars", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "test",
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"-y", "@modelcontextprotocol/server-github"},
			Env:       []string{"GITHUB_TOKEN=${GITHUB_TOKEN}", "STATIC=value"},
		}
		expanded := expandConfig(cfg)
		if expanded.Env[0] != "GITHUB_TOKEN=ghp_test123" {
			t.Fatalf("env var not expanded: got %q", expanded.Env[0])
		}
		if expanded.Env[1] != "STATIC=value" {
			t.Fatalf("static env changed: got %q", expanded.Env[1])
		}
	})

	t.Run("preserves unresolvable vars", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "test",
			Transport: "sse",
			URL:       "${NONEXISTENT_VAR}",
		}
		expanded := expandConfig(cfg)
		if expanded.URL != "${NONEXISTENT_VAR}" {
			t.Fatalf("unresolvable var should be preserved: got %q", expanded.URL)
		}
	})

	t.Run("name and transport not expanded", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:      "my-server",
			Transport: "sse",
			URL:       "http://example.com",
		}
		expanded := expandConfig(cfg)
		if expanded.Name != "my-server" {
			t.Fatalf("name should not be expanded: got %q", expanded.Name)
		}
		if expanded.Transport != "sse" {
			t.Fatalf("transport should not be expanded: got %q", expanded.Transport)
		}
	})

	t.Run("preserves KeepAliveSeconds", func(t *testing.T) {
		cfg := config.MCPServerConfig{
			Name:             "test",
			Transport:        "sse",
			URL:              "http://example.com",
			KeepAliveSeconds: 45,
		}
		expanded := expandConfig(cfg)
		if expanded.KeepAliveSeconds != 45 {
			t.Fatalf("KeepAliveSeconds not preserved: got %d", expanded.KeepAliveSeconds)
		}
	})
}

func TestExpandEnvSlice(t *testing.T) {
	os.Setenv("TEST_VAL", "hello")
	defer os.Unsetenv("TEST_VAL")

	envs := []string{"KEY=${TEST_VAL}", "PLAIN=world"}
	result := expandEnvSlice(envs)

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	if result[0] != "KEY=hello" {
		t.Fatalf("expected KEY=hello, got %q", result[0])
	}
	if result[1] != "PLAIN=world" {
		t.Fatalf("expected PLAIN=world, got %q", result[1])
	}
}

func TestNewLoaderEmptyConfig(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("empty config should not error: %v", err)
	}
	if loader == nil {
		t.Fatal("loader should not be nil")
	}
	if len(loader.Tools()) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(loader.Tools()))
	}
	loader.Close()
}

func TestNewLoaderInvalidConfig(t *testing.T) {
	configs := []config.MCPServerConfig{
		{Name: "", Transport: "sse", URL: "http://example.com"},
	}
	_, err := NewLoader(t.Context(), configs, nil)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
	if !containsStr(err.Error(), "name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewLoaderInvalidTransport(t *testing.T) {
	configs := []config.MCPServerConfig{
		{Name: "test", Transport: "grpc", URL: "http://example.com"},
	}
	_, err := NewLoader(t.Context(), configs, nil)
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
	if !containsStr(err.Error(), "transport must be") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderNilSafety(t *testing.T) {
	var l *Loader
	if tools := l.Tools(); tools != nil {
		t.Fatal("nil loader should return nil tools")
	}
	if names := l.ToolNames(); names != nil {
		t.Fatal("nil loader should return nil names")
	}
	if infos := l.ToolInfos(); infos != nil {
		t.Fatal("nil loader should return nil infos")
	}
	l.Close() // should not panic
}

func TestToolNames(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := loader.ToolNames()
	if len(names) != 0 {
		t.Fatalf("expected 0 names, got %d", len(names))
	}
	loader.Close()
}

func TestKeepaliveInterval(t *testing.T) {
	l := &Loader{
		configs: []config.MCPServerConfig{
			{Name: "a", Transport: "sse", KeepAliveSeconds: 0},
			{Name: "b", Transport: "sse", KeepAliveSeconds: 15},
			{Name: "c", Transport: "stdio"},
		},
	}
	got := l.keepaliveInterval()
	want := 15 * time.Second
	if got != want {
		t.Fatalf("keepaliveInterval() = %v, want %v", got, want)
	}
}

func TestKeepaliveIntervalDefault(t *testing.T) {
	l := &Loader{
		configs: []config.MCPServerConfig{
			{Name: "a", Transport: "sse", KeepAliveSeconds: 0},
		},
	}
	got := l.keepaliveInterval()
	if got != defaultKeepAliveInterval {
		t.Fatalf("keepaliveInterval() = %v, want default %v", got, defaultKeepAliveInterval)
	}
}

func TestCloseStopsContext(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loader.Close()

	select {
	case <-loader.ctx.Done():
		// expected
	default:
		t.Fatal("Close() should cancel the loader context")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	loader.Close()
	loader.Close() // second call should not panic
}

func TestToolsReturnsSnapshot(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer loader.Close()

	t1 := loader.Tools()
	t2 := loader.Tools()
	if t1 == nil || t2 == nil {
		return
	}

	// Two calls should return independent slices (not the same backing array).
	if len(t1) > 0 && len(t2) > 0 {
		t1[0] = nil // mutate one; the other should be unaffected
		if t2[0] == nil {
			t.Fatal("Tools() should return a copy, not a reference to internal state")
		}
	}
}

func TestWaitForReconnectNoReconnection(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer loader.Close()

	// No clients are reconnecting, should return immediately.
	if !loader.WaitForReconnect(t.Context(), 1*time.Second) {
		t.Fatal("WaitForReconnect should return true when nothing is reconnecting")
	}
}

func TestWaitForReconnectCompletesWhenFlagClears(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer loader.Close()

	mc := &managedClient{name: "test"}
	mc.reconnecting.Store(true)
	loader.mu.Lock()
	loader.clients = append(loader.clients, mc)
	loader.mu.Unlock()

	done := make(chan bool, 1)
	go func() {
		done <- loader.WaitForReconnect(t.Context(), 5*time.Second)
	}()

	// Simulate reconnection completing after a short delay.
	time.Sleep(300 * time.Millisecond)
	mc.reconnecting.Store(false)

	result := <-done
	if !result {
		t.Fatal("WaitForReconnect should return true after reconnection completes")
	}
}

func TestWaitForReconnectTimeout(t *testing.T) {
	loader, err := NewLoader(t.Context(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer loader.Close()

	mc := &managedClient{name: "test"}
	mc.reconnecting.Store(true)
	loader.mu.Lock()
	loader.clients = append(loader.clients, mc)
	loader.mu.Unlock()

	start := time.Now()
	result := loader.WaitForReconnect(t.Context(), 500*time.Millisecond)
	elapsed := time.Since(start)

	if result {
		t.Fatal("WaitForReconnect should return false on timeout")
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("timed out too fast: %v", elapsed)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
