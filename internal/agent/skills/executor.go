package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/sandbox"
)

const (
	skillsMountPoint        = "/skills"
	secretsMountPoint       = "/tmp/secrets"
	largeSecretThreshold    = 4096
	executorDefaultTimeout  = 120
)

// Executor runs skill entrypoints inside the Docker sandbox with resolved secrets.
type Executor struct {
	sandbox   *sandbox.Manager
	providers *ProviderRegistry
	logger    *zap.Logger
	metrics   MetricsRecorder // optional; records execution count, duration, secrets resolved
	// Optional runtime context for standard env vars (set by CLI when building tools).
	UserID    string
	SessionID string
	Workspace string
}

// NewExecutor creates a skill executor. metrics may be nil to disable metric recording.
func NewExecutor(sandbox *sandbox.Manager, providers *ProviderRegistry, logger *zap.Logger, metrics MetricsRecorder) *Executor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Executor{
		sandbox:   sandbox,
		providers: providers,
		logger:    logger,
		metrics:   metrics,
	}
}

// SandboxAvailable returns true if the executor has a sandbox manager configured.
func (e *Executor) SandboxAvailable() bool {
	return e.sandbox != nil
}

// Execute runs a skill's entrypoint inside the Docker sandbox with resolved secrets.
// Standard env vars (NIPPER_PLUGIN_NAME, NIPPER_PLUGIN_DIR, etc.) are always set.
// For MCP-only skills (type: mcp), no script is run; a guidance message is returned so the model uses MCP tools.
func (e *Executor) Execute(ctx context.Context, skill *Skill, args string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	start := time.Now()
	if skill == nil {
		return "", "", -1, fmt.Errorf("skill is nil")
	}

	// MCP-only skill: no script to run; tell the model to follow the skill steps using available tools.
	if skill.IsMCPOnly() {
		e.logger.Debug("skill is MCP-only, skipping script execution",
			zap.String("skill", skill.Name),
		)
		msg := fmt.Sprintf("Skill %q is workflow-only (no script). Do not call skill_exec again. Follow the steps for this skill in the \"Available Skills\" section using your available tools (e.g. web_fetch, create_note, list_folders, list_tags).", skill.Name)
		if e.metrics != nil {
			e.metrics.RecordSkillExecution(skill.Name, 0)
			e.metrics.RecordSkillExecutionDuration(skill.Name, time.Since(start).Seconds())
		}
		return msg, "", 0, nil
	}

	// Script skills require a sandbox to execute.
	if e.sandbox == nil {
		return "", "", -1, fmt.Errorf("skill %q requires a sandbox to execute but sandbox is not available; enable sandbox in config or use MCP-only skills", skill.Name)
	}

	if timeout <= 0 {
		timeout = time.Duration(executorDefaultTimeout) * time.Second
	}
	// If the skill declares a longer timeout than the default, use it.
	// The skill author knows how long their script needs (e.g. network scans).
	if skill.Config != nil && skill.Config.Timeout > 0 {
		t := time.Duration(skill.Config.Timeout) * time.Second
		if t > timeout {
			timeout = t
		}
	}

	workDir := e.Workspace
	if workDir == "" {
		workDir = e.sandbox.WorkDir()
	}

	env := make(map[string]string)

	// Standard env vars.
	env["NIPPER_PLUGIN_NAME"] = skill.Name
	env["NIPPER_PLUGIN_DIR"] = filepath.Join(skillsMountPoint, skill.Name)
	if e.UserID != "" {
		env["NIPPER_USER_ID"] = e.UserID
	}
	if e.SessionID != "" {
		env["NIPPER_SESSION_ID"] = e.SessionID
	}
	if e.Workspace != "" {
		env["NIPPER_WORKSPACE"] = e.Workspace
	}

	// Resolve secrets.
	var refs []SkillSecretRef
	if skill.Config != nil {
		refs = skill.Config.Secrets
	}
	resolved, err := e.providers.Resolve(refs)
	if err != nil {
		return "", "", -1, fmt.Errorf("resolving secrets: %w", err)
	}

	// Emit observer events and metrics for each resolved secret (never the value).
	observer := ObserverFromContext(ctx)
	for _, ref := range refs {
		if _, ok := resolved[ref.EnvVar]; ok {
			provider := ref.Provider
			if provider == "" {
				provider = "env"
			}
			if e.metrics != nil {
				e.metrics.RecordSkillSecretsResolved(provider)
			}
			if observer != nil {
				observer.RecordSecretResolved(ctx, skill.Name, ref.Name, provider)
			}
		}
	}

	stagingDir := e.sandbox.StagingDir()
	var stagedFiles []string

	for k, v := range resolved {
		if isLargeSecret(v) && stagingDir != "" {
			// Write to staging dir and pass path as env var.
			safeName := strings.ReplaceAll(k, "/", "_")
			fpath := filepath.Join(stagingDir, safeName)
			if writeErr := os.WriteFile(fpath, []byte(v), 0600); writeErr != nil {
				e.logger.Warn("failed to stage large secret, passing as env",
					zap.String("envVar", k),
					zap.Error(writeErr),
				)
				env[k] = v
				continue
			}
			stagedFiles = append(stagedFiles, fpath)
			env[k+"_FILE"] = filepath.Join(secretsMountPoint, safeName)
		} else {
			env[k] = v
		}
	}

	// If args is JSON (e.g. {"yt_url": "https://..."}), parse and set env vars + positional args for the script.
	argsEnv, positionalArgs := parseSkillArgs(args)
	for k, v := range argsEnv {
		env[k] = v
	}
	if positionalArgs != "" {
		args = positionalArgs
	}

	// Build command: /skills/{name}/{entrypoint} {args}
	entrypoint := "scripts/run.sh"
	if skill.Config != nil && skill.Config.Entrypoint != "" {
		entrypoint = skill.Config.Entrypoint
	}
	cmdPath := filepath.Join(skillsMountPoint, skill.Name, entrypoint)
	command := strings.TrimSpace(cmdPath + " " + args)

	e.logger.Debug("executing skill",
		zap.String("skill", skill.Name),
		zap.String("command", command),
		zap.Duration("timeout", timeout),
	)

	stdout, stderr, exitCode, execErr := e.sandbox.ExecWithEnv(ctx, command, workDir, timeout, env)

	// Clean up staged files.
	for _, f := range stagedFiles {
		_ = os.Remove(f)
	}

	durationSec := time.Since(start).Seconds()
	if e.metrics != nil {
		e.metrics.RecordSkillExecution(skill.Name, exitCode)
		e.metrics.RecordSkillExecutionDuration(skill.Name, durationSec)
	}

	if execErr != nil {
		return stdout, stderr, exitCode, execErr
	}
	return stdout, stderr, exitCode, nil
}

func isLargeSecret(v string) bool {
	if len(v) > largeSecretThreshold {
		return true
	}
	return strings.Contains(v, "\n")
}

// parseSkillArgs parses args when it is JSON (e.g. {"yt_url": "https://..."}).
// Returns (envVars, positional): env vars from keys (e.g. YT_URL) and a shell-safe positional string
// so the script receives values as $1, $2, .... If args is not JSON, returns (nil, "") and the
// caller keeps the original args string.
func parseSkillArgs(args string) (envVars map[string]string, positional string) {
	args = strings.TrimSpace(args)
	if args == "" || !strings.HasPrefix(args, "{") {
		return nil, ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return nil, ""
	}
	envVars = make(map[string]string)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var pos []string
	for _, k := range keys {
		val := m[k]
		var s string
		switch v := val.(type) {
		case string:
			s = v
		case float64:
			s = fmt.Sprintf("%g", v)
		case bool:
			s = fmt.Sprintf("%t", v)
		default:
			continue
		}
		envKey := toEnvKey(k)
		envVars[envKey] = s
		pos = append(pos, shellQuote(s))
	}
	return envVars, strings.Join(pos, " ")
}

func toEnvKey(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '-' || r == ' ' {
			b.WriteRune('_')
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

func shellQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
