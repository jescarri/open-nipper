// Package mcp loads tools from external MCP (Model Context Protocol) servers.
// It supports SSE and STDIO transports, with environment variable expansion
// in all configuration fields (URLs, commands, args, headers, env vars).
//
// SSE clients are kept alive with periodic pings and automatically reconnected
// (with exponential backoff) when the server-side session expires or the SSE
// stream drops.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcptypes "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"github.com/cloudwego/eino/components/tool"
	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"

	"github.com/jescarri/open-nipper/internal/agent/secrets"
	"github.com/jescarri/open-nipper/internal/config"
)

const (
	defaultKeepAliveInterval = 30 * time.Second
	pingTimeout              = 10 * time.Second
	reconnectInitialDelay = 1 * time.Second
	reconnectMaxDelay     = 60 * time.Second
)

// softErrorHandler demotes MCP tool errors from hard Go errors to normal tool
// response content. Without this, any isError:true response from the MCP server
// propagates as a NodeRunError that kills the entire ReAct run before the model
// can see the failure. By clearing IsError here, the eino-ext adapter returns
// the error text as a regular tool result string, allowing the reasoning model
// to read the failure and recover (e.g. by calling GetLiveContext to discover
// the correct entity name before retrying).
func softErrorHandler(_ context.Context, _ string, result *mcptypes.CallToolResult) (*mcptypes.CallToolResult, error) {
	result.IsError = false
	return result, nil
}

// Loader manages MCP client connections and exposes their tools as Eino BaseTools.
// For SSE transports it runs a keepalive goroutine that pings each server and
// automatically reconnects (with exponential backoff) on session loss.
type Loader struct {
	mu      sync.RWMutex
	clients []*managedClient
	tools   []tool.BaseTool
	logger  *zap.Logger
	configs []config.MCPServerConfig // expanded configs, retained for reconnection
	ctx     context.Context
	cancel  context.CancelFunc
}

type managedClient struct {
	name         string
	client       *mcpclient.Client
	reconnecting atomic.Bool
}

// NewLoader creates MCP clients from config, initializes them, and loads their tools.
// SSE clients connect to a remote URL; STDIO clients spawn a local subprocess.
// All ${VAR} placeholders in config fields are expanded from environment variables.
// A background keepalive goroutine is started for SSE clients; it is stopped by Close.
func NewLoader(ctx context.Context, configs []config.MCPServerConfig, logger *zap.Logger) (*Loader, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	loaderCtx, cancel := context.WithCancel(ctx)
	l := &Loader{
		logger:  logger,
		configs: make([]config.MCPServerConfig, 0, len(configs)),
		ctx:     loaderCtx,
		cancel:  cancel,
	}

	hasSSE := false
	for i, cfg := range configs {
		if err := validateConfig(cfg); err != nil {
			l.Close()
			return nil, fmt.Errorf("mcp server config[%d] (%s): %w", i, cfg.Name, err)
		}

		expanded := expandConfig(cfg)
		l.configs = append(l.configs, expanded)

		cli, err := l.createClient(loaderCtx, expanded)
		if err != nil {
			l.Close()
			return nil, fmt.Errorf("mcp server %q: %w", expanded.Name, err)
		}

		mc := &managedClient{
			name:   expanded.Name,
			client: cli,
		}
		l.clients = append(l.clients, mc)

		if strings.ToLower(expanded.Transport) == "sse" {
			hasSSE = true
			idx := len(l.clients) - 1
			l.registerConnectionLostHandler(cli, expanded.Name, idx)
		}

		logger.Info("mcp server connected",
			zap.String("name", expanded.Name),
			zap.String("transport", expanded.Transport),
		)
	}

	if err := l.reloadTools(loaderCtx); err != nil {
		l.Close()
		return nil, err
	}

	if hasSSE {
		go l.keepaliveLoop()
	}

	return l, nil
}

// Tools returns all tools loaded from MCP servers.
// Thread-safe; returns a snapshot of the current tool set (updated on reconnection).
func (l *Loader) Tools() []tool.BaseTool {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]tool.BaseTool, len(l.tools))
	copy(out, l.tools)
	return out
}

