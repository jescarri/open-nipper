package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestNewLoader_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if n := len(l.Skills()); n != 0 {
		t.Errorf("expected 0 skills, got %d", n)
	}
	if s := l.BuildPromptSection(); s != "" {
		t.Errorf("expected empty prompt section, got %q", s)
	}
	if _, ok := l.SkillByName("x"); ok {
		t.Error("SkillByName should return false for missing skill")
	}
}

func TestNewLoader_NoSkillsDir(t *testing.T) {
	dir := t.TempDir()
	// use a non-existent subpath so .../skills does not exist
	base := filepath.Join(dir, "agent", "data")
	logger := zap.NewNop()
	l, err := NewLoader(base, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if n := len(l.Skills()); n != 0 {
		t.Errorf("expected 0 skills when skills dir missing, got %d", n)
	}
}

func TestNewLoader_OneSkillDescriptionOnly(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(skillsDir, "deploy")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillMD := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("# Deploy\nDeploy the app."), 0644); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "deploy" {
		t.Errorf("skill name: got %q", skills[0].Name)
	}
	if skills[0].Description != "# Deploy\nDeploy the app." {
		t.Errorf("description: got %q", skills[0].Description)
	}
	if skills[0].Config != nil {
		t.Error("expected nil Config for description-only skill")
	}
	got, ok := l.SkillByName("deploy")
	if !ok || got.Name != "deploy" {
		t.Errorf("SkillByName(deploy): ok=%v, got %+v", ok, got)
	}
	section := l.BuildPromptSection()
	if section == "" {
		t.Error("expected non-empty prompt section")
	}
	if !strings.Contains(section, "Available Skills") || !strings.Contains(section, "<skill name=\"deploy\">") {
		t.Errorf("prompt section missing expected parts: %s", section)
	}
}

func TestNewLoader_PassSkillsDirDirectly(t *testing.T) {
	// When config provides agent.skills.path = base_path/skills, we pass the full path to NewLoader.
	// Loader should treat it as the skills dir (not append /skills again).
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsDir, "plant-care")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Plant Care\nWater your plants."), 0644); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(skillsDir, logger) // pass skills dir directly, not parent
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill when passing skills dir directly, got %d", len(skills))
	}
	if skills[0].Name != "plant-care" {
		t.Errorf("skill name: got %q", skills[0].Name)
	}
}

func TestNewLoader_OneSkillWithConfig(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsDir, "deploy")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Deploy"), 0644); err != nil {
		t.Fatal(err)
	}
	configYAML := `name: deploy
version: "1.0.0"
runtime: bash
entrypoint: scripts/run.sh
timeout: 300
secrets:
  - name: ssh_key
    env_var: DEPLOY_SSH_KEY
    provider: env
    ref: DEPLOY_SSH_KEY
`
	if err := os.WriteFile(filepath.Join(skillDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	cfg := skills[0].Config
	if cfg == nil {
		t.Fatal("expected non-nil Config")
	}
	if cfg.Name != "deploy" || cfg.Runtime != "bash" || cfg.Entrypoint != "scripts/run.sh" || cfg.Timeout != 300 {
		t.Errorf("config: %+v", cfg)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Name != "ssh_key" || cfg.Secrets[0].EnvVar != "DEPLOY_SSH_KEY" {
		t.Errorf("secrets: %+v", cfg.Secrets)
	}
}

func TestNewLoader_TypeMCP(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsDir, "plant-care")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Plant Care\nUse GetLiveContext for soil sensors."), 0644); err != nil {
		t.Fatal(err)
	}
	configYAML := `name: plant-care
version: "1.0.0"
type: mcp
`
	if err := os.WriteFile(filepath.Join(skillDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(skillsDir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if !skills[0].IsMCPOnly() {
		t.Error("expected IsMCPOnly() true for type: mcp")
	}
	section := l.BuildPromptSection()
	if section == "" {
		t.Fatal("expected non-empty prompt section")
	}
	if !strings.Contains(section, "MCP-only") {
		t.Errorf("prompt section should tag MCP-only skill: %s", section)
	}
}

func TestNewLoader_MultipleSkills(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	for _, name := range []string{"alpha", "beta", "deploy"} {
		skillDir := filepath.Join(skillsDir, name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name), 0644); err != nil {
			t.Fatal(err)
		}
	}

	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
	// should be sorted by name
	if skills[0].Name != "alpha" || skills[1].Name != "beta" || skills[2].Name != "deploy" {
		t.Errorf("order: %s, %s, %s", skills[0].Name, skills[1].Name, skills[2].Name)
	}
}

func TestNewLoader_SkipsDirWithoutSKILLMD(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// valid skill
	skillDir := filepath.Join(skillsDir, "valid")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Valid"), 0644); err != nil {
		t.Fatal(err)
	}
	// dir without SKILL.md
	if err := os.MkdirAll(filepath.Join(skillsDir, "no-skill-md"), 0755); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 || skills[0].Name != "valid" {
		t.Errorf("expected one skill 'valid', got %d skills: %v", len(skills), l.Skills())
	}
}

func TestNewLoader_MalformedConfigYAML(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsDir, "broken")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "config.yaml"), []byte("not: valid: yaml: ["), 0644); err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	l, err := NewLoader(dir, logger)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	skills := l.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (description-only), got %d", len(skills))
	}
	if skills[0].Config != nil {
		t.Error("expected nil Config when config.yaml is malformed")
	}
}

