package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var skillDir string

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage Open-Nipper skills (plugins)",
	Long:  "Create and manage skills that extend the agent with custom capabilities.",
}

var skillBootstrapCmd = &cobra.Command{
	Use:   "bootstrap [name]",
	Short: "Create a skill skeleton",
	Long:  "Create a new skill directory with SKILL.md, config.yaml, and scripts/run.sh. Name must be a single word (e.g. my-skill).",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillBootstrap,
}

func init() {
	skillBootstrapCmd.Flags().StringVarP(&skillDir, "dir", "d", ".", "destination directory (skill will be created as <dir>/<name>)")
	skillCmd.AddCommand(skillBootstrapCmd)
	Root.AddCommand(skillCmd)
}

func runSkillBootstrap(cmd *cobra.Command, args []string) error {
	name := strings.TrimSpace(args[0])
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if strings.Contains(name, "/") || strings.Contains(name, " ") {
		return fmt.Errorf("skill name must be a single word (use hyphens, e.g. my-skill)")
	}

	destDir, err := filepath.Abs(skillDir)
	if err != nil {
		return fmt.Errorf("resolving destination directory: %w", err)
	}
	skillPath := filepath.Join(destDir, name)

	if err := os.MkdirAll(filepath.Join(skillPath, "scripts"), 0755); err != nil {
		return fmt.Errorf("creating skill directory: %w", err)
	}

	files := []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{
			filepath.Join(skillPath, "SKILL.md"),
			skillMarkdownContent(name),
			0644,
		},
		{
			filepath.Join(skillPath, "config.yaml"),
			skillConfigYAMLContent(name),
			0644,
		},
		{
			filepath.Join(skillPath, "scripts", "run.sh"),
			skillRunScriptContent(name),
			0755,
		},
	}

	for _, f := range files {
		if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.path, err)
		}
	}

	absPath, _ := filepath.Abs(skillPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Skill skeleton created at %s\n", absPath)
	fmt.Fprintf(cmd.OutOrStdout(), "  - SKILL.md\n  - config.yaml\n  - scripts/run.sh\n")
	return nil
}

func skillMarkdownContent(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	title := strings.Join(parts, " ")
	return fmt.Sprintf(`# %s

Describe what this skill does and when the agent should use it.

## When to Use

Use this skill when the user asks to:
- (add trigger phrases)

## Parameters

- (describe parameters the script accepts)

## Usage

The agent can run this skill via:
- **skill_exec** tool: name="%s", args="..."
- **bash** tool: /skills/%s/scripts/run.sh ...

## Notes

- (optional notes)
`, title, name, name)
}

func skillConfigYAMLContent(name string) string {
	return `name: "` + name + `"
version: "0.1.0"
description: ""

runtime: "bash"
entrypoint: "scripts/run.sh"
timeout: 120

# secrets:
#   - name: "example"
#     env_var: "MY_SECRET"
#     provider: "env"
#     ref: "MY_SECRET"

network: false
require_confirmation: false
# channels: ["slack", "whatsapp"]

dependencies:
  system: []
`
}

func skillRunScriptContent(name string) string {
	return `#!/usr/bin/env bash
# Skill: ` + name + `
# Runs inside the agent sandbox with NIPPER_PLUGIN_NAME, NIPPER_PLUGIN_DIR, etc. set.

set -euo pipefail

echo "Skill ` + name + ` executed."
echo "Args: ${*:-none}"
# Add your logic here.
exit 0
`
}
