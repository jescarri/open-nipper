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

func TestOneLineDesc(t *testing.T) {
	tests := []struct {
		name   string
		skill  Skill
		expect string
	}{
		{
			name:   "config description takes precedence",
			skill:  Skill{Name: "deploy", Description: "# Deploy\nDeploy the app.", Config: &SkillConfig{Description: "Automate deployments"}},
			expect: "Automate deployments",
		},
		{
			name:   "fallback to first non-header line",
			skill:  Skill{Name: "deploy", Description: "# Deploy\nDeploy the app to production."},
			expect: "Deploy the app to production.",
		},
		{
			name:   "skips empty lines",
			skill:  Skill{Name: "test", Description: "# Test\n\n\nRun unit tests."},
			expect: "Run unit tests.",
		},
		{
			name:   "falls back to name when only headers",
			skill:  Skill{Name: "empty", Description: "# Empty\n## Also Empty"},
			expect: "empty",
		},
		{
			name:   "truncates long lines",
			skill:  Skill{Name: "long", Description: strings.Repeat("A", 150)},
			expect: strings.Repeat("A", 117) + "...",
		},
		{
			name:   "nil config uses description",
			skill:  Skill{Name: "foo", Description: "First line is content."},
			expect: "First line is content.",
		},
		{
			name:   "empty config description falls back",
			skill:  Skill{Name: "bar", Description: "# Bar\nBar does stuff.", Config: &SkillConfig{Description: ""}},
			expect: "Bar does stuff.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.skill.oneLineDesc()
			if got != tt.expect {
				t.Errorf("oneLineDesc() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestBuildSlimPromptSection(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	for _, s := range []struct{ name, md string }{
		{"alpha", "# Alpha\nDoes alpha things."},
		{"beta", "# Beta\nDoes beta things."},
	} {
		d := filepath.Join(skillsDir, s.name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(s.md), 0644); err != nil {
			t.Fatal(err)
		}
	}

	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	section := l.BuildSlimPromptSection()
	if section == "" {
		t.Fatal("expected non-empty slim section")
	}
	if !strings.Contains(section, "- alpha: Does alpha things.") {
		t.Errorf("missing alpha entry: %s", section)
	}
	if !strings.Contains(section, "- beta: Does beta things.") {
		t.Errorf("missing beta entry: %s", section)
	}
	// Should NOT contain full <skill> tags
	if strings.Contains(section, "<skill") {
		t.Error("slim section should not contain <skill> tags")
	}
}

func TestBuildSlimPromptSection_Empty(t *testing.T) {
	l, err := NewLoader("", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if s := l.BuildSlimPromptSection(); s != "" {
		t.Errorf("expected empty for no skills, got %q", s)
	}
}

func TestBuildSlimPromptSection_MCPTag(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	d := filepath.Join(skillsDir, "mcp-skill")
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# MCP Skill\nDoes MCP things."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "config.yaml"), []byte("name: mcp-skill\ntype: mcp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	section := l.BuildSlimPromptSection()
	if !strings.Contains(section, "[MCP-only]") {
		t.Errorf("expected [MCP-only] tag in slim section: %s", section)
	}
}

func TestBuildPromptSectionForSkills(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	for _, s := range []struct{ name, md string }{
		{"alpha", "# Alpha\nAlpha full description here."},
		{"beta", "# Beta\nBeta full description here."},
		{"gamma", "# Gamma\nGamma full description here."},
	} {
		d := filepath.Join(skillsDir, s.name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(s.md), 0644); err != nil {
			t.Fatal(err)
		}
	}

	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// Only "beta" is active: beta gets full description, alpha+gamma get slim
	section := l.BuildPromptSectionForSkills([]string{"beta"})
	if !strings.Contains(section, "<skill name=\"beta\">") {
		t.Errorf("expected full <skill> tag for beta: %s", section)
	}
	if strings.Contains(section, "<skill name=\"alpha\">") {
		t.Error("alpha should NOT have full <skill> tag")
	}
	if strings.Contains(section, "<skill name=\"gamma\">") {
		t.Error("gamma should NOT have full <skill> tag")
	}
	// Alpha and gamma should appear in slim list
	if !strings.Contains(section, "- alpha:") {
		t.Errorf("expected alpha in slim list: %s", section)
	}
	if !strings.Contains(section, "- gamma:") {
		t.Errorf("expected gamma in slim list: %s", section)
	}
	if !strings.Contains(section, "Other skills") {
		t.Errorf("expected 'Other skills' header: %s", section)
	}
}

func TestBuildPromptSectionForSkills_NilActiveShowsAll(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	d := filepath.Join(skillsDir, "only")
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# Only\nOnly skill."), 0644); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	section := l.BuildPromptSectionForSkills(nil)
	if !strings.Contains(section, "<skill name=\"only\">") {
		t.Errorf("nil activeSkills should show full desc: %s", section)
	}
	// No "Other skills" section when all are active
	if strings.Contains(section, "Other skills") {
		t.Error("should not have 'Other skills' when all skills have full descriptions")
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

func TestSetSandboxAvailable_FiltersScriptSkills(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")

	// Create a script skill (default type).
	scriptDir := filepath.Join(skillsDir, "deploy")
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "SKILL.md"), []byte("# Deploy\nDeploy the app."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "config.yaml"), []byte("name: deploy\nruntime: bash\nentrypoint: scripts/run.sh\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create an MCP-only skill.
	mcpDir := filepath.Join(skillsDir, "summarize")
	if err := os.MkdirAll(mcpDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "SKILL.md"), []byte("# Summarize\nSummarize URLs."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "config.yaml"), []byte("name: summarize\ntype: mcp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// Default: sandbox available = true, all skills visible.
	if len(l.AvailableSkills()) != 2 {
		t.Fatalf("expected 2 available skills with sandbox, got %d", len(l.AvailableSkills()))
	}

	// Disable sandbox: only MCP-only skills should remain.
	l.SetSandboxAvailable(false)
	available := l.AvailableSkills()
	if len(available) != 1 {
		t.Fatalf("expected 1 available skill without sandbox, got %d", len(available))
	}
	if available[0].Name != "summarize" {
		t.Errorf("expected 'summarize' skill, got %q", available[0].Name)
	}

	// Prompt should only contain the MCP skill.
	section := l.BuildPromptSection()
	if strings.Contains(section, "deploy") {
		t.Error("prompt should not contain script skill 'deploy' when sandbox is unavailable")
	}
	if !strings.Contains(section, "summarize") {
		t.Error("prompt should contain MCP-only skill 'summarize' when sandbox is unavailable")
	}

	// Re-enable sandbox: all skills visible again.
	l.SetSandboxAvailable(true)
	if len(l.AvailableSkills()) != 2 {
		t.Error("expected all skills available after re-enabling sandbox")
	}
}

func TestSetSandboxAvailable_NoSkillsWhenAllRequireSandbox(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	scriptDir := filepath.Join(skillsDir, "deploy")
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "SKILL.md"), []byte("# Deploy\nDeploy the app."), 0644); err != nil {
		t.Fatal(err)
	}

	l, err := NewLoader(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	l.SetSandboxAvailable(false)
	if len(l.AvailableSkills()) != 0 {
		t.Error("expected 0 available skills when only script skills exist and sandbox is unavailable")
	}
	if section := l.BuildPromptSection(); section != "" {
		t.Errorf("expected empty prompt section, got %q", section)
	}
	if section := l.BuildSlimPromptSection(); section != "" {
		t.Errorf("expected empty slim prompt section, got %q", section)
	}
}

