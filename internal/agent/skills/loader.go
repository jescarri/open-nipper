package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	skillMarkdownFile = "SKILL.md"
	skillConfigFile   = "config.yaml"
	skillsSubdir      = "skills"
)

// Loader scans a skills directory and parses SKILL.md and optional config.yaml per skill.
type Loader struct {
	skills           []Skill
	logger           *zap.Logger
	sandboxAvailable bool // when false, script-type skills are excluded from prompts
}

// NewLoader scans for skill subdirectories (each with SKILL.md). The path argument may be:
//   - The agent base path (e.g. ~/.open-nipper): scans basePath/skills/
//   - The skills directory itself (e.g. ~/.open-nipper/skills): scans that path directly
// If the path is empty or the skills dir does not exist, an empty loader is returned without error.
func NewLoader(basePath string, logger *zap.Logger) (*Loader, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	l := &Loader{logger: logger, sandboxAvailable: true}
	var skillsDir string
	if basePath == "" {
		skillsDir = ""
	} else if filepath.Base(basePath) == skillsSubdir {
		// Caller passed the skills directory directly (e.g. from config agent.skills.path).
		skillsDir = basePath
	} else {
		skillsDir = filepath.Join(basePath, skillsSubdir)
	}
	if skillsDir == "" {
		return l, nil
	}
	info, err := os.Stat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			l.logger.Debug("skills directory does not exist", zap.String("path", skillsDir))
			return l, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		l.logger.Warn("skills path is not a directory, skipping", zap.String("path", skillsDir))
		return l, nil
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip hidden dirs (e.g. .git) to avoid "skipping directory without SKILL.md" warnings.
		if len(name) > 0 && name[0] == '.' {
			continue
		}
		skillPath := filepath.Join(skillsDir, name)
		skillMarkdown := filepath.Join(skillPath, skillMarkdownFile)
		if _, err := os.Stat(skillMarkdown); err != nil {
			if os.IsNotExist(err) {
				l.logger.Warn("skipping directory without SKILL.md", zap.String("dir", name), zap.String("path", skillPath))
				continue
			}
			l.logger.Warn("skipping skill directory", zap.String("dir", name), zap.Error(err))
			continue
		}
		desc, err := os.ReadFile(skillMarkdown)
		if err != nil {
			l.logger.Warn("failed to read SKILL.md", zap.String("skill", name), zap.Error(err))
			continue
		}
		absPath, err := filepath.Abs(skillPath)
		if err != nil {
			absPath = skillPath
		}
		skill := Skill{
			Name:        name,
			Path:        absPath,
			Description: string(desc),
		}
		configPath := filepath.Join(skillPath, skillConfigFile)
		if data, err := os.ReadFile(configPath); err == nil {
			var cfg SkillConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				l.logger.Warn("malformed config.yaml, using description-only skill", zap.String("skill", name), zap.Error(err))
			} else {
				skill.Config = &cfg
			}
		}
		l.skills = append(l.skills, skill)
	}
	sort.Slice(l.skills, func(i, j int) bool { return l.skills[i].Name < l.skills[j].Name })
	l.logger.Info("skills loaded",
		zap.Int("count", len(l.skills)),
		zap.Strings("names", l.skillNames()),
	)
	return l, nil
}

// SetSandboxAvailable marks whether a Docker sandbox is available. When false,
// script-type skills are excluded from prompt sections (only MCP-only skills are shown).
func (l *Loader) SetSandboxAvailable(available bool) {
	l.sandboxAvailable = available
}

// availableSkills returns skills that can actually execute given the current sandbox availability.
// When sandbox is unavailable, only MCP-only skills are returned.
func (l *Loader) availableSkills() []Skill {
	if l.sandboxAvailable {
		return l.skills
	}
	var result []Skill
	for i := range l.skills {
		if !l.skills[i].RequiresSandbox() {
			result = append(result, l.skills[i])
		}
	}
	return result
}

func (l *Loader) skillNames() []string {
	names := make([]string, len(l.skills))
	for i := range l.skills {
		names[i] = l.skills[i].Name
	}
	return names
}

