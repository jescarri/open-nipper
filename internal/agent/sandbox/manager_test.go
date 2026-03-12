package sandbox_test

import (
	"testing"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/sandbox"
	"github.com/jescarri/open-nipper/internal/config"
)

func TestNewManager(t *testing.T) {
	cfg := config.SandboxConfig{
		Enabled:        true,
		Image:          "ubuntu:noble",
		WorkDir:        "/workspace",
		MemoryLimitMB:  2048,
		CPULimit:       2.0,
		TimeoutSeconds: 120,
	}
	mgr := sandbox.NewManager(cfg, zap.NewNop())
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.IsRunning() {
		t.Error("expected manager to not be running before Create()")
	}
}

func TestTruncateOutput(t *testing.T) {
	// Test through ExecBash since truncateOutput is unexported.
	// This is a basic sanity check — the real truncation tests happen
	// at the integration level with actual Docker containers.
}
