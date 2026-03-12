package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/sandbox"
	"github.com/jescarri/open-nipper/internal/agent/skills"
	"github.com/jescarri/open-nipper/internal/config"
)

const (
	bashMaxOutputBytes  = 50 * 1024 // 50 KB per stream
	bashDefaultTimeout  = 120
	localExecShell      = "/bin/bash"
)

// BashParams defines the input for the bash tool.
type BashParams struct {
	Command string `json:"command" jsonschema:"description=Bash command to execute,required"`
	WorkDir string `json:"work_dir,omitempty" jsonschema:"description=Working directory (default: /workspace)"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds (default 120, max 300)"`
}

// BashResult is the output of the bash tool.
type BashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// BashExecutor encapsulates bash execution with optional sandbox support.
// When SkillsLoader and SkillExecutor are set, commands targeting /skills/{name}/... run via the executor with secret injection.
type BashExecutor struct {
	sandboxMgr    *sandbox.Manager
	cfg           config.SandboxConfig
	logger        *zap.Logger
	skillsLoader  *skills.Loader
	skillExecutor *skills.Executor
}

// NewBashExecutor creates a bash executor. If sandboxMgr is nil, commands run locally.
func NewBashExecutor(sandboxMgr *sandbox.Manager, cfg config.SandboxConfig, logger *zap.Logger) *BashExecutor {
	return &BashExecutor{
		sandboxMgr: sandboxMgr,
		cfg:        cfg,
		logger:     logger,
	}
}

// SetSkillExecution sets the skills loader and executor for /skills/... pre-exec hook. Optional.
func (e *BashExecutor) SetSkillExecution(loader *skills.Loader, executor *skills.Executor) {
	e.skillsLoader = loader
	e.skillExecutor = executor
}

// ExecBash validates and executes a bash command. Exported for testing.
func (e *BashExecutor) ExecBash(ctx context.Context, params BashParams) (*BashResult, error) {
	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	if err := validateCommand(params.Command); err != nil {
		return &BashResult{
			Stderr:   fmt.Sprintf("BLOCKED: %s", err.Error()),
			ExitCode: 126,
		}, nil
	}

	timeout := time.Duration(params.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(bashDefaultTimeout) * time.Second
	}
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}

	e.logger.Debug("executing bash command",
		zap.String("command", truncateForLog(params.Command, 200)),
		zap.Bool("sandboxed", e.sandboxMgr != nil),
		zap.Duration("timeout", timeout),
	)

	if e.sandboxMgr != nil {
		return e.execSandboxed(ctx, params, timeout)
	}
	return e.execLocal(ctx, params, timeout)
}

func (e *BashExecutor) execSandboxed(ctx context.Context, params BashParams, timeout time.Duration) (*BashResult, error) {
	// If command targets a skill path and we have loader + executor, run via skill executor (secret injection).
	if e.skillsLoader != nil && e.skillExecutor != nil {
		if skillName, args := ParseSkillCommand(params.Command); skillName != "" {
			if skill, ok := e.skillsLoader.SkillByName(skillName); ok {
				stdout, stderr, exitCode, err := e.skillExecutor.Execute(ctx, skill, args, timeout)
				if err != nil {
					return nil, fmt.Errorf("skill exec: %w", err)
				}
				return &BashResult{
					Stdout:   stdout,
					Stderr:   stderr,
					ExitCode: exitCode,
				}, nil
			}
		}
	}

	workDir := params.WorkDir
	if workDir == "" {
		workDir = e.cfg.WorkDir
	}

	stdout, stderr, exitCode, err := e.sandboxMgr.Exec(ctx, params.Command, workDir, timeout)
	if err != nil {
		return nil, fmt.Errorf("sandbox exec: %w", err)
	}

	return &BashResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}, nil
}

// ParseSkillCommand extracts skill name and trailing args from a command like "/skills/deploy/scripts/run.sh --env=staging".
// Returns ("", "") if the command does not target /skills/{name}/. Exported for tests.
func ParseSkillCommand(command string) (skillName, args string) {
	trimmed := strings.TrimSpace(command)
	const prefix = "/skills/"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", ""
	}
	rest := trimmed[len(prefix):]
	idx := strings.IndexAny(rest, "/ \t")
	if idx < 0 {
		return rest, ""
	}
	if rest[idx] == '/' {
		// rest is "name/scripts/run.sh ..."; skill name is the first segment
		skillName = rest[:idx]
		after := rest[idx+1:]
		argsStart := strings.IndexAny(after, " \t")
		if argsStart < 0 {
			return skillName, ""
		}
		return skillName, strings.TrimSpace(after[argsStart:])
	}
	// space: rest is "name args"
	skillName = rest[:idx]
	return skillName, strings.TrimSpace(rest[idx:])
}

func (e *BashExecutor) execLocal(ctx context.Context, params BashParams, timeout time.Duration) (*BashResult, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, localExecShell, "-c", params.Command)
	if params.WorkDir != "" {
		cmd.Dir = params.WorkDir
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("exec failed: %w", runErr)
		}
	}

	stdout := truncateBashOutput(stdoutBuf.String())
	stderr := truncateBashOutput(stderrBuf.String())

	return &BashResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}, nil
}

