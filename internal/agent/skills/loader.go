package skills

import (
	"os"
	"path/filepath"
	"sort"

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
	skills []Skill
	logger *zap.Logger
}

// NewLoader scans for skill subdirectories (each with SKILL.md). The path argument may be:
//   - The agent base path (e.g. ~/.open-nipper): scans basePath/skills/
//   - The skills directory itself (e.g. ~/.open-nipper/skills): scans that path directly
// If the path is empty or the skills dir does not exist, an empty loader is returned without error.
func NewLoader(basePath string, logger *zap.Logger) (*Loader, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	l := &Loader{logger: logger}
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
	if len(l.skills) == 0 {
		return ""
	}
	const header = "\n\n## Available Skills\n\n" +
		"CRITICAL: Skills are NOT separate tools. Each skill name (e.g. yt_summary) is a parameter, not a tool. " +
		"You MUST use the skill_exec tool: pass the skill name as the 'name' parameter and arguments as 'args'. " +
		"Never call a tool by the skill name — e.g. there is no tool named yt_summary; call skill_exec with name=\"yt_summary\" instead.\n\n" +
		"MCP-only skills (e.g. summarize_url, plant-care): there is NO tool with the skill name. Do NOT invoke summarize_url or any other MCP-only skill name as a tool. Follow that skill's steps using web_fetch and/or the MCP tools listed in the skill (e.g. list_folders, create_note, list_tags).\n\n" +
		"After skill_exec returns a result, do NOT call skill_exec again with the same arguments. Use the data from that first result. " +
		"For Joplin notes: verify the folder exists (list_folders), record its ID, then call create_note with that parent_id and tag_names. If the folder does not exist, create it with create_folder first. You MUST call create_note.\n\n" +
		"For script-based skills use skill_exec (or bash with the script path). " +
		"For MCP-only skills (tagged below) do NOT use skill_exec — use the MCP tools described in the skill directly.\n\n"
	var b string
	for i := range l.skills {
		s := &l.skills[i]
		desc := s.Description
		if s.IsMCPOnly() {
			desc += "\n\n(MCP-only skill: use the MCP tools listed in your tool set as described above; do not call skill_exec for this skill.)"
		}
		b += "<skill name=\"" + s.Name + "\">\n" + desc + "\n</skill>\n\n"
	}
	return header + b
}
