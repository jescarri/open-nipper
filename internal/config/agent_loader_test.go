package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/open-nipper/open-nipper/internal/config"
)

func TestLoadAgentConfig_Defaults(t *testing.T) {
	cfg, err := config.LoadAgentConfig("") // no file → all defaults
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.BasePath == "" {
		t.Error("expected non-empty base_path default")
	}
	// BasePath must be expanded to actual path (no literal ~ or ${HOME})
	if cfg.Agent.BasePath == "~/.open-nipper" || cfg.Agent.BasePath == "${HOME}/.open-nipper" {
		t.Errorf("base_path must be expanded to actual home path, got %q", cfg.Agent.BasePath)
	}
	if cfg.Agent.MaxSteps <= 0 {
		t.Errorf("expected positive max_steps default, got %d", cfg.Agent.MaxSteps)
	}
	if cfg.Agent.Inference.Provider == "" {
		t.Error("expected non-empty inference.provider default")
	}
}

func TestLoadAgentConfig_FromYAML(t *testing.T) {
	yaml := `
agent:
  base_path: "/tmp/test-nipper"
  inference:
    provider: "ollama"
    model: "llama3"
    base_url: "http://localhost:11434"
    temperature: 0.5
    max_tokens: 2048
  max_steps: 10
  prompt:
    system_prompt: "Be concise."
  tools:
    web_fetch: true
    bash: false
`
	f, err := os.CreateTemp("", "agent-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := config.LoadAgentConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.BasePath != "/tmp/test-nipper" {
		t.Errorf("base_path: got %q, want /tmp/test-nipper", cfg.Agent.BasePath)
	}
	if cfg.Agent.Inference.Provider != "ollama" {
		t.Errorf("provider: got %q, want ollama", cfg.Agent.Inference.Provider)
	}
	if cfg.Agent.Inference.Model != "llama3" {
		t.Errorf("model: got %q, want llama3", cfg.Agent.Inference.Model)
	}
	if cfg.Agent.MaxSteps != 10 {
		t.Errorf("max_steps: got %d, want 10", cfg.Agent.MaxSteps)
	}
	if !cfg.Agent.Tools.WebFetch {
		t.Error("expected tools.web_fetch=true")
	}
	if cfg.Agent.Tools.Bash {
		t.Error("expected tools.bash=false")
	}
}

func TestLoadAgentConfig_EnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "sk-test-from-env")
	t.Setenv("TEST_MINIO_ENDPOINT", "https://minio.example.com")
	t.Setenv("TEST_MINIO_BUCKET", "mr-robot")
	t.Setenv("TEST_MINIO_ACCESS", "AKIATEST")
	t.Setenv("TEST_MINIO_SECRET", "SECRETTEST")

	yaml := `
agent:
  inference:
    provider: "openai"
    model: "gpt-4o"
    api_key: "${TEST_OPENAI_KEY}"
  s3:
    endpoint: "${TEST_MINIO_ENDPOINT}"
    bucket: "${TEST_MINIO_BUCKET}"
    access_key: "${TEST_MINIO_ACCESS}"
    secret_key: "${TEST_MINIO_SECRET}"
`
	f, err := os.CreateTemp("", "agent-env-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := config.LoadAgentConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.Inference.APIKey != "sk-test-from-env" {
		t.Errorf("api_key: got %q, want sk-test-from-env", cfg.Agent.Inference.APIKey)
	}

	if cfg.Agent.S3.Endpoint != "https://minio.example.com" {
		t.Errorf("s3.endpoint: got %q, want https://minio.example.com", cfg.Agent.S3.Endpoint)
	}
	if cfg.Agent.S3.Bucket != "mr-robot" {
		t.Errorf("s3.bucket: got %q, want mr-robot", cfg.Agent.S3.Bucket)
	}
	if cfg.Agent.S3.AccessKey != "AKIATEST" {
		t.Errorf("s3.access_key: got %q, want AKIATEST", cfg.Agent.S3.AccessKey)
	}
	if cfg.Agent.S3.SecretKey != "SECRETTEST" {
		t.Errorf("s3.secret_key: got %q, want SECRETTEST", cfg.Agent.S3.SecretKey)
	}
}

func TestLoadAgentConfig_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent.yaml")
	cfg, err := config.LoadAgentConfig(missing)
	// Missing file is not an error — defaults are used.
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config even for missing file")
	}
}
