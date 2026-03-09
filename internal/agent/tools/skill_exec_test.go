package tools

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/skills"
)

func TestBuildSkillExecTool_RequiresLoaderAndExecutor(t *testing.T) {
	_, err := BuildSkillExecTool(nil, nil, zap.NewNop())
	if err == nil {
		t.Error("expected error when loader and executor are nil")
	}

	loader, err := loadSkillsForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildSkillExecTool(loader, nil, zap.NewNop())
	if err == nil {
		t.Error("expected error when executor is nil")
	}
}

// loadSkillsForTest creates a minimal loader from a temp dir with one skill (for tests that need a valid loader).
func loadSkillsForTest(t *testing.T) (*skills.Loader, error) {
	t.Helper()
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	deployDir := filepath.Join(skillsDir, "deploy")
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(deployDir, "SKILL.md"), []byte("# Deploy\nDeploy the app."), 0644); err != nil {
		return nil, err
	}
	return skills.NewLoader(dir, zap.NewNop())
}
