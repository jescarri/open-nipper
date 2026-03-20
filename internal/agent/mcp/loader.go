// Package mcp loads tools from external MCP (Model Context Protocol) servers.
// It supports SSE, Streamable HTTP, and STDIO transports, with environment
// variable expansion in all configuration fields.
//
// SSE and Streamable clients support OIDC device authorization grant for
// headless environments. Tokens are encrypted at rest with AES-256-GCM.
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/mcp/oidc"
	"github.com/jescarri/open-nipper/internal/agent/secrets"
	"github.com/jescarri/open-nipper/internal/config"
)

// DeviceAuthNotifier is called when the OIDC device authorization flow starts.
// It receives the server name, verification URL, user code, and expiry so the
// caller can notify the user through available channels (WhatsApp, Slack, etc.).
type DeviceAuthNotifier func(ctx context.Context, serverName, verificationURI, userCode string, expiresIn int)

const (
	defaultKeepAliveInterval = 30 * time.Second
	pingTimeout              = 10 * time.Second
	reconnectInitialDelay    = 1 * time.Second
	reconnectMaxDelay        = 60 * time.Second
	// stdioConnectTimeout is the max time to wait for a STDIO MCP subprocess to
	// start and complete the initialize handshake. Prevents agent startup from
	// blocking on slow or stuck STDIO servers.
	stdioConnectTimeout = 60 * time.Second
	// sseConnectTimeout is the max time to wait for an SSE/Streamable MCP
	// server to connect during initial startup. If the server is not available
	// within this timeout, the agent starts without it and retries in the
	// background.
	sseConnectTimeout = 10 * time.Second
)

// Loader manages MCP client connections and exposes their tools as Eino BaseTools.
// For SSE transports it runs a keepalive goroutine that pings each server and
// automatically reconnects (with exponential backoff) on session loss.
type Loader struct {
	mu      sync.RWMutex
	clients []*managedClient
	tools   []tool.BaseTool
	logger  *zap.Logger
	configs []config.MCPServerConfig // expanded configs, retained for reconnection

	ctx    context.Context
	cancel context.CancelFunc

	// OIDC providers keyed by server name.
	providers map[string]*oidc.Provider

	// basePath and encryptionPassword for creating new providers on reconnect.
	basePath           string
	encryptionPassword string

	// notifier is called when an OIDC device flow starts (optional).
	notifier DeviceAuthNotifier
}

type managedClient struct {
	name    string
	client  *mcpsdk.Client
	session *mcpsdk.ClientSession
}

// NewLoader creates MCP clients from config, initializes them, and loads their tools.
// SSE clients connect to a remote URL; STDIO clients spawn a local subprocess.
// All ${VAR} placeholders in config fields are expanded from environment variables.
//
// basePath is the agent's base path for storing encrypted tokens.
// encryptionPassword is required when any MCP server uses OIDC auth.
func NewLoader(ctx context.Context, configs []config.MCPServerConfig, logger *zap.Logger, basePath, encryptionPassword string, notifier DeviceAuthNotifier) (*Loader, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	loaderCtx, cancel := context.WithCancel(ctx)
	l := &Loader{
		logger:             logger,
		configs:            make([]config.MCPServerConfig, 0, len(configs)),
		ctx:                loaderCtx,
		cancel:             cancel,
		providers:          make(map[string]*oidc.Provider),
		basePath:           basePath,
		encryptionPassword: encryptionPassword,
		notifier:           notifier,
	}

	hasSSE := false
	stdioIndices := make([]int, 0)
	sseRetryIndices := make([]int, 0)
	for i, cfg := range configs {
		if err := validateConfig(cfg); err != nil {
			l.Close()
			return nil, fmt.Errorf("mcp server config[%d] (%s): %w", i, cfg.Name, err)
		}

		expanded := expandConfig(cfg)
		l.configs = append(l.configs, expanded)

		// Bootstrap OIDC if configured.
		if expanded.Auth != nil && strings.ToLower(expanded.Auth.Type) == "oidc" {
			if err := l.bootstrapOIDC(loaderCtx, expanded); err != nil {
				l.Close()
				return nil, fmt.Errorf("mcp server %q: %w", expanded.Name, err)
			}
		}

		transport := strings.ToLower(expanded.Transport)
		if transport == "stdio" {
			// Connect STDIO in the background so agent startup does not block on
			// slow subprocess startup (e.g. Docker). Tools appear when the server
			// is ready.
			l.clients = append(l.clients, &managedClient{name: expanded.Name, client: nil, session: nil})
			stdioIndices = append(stdioIndices, len(l.clients)-1)
			continue
		}

		if transport == "sse" || transport == "streamable" {
			hasSSE = true
		}

		// SSE/Streamable: try to connect with a timeout. If the server is not
		// available, add a placeholder and retry in the background so the agent
		// can still start and process messages with whatever tools are available.
		connectCtx, connectCancel := context.WithTimeout(loaderCtx, sseConnectTimeout)
		cli, session, err := l.createClient(connectCtx, expanded)
		connectCancel()
		if err != nil {
			logger.Warn("MCP server not available at startup, will retry in background",
				zap.String("name", expanded.Name),
				zap.String("transport", expanded.Transport),
				zap.Error(err),
			)
			l.clients = append(l.clients, &managedClient{name: expanded.Name, client: nil, session: nil})
			sseRetryIndices = append(sseRetryIndices, len(l.clients)-1)
			continue
		}

		l.clients = append(l.clients, &managedClient{
			name:    expanded.Name,
			client:  cli,
			session: session,
		})

		logger.Info("mcp server connected",
			zap.String("name", expanded.Name),
			zap.String("transport", expanded.Transport),
		)
	}

	if err := l.reloadTools(loaderCtx); err != nil {
		// reloadTools only errors when ALL servers fail; if some connected,
		// this won't error. If none connected, we still want to start.
		logger.Warn("initial tool reload failed (will retry after background connections)",
			zap.Error(err),
		)
	}

	if hasSSE {
		go l.keepaliveLoop()
	}

	if len(stdioIndices) > 0 {
		go l.connectStdioAsync(stdioIndices)
	}

	for _, idx := range sseRetryIndices {
		go l.reconnectWithBackoff(idx)
	}

	return l, nil
}