// --- Command validation / security blocklist ---

// blockedPatterns defines regex patterns for commands that are never allowed.
var blockedPatterns = []*regexp.Regexp{
	// Destructive filesystem operations targeting system root directories
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*\s+/(etc|usr|var|boot|sys|proc|dev|bin|sbin|lib|lib64|opt|root|home)\b`),
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*\s+~\s`),
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*\s+\$HOME\b`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bformat\s+[A-Za-z]:`),

	// Direct disk writes
	regexp.MustCompile(`\bdd\b.*\bof\s*=\s*/dev/`),

	// Fork bombs and resource exhaustion
	regexp.MustCompile(`:\(\)\s*\{`),
	regexp.MustCompile(`\bfork\s*bomb\b`),

	// System control
	regexp.MustCompile(`\bshutdown\b`),
	regexp.MustCompile(`\breboot\b`),
	regexp.MustCompile(`\bhalt\b`),
	regexp.MustCompile(`\bpoweroff\b`),
	regexp.MustCompile(`\binit\s+[06]\b`),
	regexp.MustCompile(`\bsystemctl\s+(halt|poweroff|reboot|rescue|emergency)\b`),

	// Kernel module manipulation
	regexp.MustCompile(`\binsmod\b`),
	regexp.MustCompile(`\brmmod\b`),
	regexp.MustCompile(`\bmodprobe\b`),

	// Direct device access
	regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|vd|xvd|loop)`),
	regexp.MustCompile(`\bmount\b.*\b/\s*$`),
	regexp.MustCompile(`\bumount\s+/\s*$`),

	// Network redirection attacks (reverse shells)
	regexp.MustCompile(`/dev/tcp/`),
	regexp.MustCompile(`\bnc\s+.*-e\b`),
	regexp.MustCompile(`\bncat\s+.*-e\b`),

	// Credential theft / escalation
	regexp.MustCompile(`\bpasswd\s+root\b`),
	regexp.MustCompile(`\busermod\b.*-[a-zA-Z]*G.*\b(sudo|wheel|root)\b`),
	regexp.MustCompile(`\bchmod\s+[u+]*s\b`),
	regexp.MustCompile(`\bchmod\s+[246][0-7]{3}\b`),

	// Container escape attempts
	regexp.MustCompile(`\bnsenter\b`),
	regexp.MustCompile(`/proc/1/`),
	regexp.MustCompile(`\bdocker\s+run\b`),
	regexp.MustCompile(`\bdocker\s+exec\b`),

	// iptables / firewall manipulation
	regexp.MustCompile(`\biptables\b`),
	regexp.MustCompile(`\bnft\b`),

	// Crontab manipulation
	regexp.MustCompile(`\bcrontab\s+-r\b`),

	// Overwriting critical system files
	regexp.MustCompile(`>\s*/etc/(passwd|shadow|sudoers|hosts)`),
	regexp.MustCompile(`\btee\s+/etc/(passwd|shadow|sudoers)`),

	// curl/wget piped to shell (can be used for supply chain attacks)
	regexp.MustCompile(`\bcurl\b.*\|\s*(ba)?sh\b`),
	regexp.MustCompile(`\bwget\b.*\|\s*(ba)?sh\b`),
	regexp.MustCompile(`\bcurl\b.*\|\s*sudo\b`),
	regexp.MustCompile(`\bwget\b.*\|\s*sudo\b`),
}

// blockedExactPatterns are patterns matched against the normalized command.
// These catch common destructive one-liners precisely (anchored to avoid false positives).
var blockedExactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^rm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+/\s*$`),
	regexp.MustCompile(`^rm\s+-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*\s+/\s*$`),
	regexp.MustCompile(`^rm\s+-[a-zA-Z]*r[a-zA-Z]*\s+/\s*$`),
	regexp.MustCompile(`^rm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+/\*`),
	regexp.MustCompile(`^rm\s+-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*\s+/\*`),
	regexp.MustCompile(`>\s*/dev/sd[a-z]`),
	regexp.MustCompile(`^chmod\s+-R\s+777\s+/\s*$`),
}

func validateCommand(command string) error {
	if len(command) > 10000 {
		return fmt.Errorf("command too long (max 10000 characters)")
	}

	normalized := strings.TrimSpace(command)

	for _, pattern := range blockedExactPatterns {
		if pattern.MatchString(normalized) {
			return fmt.Errorf("command matches blocked pattern: destructive system operation")
		}
	}

	for _, pattern := range blockedPatterns {
		if pattern.MatchString(command) {
			return fmt.Errorf("command matches blocked pattern: potentially destructive operation")
		}
	}

	return nil
}

// ValidateCommand is exported for testing.
func ValidateCommand(command string) error {
	return validateCommand(command)
}

func truncateBashOutput(s string) string {
	if len(s) <= bashMaxOutputBytes {
		return s
	}
	return s[:bashMaxOutputBytes] + "\n... [output truncated at 50KB]"
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