// Skills returns all loaded skills (read-only), sorted by name.
func (l *Loader) Skills() []Skill {
	return l.skills
}

// AvailableSkills returns skills that can actually execute given the current
// sandbox availability. When sandbox is unavailable, only MCP-only skills are returned.
func (l *Loader) AvailableSkills() []Skill {
	return l.availableSkills()
}

// SkillByName returns the skill with the given name and true, or nil and false.
func (l *Loader) SkillByName(name string) (*Skill, bool) {
	for i := range l.skills {
		if l.skills[i].Name == name {
			return &l.skills[i], true
		}
	}
	return nil, false
}

// BuildPromptSection returns the "Available Skills" section for the system prompt,
// with each skill wrapped in <skill name="...">...</skill>. Empty string if no skills.
// MCP-only skills are tagged so the model uses MCP tools directly instead of skill_exec.
func (l *Loader) BuildPromptSection() string {
	return l.BuildPromptSectionForSkills(nil)
}

// BuildSlimPromptSection returns a compact skills index with only name + 1-line
// description for each skill. Use this when no skill-specific workflow is needed,
// saving significant context tokens on local LLMs with limited context windows.
// Skills that require a sandbox are excluded when sandbox is not available.
func (l *Loader) BuildSlimPromptSection() string {
	available := l.availableSkills()
	if len(available) == 0 {
		return ""
	}
	var b string
	b = "\n\nAvailable skills (use skill_exec tool with skill name as 'name' param; MCP-only skills use MCP tools directly):\n"
	for i := range available {
		s := &available[i]
		desc := s.oneLineDesc()
		tag := ""
		if s.IsMCPOnly() {
			tag = " [MCP-only]"
		}
		b += "- " + s.Name + tag + ": " + desc + "\n"
	}
	return b
}

// BuildPromptSectionForSkills returns the full prompt section but only includes
// full descriptions for the named skills. Other skills get a 1-line summary.
// If activeSkills is nil or empty, all skills get full descriptions (legacy behavior).
// Skills that require a sandbox are excluded when sandbox is not available.
func (l *Loader) BuildPromptSectionForSkills(activeSkills []string) string {
	available := l.availableSkills()
	if len(available) == 0 {
		return ""
	}

	// If no active skills specified, include all (legacy behavior).
	includeAll := len(activeSkills) == 0
	activeSet := make(map[string]bool, len(activeSkills))
	for _, name := range activeSkills {
		activeSet[name] = true
	}

	const header = "\n\n## Available Skills\n\n" +
		"Skills are NOT tools. Use skill_exec with the skill name as 'name' param. " +
		"MCP-only skills: use MCP tools directly (not skill_exec).\n\n"

	var fullSkills string
	var slimSkills string

	for i := range available {
		s := &available[i]
		if includeAll || activeSet[s.Name] {
			desc := s.Description
			if s.IsMCPOnly() {
				desc += "\n\n(MCP-only: use MCP tools directly, not skill_exec.)"
			}
			fullSkills += "<skill name=\"" + s.Name + "\">\n" + desc + "\n</skill>\n\n"
		} else {
			tag := ""
			if s.IsMCPOnly() {
				tag = " [MCP-only]"
			}
			slimSkills += "- " + s.Name + tag + ": " + s.oneLineDesc() + "\n"
		}
	}

	result := header
	if fullSkills != "" {
		result += fullSkills
	}
	if slimSkills != "" {
		result += "Other skills (send a message mentioning the skill to activate):\n" + slimSkills
	}
	return result
}

// oneLineDesc extracts the first meaningful line from the SKILL.md description.
func (s *Skill) oneLineDesc() string {
	if s.Config != nil && s.Config.Description != "" {
		return s.Config.Description
	}
	// Fall back to first non-header, non-empty line from SKILL.md.
	for _, line := range strings.Split(s.Description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 120 {
			return line[:117] + "..."
		}
		return line
	}
	return s.Name
}