// bootstrapOIDC creates and initializes an OIDC provider for a server.
func (l *Loader) bootstrapOIDC(ctx context.Context, cfg config.MCPServerConfig) error {
	authCfg := cfg.Auth
	providerCfg := &oidc.ProviderConfig{
		ClientID:         secrets.ExpandString(authCfg.ClientID),
		ClientSecret:     secrets.ExpandString(authCfg.ClientSecret),
		Scopes:           authCfg.Scopes,
		IssuerURL:        secrets.ExpandString(authCfg.IssuerURL),
		AuthorizationURL: secrets.ExpandString(authCfg.AuthorizationURL),
		DeviceAuthURL:    secrets.ExpandString(authCfg.DeviceAuthURL),
		TokenURL:         secrets.ExpandString(authCfg.TokenURL),
		Audience:         authCfg.Audience,
		Flow:             authCfg.Flow,
	}

	// Convert loader notifier to OIDC provider notifier.
	var oidcNotifier oidc.DeviceAuthNotifier
	if l.notifier != nil {
		loaderNotifier := l.notifier
		oidcNotifier = func(ctx context.Context, serverName, verificationURI, userCode string, expiresIn int) {
			loaderNotifier(ctx, serverName, verificationURI, userCode, expiresIn)
		}
	}

	provider, err := oidc.NewProvider(ctx, providerCfg, l.basePath, cfg.Name, l.encryptionPassword, l.logger, oidcNotifier)
	if err != nil {
		return fmt.Errorf("OIDC setup: %w", err)
	}

	if err := provider.EnsureToken(ctx); err != nil {
		return fmt.Errorf("OIDC token acquisition: %w", err)
	}

	l.providers[cfg.Name] = provider
	return nil
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

// ToolsByNames returns only the MCP tools whose names are in the given set.
// Used by lean MCP mode to bind a subset of tools after search_tools resolves.
func (l *Loader) ToolsByNames(names []string) []tool.BaseTool {
	if l == nil || len(names) == 0 {
		return nil
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	var matched []tool.BaseTool
	for _, t := range l.tools {
		info, err := t.Info(context.Background())
		if err != nil {
			continue
		}
		if nameSet[info.Name] {
			matched = append(matched, t)
		}
	}
	return matched
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
		if mc != nil && mc.session != nil {
			if err := mc.session.Close(); err != nil {
				l.logger.Warn("failed to close mcp session",
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
// or until ctx is cancelled or timeout expires.
func (l *Loader) WaitForReconnect(ctx context.Context, timeout time.Duration) bool {
	// With the official SDK, reconnection is handled internally by the
	// Streamable transport. For SSE, we rely on keepalive pings.
	// This is kept for API compatibility.
	deadline := time.After(timeout)
	select {
	case <-ctx.Done():
		return false
	case <-deadline:
		return true
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
			l.pingAll()
		}
	}
}

func (l *Loader) keepaliveInterval() time.Duration {
	best := defaultKeepAliveInterval
	for _, cfg := range l.configs {
		transport := strings.ToLower(cfg.Transport)
		if (transport == "sse" || transport == "streamable") && cfg.KeepAliveSeconds > 0 {
			d := time.Duration(cfg.KeepAliveSeconds) * time.Second
			if d < best {
				best = d
			}
		}
	}
	return best
}

func (l *Loader) pingAll() {
	l.mu.RLock()
	snapshot := make([]*managedClient, len(l.clients))
	copy(snapshot, l.clients)
	l.mu.RUnlock()

	for i, mc := range snapshot {
		transport := strings.ToLower(l.configs[i].Transport)
		if transport != "sse" && transport != "streamable" {
			continue
		}
		if mc.session == nil {
			continue
		}

		pingCtx, cancel := context.WithTimeout(l.ctx, pingTimeout)
		err := mc.session.Ping(pingCtx, &mcpsdk.PingParams{})
		cancel()

		if err != nil {
			l.logger.Warn("MCP keepalive ping failed",
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

func (l *Loader) reconnectWithBackoff(idx int) {
	l.mu.RLock()
	if idx >= len(l.clients) {
		l.mu.RUnlock()
		return
	}
	mc := l.clients[idx]
	l.mu.RUnlock()

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
	l.mu.RUnlock()

	l.logger.Info("reconnecting MCP client",
		zap.String("name", mc.name),
		zap.String("transport", cfg.Transport),
	)

	// Re-ensure OIDC token if needed (may have expired).
	if provider, ok := l.providers[cfg.Name]; ok {
		if err := provider.EnsureToken(l.ctx); err != nil {
			return fmt.Errorf("OIDC token refresh for %q: %w", mc.name, err)
		}
	}

	// Create the new client BEFORE closing the old session so that
	// concurrent reloadTools calls always see a usable session.
	newCli, newSession, err := l.createClient(l.ctx, cfg)
	if err != nil {
		return fmt.Errorf("creating new client for %q: %w", mc.name, err)
	}

	// Swap old → new under the write lock so reloadTools never sees
	// a closed session for this server.
	l.mu.Lock()
	var oldSession *mcpsdk.ClientSession
	if idx < len(l.clients) {
		oldSession = l.clients[idx].session
		l.clients[idx].client = newCli
		l.clients[idx].session = newSession
	}
	l.mu.Unlock()

	// Close the old session AFTER the swap so it's no longer reachable.
	if oldSession != nil {
		_ = oldSession.Close()
	}

	if err := l.reloadTools(l.ctx); err != nil {
		return fmt.Errorf("reloading tools after reconnect: %w", err)
	}

	return nil
}

// connectStdioAsync connects STDIO MCP servers in the background. Each server
// is given stdioConnectTimeout to start and complete the initialize handshake.
// When a server connects, its tools are merged via reloadTools. Loader.ctx
// cancellation stops further connection attempts.
func (l *Loader) connectStdioAsync(stdioIndices []int) {
	for _, idx := range stdioIndices {
		select {
		case <-l.ctx.Done():
			return
		default:
		}

		l.mu.RLock()
		if idx >= len(l.configs) {
			l.mu.RUnlock()
			continue
		}
		cfg := l.configs[idx]
		l.mu.RUnlock()

		connectCtx, cancel := context.WithTimeout(l.ctx, stdioConnectTimeout)
		cli, session, err := l.createClient(connectCtx, cfg)
		cancel()
		if err != nil {
			l.logger.Warn("STDIO MCP server failed to connect (will not retry in this session)",
				zap.String("name", cfg.Name),
				zap.Duration("timeout", stdioConnectTimeout),
				zap.Error(err),
			)
			continue
		}

		mc := &managedClient{name: cfg.Name, client: cli, session: session}
		l.mu.Lock()
		if idx < len(l.clients) {
			l.clients[idx] = mc
		}
		l.mu.Unlock()

		l.logger.Info("mcp server connected",
			zap.String("name", cfg.Name),
			zap.String("transport", cfg.Transport),
		)
		if err := l.reloadTools(l.ctx); err != nil {
			l.logger.Warn("reload tools after STDIO connect failed", zap.Error(err))
		}
	}
}

// ---------------------------------------------------------------------------
// Client creation and initialization
// ---------------------------------------------------------------------------

func (l *Loader) createClient(ctx context.Context, cfg config.MCPServerConfig) (*mcpsdk.Client, *mcpsdk.ClientSession, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "open-nipper-agent",
		Version: "1.0.0",
	}, nil)

	transport, err := l.createTransport(cfg)
	if err != nil {
		return nil, nil, err
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to MCP server %q: %w", cfg.Name, err)
	}

	initResult := session.InitializeResult()
	if initResult != nil {
		l.logger.Debug("mcp server initialized",
			zap.String("name", cfg.Name),
			zap.String("serverName", initResult.ServerInfo.Name),
			zap.String("serverVersion", initResult.ServerInfo.Version),
		)
	}

	return client, session, nil
}

func (l *Loader) createTransport(cfg config.MCPServerConfig) (mcpsdk.Transport, error) {
	switch strings.ToLower(cfg.Transport) {
	case "sse":
		return l.createSSETransport(cfg)
	case "streamable":
		return l.createStreamableTransport(cfg)
	case "stdio":
		return l.createStdioTransport(cfg)
	default:
		return nil, fmt.Errorf("unsupported transport %q (must be \"sse\", \"streamable\", or \"stdio\")", cfg.Transport)
	}
}

func (l *Loader) createSSETransport(cfg config.MCPServerConfig) (*mcpsdk.SSEClientTransport, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("SSE transport requires a URL")
	}

	transport := &mcpsdk.SSEClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: l.buildHTTPClient(cfg),
	}

	return transport, nil
}

func (l *Loader) createStreamableTransport(cfg config.MCPServerConfig) (*mcpsdk.StreamableClientTransport, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("Streamable transport requires a URL")
	}

	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: l.buildHTTPClient(cfg),
	}

	return transport, nil
}

func (l *Loader) createStdioTransport(cfg config.MCPServerConfig) (*mcpsdk.CommandTransport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("STDIO transport requires a command")
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	expandedEnv := expandEnvSlice(cfg.Env)
	if len(expandedEnv) > 0 {
		cmd.Env = expandedEnv
	}

	return &mcpsdk.CommandTransport{Command: cmd}, nil
}

// buildHTTPClient creates an HTTP client with optional auth and custom headers.
func (l *Loader) buildHTTPClient(cfg config.MCPServerConfig) *http.Client {
	var transport http.RoundTripper = http.DefaultTransport

	// Add OIDC bearer token injection if configured.
	if provider, ok := l.providers[cfg.Name]; ok {
		transport = provider.OAuthRoundTripper(transport)
	}

	// Add custom headers if configured.
	if len(cfg.Headers) > 0 {
		transport = &headerRoundTripper{
			base:    transport,
			headers: cfg.Headers,
		}
	}

	return &http.Client{Transport: transport}
}

// headerRoundTripper injects custom headers into every request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Tool loading
// ---------------------------------------------------------------------------

// reloadTools fetches tools from every connected MCP server and replaces the
// internal tool list atomically. Servers whose ListTools call fails are
// skipped with a warning so that one broken server does not block all others.
func (l *Loader) reloadTools(ctx context.Context) error {
	l.mu.RLock()
	snapshot := make([]*managedClient, len(l.clients))
	copy(snapshot, l.clients)
	l.mu.RUnlock()

	var newTools []tool.BaseTool
	var failures int
	for _, mc := range snapshot {
		if mc.session == nil {
			continue
		}

		mcpTools, err := GetToolsFromSession(ctx, mc.session, true)
		if err != nil {
			l.logger.Warn("skipping MCP server during tool reload (session may be reconnecting)",
				zap.String("server", mc.name),
				zap.Error(err),
			)
			failures++
			continue
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

	// If ALL servers failed, keep existing tools rather than clearing them.
	if failures > 0 && len(newTools) == 0 {
		l.logger.Warn("all MCP servers failed tool reload; keeping previous tool set",
			zap.Int("failures", failures),
		)
		return fmt.Errorf("all %d MCP servers failed to reload tools", failures)
	}

	newTools = WrapTools(newTools, l.logger)

	l.mu.Lock()
	l.tools = newTools
	l.mu.Unlock()

	if failures > 0 {
		l.logger.Warn("tool reload completed with partial failures",
			zap.Int("failures", failures),
			zap.Int("totalTools", len(newTools)),
		)
	}

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
	if transport != "sse" && transport != "stdio" && transport != "streamable" {
		return fmt.Errorf("transport must be \"sse\", \"streamable\", or \"stdio\", got %q", cfg.Transport)
	}
	if (transport == "sse" || transport == "streamable") && cfg.URL == "" {
		return fmt.Errorf("%s transport requires url", strings.ToUpper(transport))
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
		Auth:             cfg.Auth,
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
