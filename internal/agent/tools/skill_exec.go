package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/agent/skills"
)

// skillExecMaxStdoutForLLM is the max bytes of skill stdout passed to the LLM.
// We keep the last N bytes so the end of the script output is visible when the sandbox truncates at 50KB from the start.
const skillExecMaxStdoutForLLM = 8192

// SkillExecParams is the input for the skill_exec tool.
type SkillExecParams struct {
	Name    string `json:"name"    jsonschema:"description=Skill name (directory name under skills/),required"`
	Args    string `json:"args"    jsonschema:"description=Arguments for the skill: a string the skill expects (e.g. a URL, JSON like {\"yt_url\": \"https://...\"}, or flags like --env=staging). Format depends on the skill; see each skill's Usage in Available Skills."`
	Timeout int    `json:"timeout" jsonschema:"description=Timeout in seconds (0 = use skill or default 120)"`
}

// SkillExecResult is the output of the skill_exec tool.
type SkillExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// SkillExecExecutor runs a skill by name via the skill executor.
type SkillExecExecutor struct {
	loader   *skills.Loader
	executor *skills.Executor
	logger   *zap.Logger
}

// NewSkillExecExecutor creates an executor for the skill_exec tool.
func NewSkillExecExecutor(loader *skills.Loader, executor *skills.Executor, logger *zap.Logger) *SkillExecExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SkillExecExecutor{
		loader:   loader,
		executor: executor,
		logger:   logger,
	}
}

// ExecSkillExec runs the named skill with the given args and returns stdout, stderr, and exit code.
func (e *SkillExecExecutor) ExecSkillExec(ctx context.Context, params SkillExecParams) (*SkillExecResult, error) {
	if params.Name == "" {
		return &SkillExecResult{ExitCode: -1}, fmt.Errorf("skill name is required")
	}

	skill, ok := e.loader.SkillByName(params.Name)
	if !ok {
		return &SkillExecResult{ExitCode: -1}, fmt.Errorf("skill %q not found", params.Name)
	}

	timeout := time.Duration(params.Timeout) * time.Second
	stdout, stderr, exitCode, err := e.executor.Execute(ctx, skill, params.Args, timeout)
	if err != nil {
		return &SkillExecResult{Stdout: stdout, Stderr: stderr, ExitCode: exitCode}, fmt.Errorf("skill execution: %w", err)
	}

	// For successful runs, keep only the last 8KB of stdout for the LLM so the end of the output
	// is visible when the sandbox truncates the full stream at 50KB from the start.
	if exitCode == 0 && len(stdout) > skillExecMaxStdoutForLLM {
		stdout = stdout[len(stdout)-skillExecMaxStdoutForLLM:]
	}

	return &SkillExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
	}, nil
}

// BuildSkillExecTool builds the skill_exec EINO tool. Requires loader and executor to be non-nil.
func BuildSkillExecTool(loader *skills.Loader, executor *skills.Executor, logger *zap.Logger) (tool.BaseTool, error) {
	if loader == nil || executor == nil {
		return nil, fmt.Errorf("skills loader and executor are required for skill_exec tool")
	}
	exec := NewSkillExecExecutor(loader, executor, logger)
	t, err := toolutils.InferTool(
		"skill_exec",
		"Run a skill (plugin) by name with optional arguments. Use this tool when the user's request matches an available skill. "+
			"Provide the skill name (as listed in Available Skills) and args as required by that skill (e.g. a URL string, JSON like {\"yt_url\": \"https://...\"}, or flags like --env=staging).",
		exec.ExecSkillExec,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}