// ToolNames returns the names of all loaded MCP tools (for logging / system prompt).
func (l *Loader) ToolNames() []string {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.tools))
	for _, t := range l.tools {
		info, err := t.Info(context.Background())
		if err == nil {
			names = append(names, info.Name)
		}
	}
	return names
}

// ToolInfo holds the name and description of a single MCP tool.
type ToolInfo struct {
	Name string
	Desc string
}

// ToolInfos returns the name and description of every loaded MCP tool.
// Descriptions come directly from the MCP server so the system prompt can
// give the model accurate, tool-specific guidance instead of a generic placeholder.
func (l *Loader) ToolInfos() []ToolInfo {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	infos := make([]ToolInfo, 0, len(l.tools))
	for _, t := range l.tools {
		info, err := t.Info(context.Background())
		if err == nil {
			infos = append(infos, ToolInfo{Name: info.Name, Desc: info.Desc})
		}
	}
	return infos
}

// Close shuts down the keepalive goroutine, closes all MCP clients, and releases resources.
func (l *Loader) Close() {
	if l == nil {
		return
	}
	if l.cancel != nil {
		l.cancel()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, mc := range l.clients {
		if mc != nil && mc.client != nil {
			if err := mc.client.Close(); err != nil {
				l.logger.Warn("failed to close mcp client",
					zap.String("name", mc.name),
					zap.Error(err),
				)
			}
		}
	}
	l.clients = nil
	l.tools = nil
}

// WaitForReconnect blocks until no MCP client is actively reconnecting,
// or until ctx is cancelled or timeout expires. Returns true if all
// reconnections finished, false on timeout or cancellation.
func (l *Loader) WaitForReconnect(ctx context.Context, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		reconnecting := false
		l.mu.RLock()
		for _, mc := range l.clients {
			if mc.reconnecting.Load() {
				reconnecting = true
				break
			}
		}
		l.mu.RUnlock()

		if !reconnecting {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Keepalive
// ---------------------------------------------------------------------------

func (l *Loader) keepaliveLoop() {
	interval := l.keepaliveInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	l.logger.Info("MCP keepalive started", zap.Duration("interval", interval))

	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.pingAllSSE()
		}
	}
}

func (l *Loader) keepaliveInterval() time.Duration {
	best := defaultKeepAliveInterval
	for _, cfg := range l.configs {
		if strings.ToLower(cfg.Transport) == "sse" && cfg.KeepAliveSeconds > 0 {
			d := time.Duration(cfg.KeepAliveSeconds) * time.Second
			if d < best {
				best = d
			}
		}
	}
	return best
}

func (l *Loader) pingAllSSE() {
	l.mu.RLock()
	snapshot := make([]*managedClient, len(l.clients))
	copy(snapshot, l.clients)
	l.mu.RUnlock()

	for i, mc := range snapshot {
		if strings.ToLower(l.configs[i].Transport) != "sse" {
			continue
		}

		pingCtx, cancel := context.WithTimeout(l.ctx, pingTimeout)
		err := mc.client.Ping(pingCtx)
		cancel()

		if err != nil {
			l.logger.Warn("MCP keepalive ping failed, triggering reconnect",
				zap.String("name", mc.name),
				zap.Error(err),
			)
			go l.reconnectWithBackoff(i)
		} else {
			l.logger.Debug("MCP keepalive ping OK", zap.String("name", mc.name))
		}
	}
}

// ---------------------------------------------------------------------------
// Reconnection
// ---------------------------------------------------------------------------

func (l *Loader) registerConnectionLostHandler(cli *mcpclient.Client, name string, idx int) {
	cli.OnConnectionLost(func(err error) {
		l.logger.Warn("MCP SSE connection lost, triggering reconnect",
			zap.String("name", name),
			zap.Error(err),
		)
		go l.reconnectWithBackoff(idx)
	})
}

func (l *Loader) reconnectWithBackoff(idx int) {
	l.mu.RLock()
	if idx >= len(l.clients) {
		l.mu.RUnlock()
		return
	}
	mc := l.clients[idx]
	l.mu.RUnlock()

	if !mc.reconnecting.CompareAndSwap(false, true) {
		return
	}
	defer mc.reconnecting.Store(false)

	backoff := reconnectInitialDelay
	for attempt := 1; ; attempt++ {
		select {
		case <-l.ctx.Done():
			return
		default:
		}

		if err := l.reconnectClient(idx); err != nil {
			l.logger.Error("MCP reconnect attempt failed",
				zap.String("name", mc.name),
				zap.Int("attempt", attempt),
				zap.Duration("nextRetry", backoff),
				zap.Error(err),
			)
			select {
			case <-l.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, reconnectMaxDelay)
			continue
		}

		l.logger.Info("MCP client reconnected",
			zap.String("name", mc.name),
			zap.Int("attempts", attempt),
		)
		return
	}
}

func (l *Loader) reconnectClient(idx int) error {
	l.mu.RLock()
	if idx >= len(l.clients) {
		l.mu.RUnlock()
		return fmt.Errorf("client index %d out of range", idx)
	}
	cfg := l.configs[idx]
	mc := l.clients[idx]
	oldCli := mc.client
	l.mu.RUnlock()

	l.logger.Info("reconnecting MCP client",
		zap.String("name", mc.name),
		zap.String("transport", cfg.Transport),
	)

	if oldCli != nil {
		_ = oldCli.Close()
	}

	// Use l.ctx (the loader's long-lived context) for createClient, NOT a
	// timeout-derived context. The SSE transport stores the context passed to
	// Start() and derives a child context that keeps the SSE stream alive.
	// If we used a timeout context here its deferred cancel would immediately
	// kill the brand-new SSE stream, triggering the connection-lost handler
	// in an infinite reconnect loop.
	//
	// Timeout protection is still provided by the mcp-go library itself:
	// SSE.Start() has a 30s endpoint wait, and SendRequest() has a 60s
	// response timeout.
	newCli, err := l.createClient(l.ctx, cfg)
	if err != nil {
		return fmt.Errorf("creating new client for %q: %w", mc.name, err)
	}

	if strings.ToLower(cfg.Transport) == "sse" {
		l.registerConnectionLostHandler(newCli, cfg.Name, idx)
	}

	l.mu.Lock()
	if idx < len(l.clients) {
		l.clients[idx].client = newCli
	}
	l.mu.Unlock()

	if err := l.reloadTools(l.ctx); err != nil {
		return fmt.Errorf("reloading tools after reconnect: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Client creation and initialization
// ---------------------------------------------------------------------------

func (l *Loader) createClient(ctx context.Context, cfg config.MCPServerConfig) (*mcpclient.Client, error) {
	switch strings.ToLower(cfg.Transport) {
	case "sse":
		return l.createSSEClient(ctx, cfg)
	case "stdio":
		return l.createStdioClient(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport %q (must be \"sse\" or \"stdio\")", cfg.Transport)
	}
}

func (l *Loader) createSSEClient(ctx context.Context, cfg config.MCPServerConfig) (*mcpclient.Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("SSE transport requires a URL")
	}

	var opts []mcptransport.ClientOption
	if len(cfg.Headers) > 0 {
		opts = append(opts, mcptransport.WithHeaders(cfg.Headers))
	}

	cli, err := mcpclient.NewSSEMCPClient(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating SSE client for %q: %w", cfg.URL, err)
	}

	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("starting SSE client for %q: %w", cfg.URL, err)
	}

	if err := l.initializeClient(ctx, cli, cfg.Name); err != nil {
		_ = cli.Close()
		return nil, err
	}

	return cli, nil
}

func (l *Loader) createStdioClient(ctx context.Context, cfg config.MCPServerConfig) (*mcpclient.Client, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("STDIO transport requires a command")
	}

	expandedEnv := expandEnvSlice(cfg.Env)

	cli, err := mcpclient.NewStdioMCPClient(cfg.Command, expandedEnv, cfg.Args...)
	if err != nil {
		return nil, fmt.Errorf("creating STDIO client for %q: %w", cfg.Command, err)
	}

	if err := l.initializeClient(ctx, cli, cfg.Name); err != nil {
		_ = cli.Close()
		return nil, err
	}

	return cli, nil
}

func (l *Loader) initializeClient(ctx context.Context, cli *mcpclient.Client, name string) error {
	initReq := mcptypes.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcptypes.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcptypes.Implementation{
		Name:    "open-nipper-agent",
		Version: "1.0.0",
	}

	result, err := cli.Initialize(ctx, initReq)
	if err != nil {
		return fmt.Errorf("initializing MCP server %q: %w", name, err)
	}

	l.logger.Debug("mcp server initialized",
		zap.String("name", name),
		zap.String("serverName", result.ServerInfo.Name),
		zap.String("serverVersion", result.ServerInfo.Version),
		zap.String("protocolVersion", result.ProtocolVersion),
	)

	return nil
}

// ---------------------------------------------------------------------------
// Tool loading
// ---------------------------------------------------------------------------

// reloadTools fetches tools from every connected MCP server and replaces the
// internal tool list atomically. Safe to call from any goroutine.
func (l *Loader) reloadTools(ctx context.Context) error {
	l.mu.RLock()
	snapshot := make([]*managedClient, len(l.clients))
	copy(snapshot, l.clients)
	l.mu.RUnlock()

	var newTools []tool.BaseTool
	for _, mc := range snapshot {
		mcpTools, err := einomcp.GetTools(ctx, &einomcp.Config{
			Cli:                   mc.client,
			ToolCallResultHandler: softErrorHandler,
		})
		if err != nil {
			return fmt.Errorf("loading tools from MCP server %q: %w", mc.name, err)
		}

		l.logger.Info("mcp tools loaded",
			zap.String("server", mc.name),
			zap.Int("toolCount", len(mcpTools)),
		)

		for _, t := range mcpTools {
			info, infoErr := t.Info(ctx)
			if infoErr != nil {
				l.logger.Warn("failed to get MCP tool info", zap.Error(infoErr))
				continue
			}
			l.logger.Debug("mcp tool available",
				zap.String("server", mc.name),
				zap.String("tool", info.Name),
				zap.String("desc", info.Desc),
			)
		}

		newTools = append(newTools, mcpTools...)
	}

	newTools = WrapTools(newTools, l.logger)

	l.mu.Lock()
	l.tools = newTools
	l.mu.Unlock()

	return nil
}

// ---------------------------------------------------------------------------
// Config validation and expansion
// ---------------------------------------------------------------------------

// validateConfig checks that required fields are present before expansion.
func validateConfig(cfg config.MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("name is required")
	}
	transport := strings.ToLower(cfg.Transport)
	if transport != "sse" && transport != "stdio" {
		return fmt.Errorf("transport must be \"sse\" or \"stdio\", got %q", cfg.Transport)
	}
	if transport == "sse" && cfg.URL == "" {
		return fmt.Errorf("SSE transport requires url")
	}
	if transport == "stdio" && cfg.Command == "" {
		return fmt.Errorf("STDIO transport requires command")
	}
	return nil
}

// expandConfig replaces ${VAR} placeholders in all string fields with env var values.
func expandConfig(cfg config.MCPServerConfig) config.MCPServerConfig {
	out := config.MCPServerConfig{
		Name:             cfg.Name,
		Transport:        cfg.Transport,
		Command:          secrets.ExpandString(cfg.Command),
		URL:              secrets.ExpandString(cfg.URL),
		Args:             make([]string, len(cfg.Args)),
		Env:              make([]string, len(cfg.Env)),
		KeepAliveSeconds: cfg.KeepAliveSeconds,
	}

	for i, a := range cfg.Args {
		out.Args[i] = secrets.ExpandString(a)
	}
	for i, e := range cfg.Env {
		out.Env[i] = secrets.ExpandString(e)
	}

	if len(cfg.Headers) > 0 {
		out.Headers = make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			out.Headers[k] = secrets.ExpandString(v)
		}
	}

	return out
}

// expandEnvSlice expands ${VAR} in each element and also inherits values from
// the process environment when the format is "KEY=${KEY}" and the var is set.
func expandEnvSlice(envs []string) []string {
	out := make([]string, 0, len(envs))
	for _, e := range envs {
		expanded := secrets.ExpandString(e)
		if expanded != "" {
			out = append(out, expanded)
		}
	}
	return out
}
