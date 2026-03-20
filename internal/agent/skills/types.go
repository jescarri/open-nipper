package skills

// Skill represents a loaded skill (plugin) from the skills directory.
type Skill struct {
	Name        string       // directory name
	Path        string       // absolute path on host
	Description string       // contents of SKILL.md
	Config      *SkillConfig // parsed config.yaml (nil if absent)
}

// SkillType is the execution model for a skill.
const (
	SkillTypeScript = "script" // default: run entrypoint in sandbox
	SkillTypeMCP    = "mcp"    // description-only: use MCP tools from the prompt, no script
)

// SkillConfig holds metadata from a skill's config.yaml.
type SkillConfig struct {
	Name        string           `yaml:"name"`
	Version     string           `yaml:"version"`
	Description string           `yaml:"description"`
	PromptHint  string           `yaml:"prompt_hint"` // compact LLM-facing summary; used instead of full SKILL.md when present
	Type        string           `yaml:"type"`        // "script" (default) | "mcp" — mcp = no script, use MCP tools only
	Runtime     string           `yaml:"runtime"`     // "bash" | "node" | "python"
	Entrypoint  string           `yaml:"entrypoint"`  // e.g. "scripts/run.sh"
	Timeout     int              `yaml:"timeout"`     // seconds
	Secrets     []SkillSecretRef `yaml:"secrets"`     // secret references
	Network     bool             `yaml:"network"`     // needs network access
	Confirm     bool             `yaml:"require_confirmation"`
	Channels    []string         `yaml:"channels"`    // allowed channel types
	Deps        SkillDeps        `yaml:"dependencies"`
}

// SkillSecretRef references a secret to be resolved by a provider.
type SkillSecretRef struct {
	Name     string `yaml:"name"`     // logical name (e.g. "deploy_ssh_key")
	EnvVar   string `yaml:"env_var"`  // env var name inside container
	Provider string `yaml:"provider"` // "env" (default) | "vault" | "op" (future)
	Ref      string `yaml:"ref"`      // provider-specific ref (env var name on host, vault path, etc.)
}

// SkillDeps lists required system dependencies.
type SkillDeps struct {
	System []string `yaml:"system"` // required binaries
}

// promptDesc returns the text to inject for this skill in the system prompt.
// If config.yaml provides a compact prompt_hint, that is used instead of the
// full SKILL.md body, significantly reducing prompt size for large skills.
func (s *Skill) promptDesc() string {
	if s.Config != nil && s.Config.PromptHint != "" {
		return s.Config.PromptHint
	}
	return s.Description
}

// IsMCPOnly returns true if this skill has no runnable script and should be used via MCP tools only.
func (s *Skill) IsMCPOnly() bool {
	if s.Config == nil {
		return false // no config => treat as script (legacy; may have scripts/run.sh)
	}
	return s.Config.Type == SkillTypeMCP
}

// RequiresSandbox returns true if this skill needs a Docker sandbox to execute.
// MCP-only skills do not require a sandbox; all other skills (script type) do.
func (s *Skill) RequiresSandbox() bool {
	return !s.IsMCPOnly()
}
