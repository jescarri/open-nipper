package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Load from a non-existent file so only defaults apply.
	cfg, err := Load("/tmp/does-not-exist-nipper.yaml")
	if err != nil {
		t.Fatalf("Load returned error for missing config: %v", err)
	}
	if cfg.Gateway.Port != 18789 {
		t.Errorf("expected default port 18789, got %d", cfg.Gateway.Port)
	}
	if cfg.Gateway.Admin.Port != 18790 {
		t.Errorf("expected default admin port 18790, got %d", cfg.Gateway.Admin.Port)
	}
	if cfg.Gateway.Bind != "127.0.0.1" {
		t.Errorf("expected bind 127.0.0.1, got %s", cfg.Gateway.Bind)
	}
	if cfg.Queue.DefaultMode != "steer" {
		t.Errorf("expected default queue mode 'steer', got %s", cfg.Queue.DefaultMode)
	}
	if cfg.Security.RateLimit.PerUser.MessagesPerMinute != 20 {
		t.Errorf("expected default rate limit 20/min, got %d", cfg.Security.RateLimit.PerUser.MessagesPerMinute)
	}
}

func TestLoad_FromYAML(t *testing.T) {
	content := `
gateway:
  bind: "0.0.0.0"
  port: 9000
  admin:
    port: 9001
`
	tmp, err := os.CreateTemp("", "nipper-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	cfg, err := Load(tmp.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gateway.Port != 9000 {
		t.Errorf("expected port 9000, got %d", cfg.Gateway.Port)
	}
	if cfg.Gateway.Admin.Port != 9001 {
		t.Errorf("expected admin port 9001, got %d", cfg.Gateway.Admin.Port)
	}
}

func TestLoad_LocalOverlay(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	local := filepath.Join(dir, "config.local.yaml")

	if err := os.WriteFile(base, []byte("gateway:\n  port: 8000\n  admin:\n    port: 8001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("gateway:\n  port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gateway.Port != 8080 {
		t.Errorf("expected local overlay port 8080, got %d", cfg.Gateway.Port)
	}
}

func TestLoad_EnvVarOverride(t *testing.T) {
	t.Setenv("NIPPER_GATEWAY_PORT", "19000")

	cfg, err := Load("/tmp/does-not-exist-nipper.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gateway.Port != 19000 {
		t.Errorf("expected env-override port 19000, got %d", cfg.Gateway.Port)
	}
}

func TestLoad_EnvPlaceholderResolution(t *testing.T) {
	t.Setenv("TEST_WUZAPI_TOKEN", "abc123")

	content := "channels:\n  whatsapp:\n    enabled: false\n    config:\n      wuzapi_token: \"${TEST_WUZAPI_TOKEN}\"\n"
	tmp, err := os.CreateTemp("", "nipper-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	cfg, err := Load(tmp.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Channels.WhatsApp.Config.WuzapiToken != "abc123" {
		t.Errorf("expected resolved token 'abc123', got %q", cfg.Channels.WhatsApp.Config.WuzapiToken)
	}
}

func TestValidate_SamePort(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	cfg.Gateway.Admin.Port = cfg.Gateway.Port

	if err := Validate(cfg); err == nil {
		t.Error("expected error when gateway and admin ports are equal")
	}
}

func TestResolveString(t *testing.T) {
	t.Setenv("MY_SECRET", "supersecret")
	got := resolveString("prefix_${MY_SECRET}_suffix")
	want := "prefix_supersecret_suffix"
	if got != want {
		t.Errorf("resolveString = %q, want %q", got, want)
	}
}

func TestResolveString_MissingEnv(t *testing.T) {
	os.Unsetenv("DEFINITELY_NOT_SET_VAR")
	got := resolveString("${DEFINITELY_NOT_SET_VAR}")
	if got != "${DEFINITELY_NOT_SET_VAR}" {
		t.Errorf("expected unresolved placeholder, got %q", got)
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := expandTilde("~/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "foo/bar")
	if got != want {
		t.Errorf("expandTilde = %q, want %q", got, want)
	}
}
