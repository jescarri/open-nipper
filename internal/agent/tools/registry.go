package tools

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/memory"
	"github.com/jescarri/open-nipper/internal/agent/registration"
	"github.com/jescarri/open-nipper/internal/agent/sandbox"
	"github.com/jescarri/open-nipper/internal/agent/skills"
	"github.com/jescarri/open-nipper/internal/config"
)

// BuildToolsOptions holds optional dependencies for tool construction.
type BuildToolsOptions struct {
	SandboxMgr     *sandbox.Manager
	MemoryStore   *memory.Store
	Logger        *zap.Logger
	SkillsLoader  *skills.Loader
	SkillExecutor *skills.Executor
}

// BuildTools constructs the list of enabled tools for the agent based on config and policies.
func BuildTools(ctx context.Context, cfg *config.AgentRuntimeConfig, policies *registration.ToolsPolicy, opts *BuildToolsOptions) ([]tool.BaseTool, error) {
	var tools []tool.BaseTool

	if cfg.Tools.WebFetch && isAllowed("web_fetch", policies) {
		t, err := buildWebFetchTool(ctx)
		if err != nil {
			return nil, fmt.Errorf("building web_fetch tool: %w", err)
		}
		tools = append(tools, t)
	}

	if cfg.Tools.WebSearch && isAllowed("web_search", policies) {
		t, err := buildWebSearchTool(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("building web_search tool: %w", err)
		}
		if t != nil {
			tools = append(tools, t)
		}
	}

	if cfg.Tools.Bash && isAllowed("bash", policies) {
		t, err := buildBashTool(ctx, cfg, opts)
		if err != nil {
			return nil, fmt.Errorf("building bash tool: %w", err)
		}
		tools = append(tools, t)
	}

	if cfg.Tools.DocFetcher && isAllowed("doc_fetch", policies) {
		t, err := buildDocFetchTool(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("building doc_fetch tool: %w", err)
		}
		tools = append(tools, t)
	}

	if cfg.Tools.Memory && isAllowed("memory_write", policies) && isAllowed("memory_read", policies) {
		if opts != nil && opts.MemoryStore != nil {
			writeTool, readTool, err := buildMemoryTools(ctx, opts.MemoryStore)
			if err != nil {
				return nil, fmt.Errorf("building memory tools: %w", err)
			}
			tools = append(tools, writeTool, readTool)
		}
	}

	if cfg.Tools.Weather && isAllowed("get_weather", policies) {
		t, err := buildWeatherTool(ctx)
		if err != nil {
			return nil, fmt.Errorf("building weather tool: %w", err)
		}
		tools = append(tools, t)
	}

	if cfg.Tools.Cron {
		if isAllowed("cron_list_jobs", policies) {
			t, err := buildCronListTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building cron_list_jobs tool: %w", err)
			}
			tools = append(tools, t)
		}
		if isAllowed("cron_add_job", policies) {
			t, err := buildCronAddTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building cron_add_job tool: %w", err)
			}
			tools = append(tools, t)
		}
		if isAllowed("cron_remove_job", policies) {
			t, err := buildCronRemoveTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building cron_remove_job tool: %w", err)
			}
			tools = append(tools, t)
		}
		if isAllowed("at_list_jobs", policies) {
			t, err := buildAtListTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building at_list_jobs tool: %w", err)
			}
			tools = append(tools, t)
		}
		if isAllowed("at_add_job", policies) {
			t, err := buildAtAddTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building at_add_job tool: %w", err)
			}
			tools = append(tools, t)
		}
		if isAllowed("at_remove_job", policies) {
			t, err := buildAtRemoveTool(ctx)
			if err != nil {
				return nil, fmt.Errorf("building at_remove_job tool: %w", err)
			}
			tools = append(tools, t)
		}
	}

	// get_datetime: always available — zero side effects, no config needed.
	if isAllowed("get_datetime", policies) {
		t, err := buildDateTimeTool(ctx)
		if err != nil {
			return nil, fmt.Errorf("building get_datetime tool: %w", err)
		}
		tools = append(tools, t)
	}

	// skill_exec: run a skill by name (preferred over bash when skills are available).
	if opts != nil && opts.SkillsLoader != nil && opts.SkillExecutor != nil && isAllowed("skill_exec", policies) {
		t, err := BuildSkillExecTool(opts.SkillsLoader, opts.SkillExecutor, opts.Logger)
		if err != nil {
			return nil, fmt.Errorf("building skill_exec tool: %w", err)
		}
		tools = append(tools, t)
	}

	return tools, nil
}

func buildWebFetchTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"web_fetch",
		"Fetch a URL and return status_code, url, title, and body (text extracted from HTML or raw content). "+
			"Use for summarizing web pages and the summarize_url workflow. "+
			"For GitHub blob URLs (e.g. .../blob/main/README.md) the body may be minimal; for full file content use the raw URL (e.g. https://raw.githubusercontent.com/owner/repo/main/README.md).",
		webFetchFn,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildWebSearchTool(_ context.Context, cfg *config.AgentRuntimeConfig) (tool.BaseTool, error) {
	engine, ok := cfg.Tools.WebSearchConfig.EffectiveEngine()
	if !ok {
		return nil, fmt.Errorf("web_search_config: exactly one of google.enabled or duck_duck_go.enabled must be true (mutually exclusive)")
	}
	if engine == "" {
		return nil, nil // web_search off when no engine enabled
	}

	executor := NewWebSearchExecutor(cfg.Tools.WebSearchConfig, engine)

	engineName := "DuckDuckGo"
	if engine == "google" {
		engineName = "Google"
	}
	desc := fmt.Sprintf("Search the web using %s. ", engineName) +
		"Returns a list of results with title, URL, and snippet. " +
		"Use this tool to find current information, look up facts, or research topics. " +
		"The search engine is set by configuration (do not pass an engine parameter)."

	t, err := toolutils.InferTool(
		"web_search",
		desc,
		executor.ExecWebSearch,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildBashTool(_ context.Context, cfg *config.AgentRuntimeConfig, opts *BuildToolsOptions) (tool.BaseTool, error) {
	var mgr *sandbox.Manager
	var logger *zap.Logger
	var skillsLoader *skills.Loader
	var skillExecutor *skills.Executor

	if opts != nil {
		mgr = opts.SandboxMgr
		logger = opts.Logger
		skillsLoader = opts.SkillsLoader
		skillExecutor = opts.SkillExecutor
	}
	if logger == nil {
		logger, _ = zap.NewNop(), error(nil)
	}

	executor := NewBashExecutor(mgr, cfg.Sandbox, logger)
	if skillsLoader != nil && skillExecutor != nil {
		executor.SetSkillExecution(skillsLoader, skillExecutor)
	}

	t, err := toolutils.InferTool(
		"bash",
		"Execute a shell command and return stdout, stderr, and exit code. "+
			"Commands run in a sandboxed environment. "+
			"Do NOT use this tool for destructive operations (rm -rf /, mkfs, dd, shutdown, etc.).",
		executor.ExecBash,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildDocFetchTool(_ context.Context, cfg *config.AgentRuntimeConfig) (tool.BaseTool, error) {
	executor := NewDocFetchExecutor(cfg.S3)

	t, err := toolutils.InferTool(
		"doc_fetch",
		"Fetch a document from an HTTP/HTTPS URL or S3 URI (s3://bucket/key) and return its text content. "+
			"Supports PDF, HTML, plain text, Markdown, JSON, YAML, and XML. "+
			"For binary media (images, audio, video) returns metadata only. "+
			"Use this tool to read documents, WhatsApp media attachments, or files stored in S3/Minio.",
		executor.ExecDocFetch,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildMemoryTools(_ context.Context, store *memory.Store) (tool.BaseTool, tool.BaseTool, error) {
	executor := NewMemoryToolExecutor(store)

	writeTool, err := toolutils.InferTool(
		"memory_write",
		"Save a fact, observation, or note to durable memory so it persists across sessions. "+
			"Use this when the user asks you to remember something, or when you learn an important fact "+
			"(user preferences, project details, key decisions) that should be recalled later.",
		executor.ExecMemoryWrite,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("building memory_write: %w", err)
	}

	readTool, err := toolutils.InferTool(
		"memory_read",
		"Search durable memory for previously saved notes and facts. "+
			"Use this when the user asks 'do you remember...', refers to previous conversations, "+
			"or when context from past sessions would help answer the current question.",
		executor.ExecMemoryRead,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("building memory_read: %w", err)
	}

	return writeTool, readTool, nil
}

func buildWeatherTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"get_weather",
		"Get current weather conditions and forecast for a location in Canada using Environment Canada. "+
			"Requires latitude and longitude coordinates. "+
			"Use the user's profile coordinates if available. "+
			"If the user has no coordinates set, ask them to share their location or run /setup coords <lat,lon>. "+
			"Returns current temperature, wind, humidity, pressure, and a multi-day forecast.",
		ExecWeather,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildCronListTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"cron_list_jobs",
		"List this user's scheduled cron jobs. Each job runs at a cron schedule and sends a prompt to the agent; "+
			"responses are delivered to the job's notify channels. Use this to see existing job ids before adding or removing.",
		ExecCronListJobs,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildCronAddTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"cron_add_job",
		"Add a scheduled cron job. Provide a unique id, a 6-field cron schedule (e.g. '0 0 9 * * *' for daily at 9:00), "+
			"and a prompt (the message sent to the agent when the job runs). "+
			"Responses are automatically delivered to the user's registered channels. "+
			"Cron jobs are prompts only — no shell commands or scripts.",
		ExecCronAddJob,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildCronRemoveTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"cron_remove_job",
		"Remove a cron job by id. Use cron_list_jobs to see existing ids.",
		ExecCronRemoveJob,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildAtListTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"at_list_jobs",
		"List this user's pending one-shot AT jobs. Each job fires once at a specific time and is auto-deleted after firing. "+
			"Use this to see existing job ids before adding or removing.",
		ExecAtListJobs,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildAtAddTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"at_add_job",
		"Schedule a one-shot AT job that fires once at a specific time and is auto-deleted after firing. "+
			"Use this for reminders, one-time tasks, and delayed actions (NOT for recurring schedules — use cron_add_job for those). "+
			"Provide a unique id, an RFC 3339 run_at timestamp in the future (e.g. '2026-03-08T22:00:00-08:00'), "+
			"and a prompt (the message sent to the agent when the job fires). "+
			"IMPORTANT: Always call get_datetime first to know the current time and timezone, then compute run_at from there. "+
			"Responses are automatically delivered to the user's registered channels.",
		ExecAtAddJob,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildAtRemoveTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"at_remove_job",
		"Cancel and remove a pending AT job by id. Use at_list_jobs to see existing ids.",
		ExecAtRemoveJob,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func buildDateTimeTool(_ context.Context) (tool.BaseTool, error) {
	t, err := toolutils.InferTool(
		"get_datetime",
		"Get the current date, time, weekday, timezone, and UTC offset. "+
			"Optionally pass a timezone (IANA name like 'America/New_York') to get the time in that timezone; "+
			"otherwise returns the system timezone. Use this whenever you need to know the current time, "+
			"check what day it is, or compute scheduling times for cron/at jobs.",
		ExecDateTime,
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// isAllowed returns true if toolName is permitted by the policy.
// If policies is nil, all tools are allowed.
func isAllowed(toolName string, policies *registration.ToolsPolicy) bool {
	if policies == nil {
		return true
	}

	// Check deny list first.
	for _, d := range policies.Deny {
		if d == toolName || d == "*" {
			return false
		}
	}

	// If allow list is non-empty, tool must be in it.
	if len(policies.Allow) > 0 {
		for _, a := range policies.Allow {
			if a == toolName || a == "*" {
				return true
			}
		}
		return false
	}

	return true
}
