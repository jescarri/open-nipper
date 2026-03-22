// Package sandbox manages Docker container sandboxes for safe command execution.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

const (
	containerPrefix     = "nipper-sandbox-"
	defaultShell        = "/bin/bash"
	maxOutputBytes      = 50 * 1024 // 50 KB per stream
	dockerBin           = "docker"
	healthRetries       = 30
	healthRetrySleep    = 500 * time.Millisecond
	skillsMountPoint   = "/skills"
	secretsStagingName = "nipper-secrets"
	secretsMountPoint  = "/tmp/secrets"

	// defaultPath is set in the container so exec'd shells have a usable PATH (minimal images may not set it).
	defaultPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// containerEnv returns env vars for the container: defaults (e.g. PATH) first, then cfg overrides/additions.
func containerEnv(cfg []string) []string {
	m := make(map[string]string)
	// defaults
	if idx := strings.Index(defaultPath, "="); idx > 0 {
		m[defaultPath[:idx]] = defaultPath
	}
	// config overrides/additions
	for _, e := range cfg {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if idx := strings.Index(e, "="); idx > 0 {
			m[e[:idx]] = e
		}
	}
	// deterministic order: PATH first, then rest sorted
	out := make([]string, 0, len(m))
	if v, ok := m["PATH"]; ok {
		out = append(out, v)
		delete(m, "PATH")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// Manager handles the lifecycle of a Docker sandbox container.
type Manager struct {
	cfg          config.SandboxConfig
	logger       *zap.Logger
	containerID  string
	name         string
	stagingDir   string // host path for large secret files; mounted at secretsMountPoint
	mu           sync.Mutex
}

// NewManager creates a sandbox manager from config. Call Create() to start the container.
func NewManager(cfg config.SandboxConfig, logger *zap.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		logger: logger,
		name:   containerPrefix + uuid.NewString()[:8],
	}
}

// Create pulls the image (if needed) and starts the sandbox container.
func (m *Manager) Create(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("creating sandbox container",
		zap.String("image", m.cfg.Image),
		zap.String("name", m.name),
		zap.Bool("read_only", m.cfg.ReadOnly),
	)

	args := []string{
		"run", "-d",
		"--name", m.name,
		"--hostname", "sandbox",
		"--memory", fmt.Sprintf("%dm", m.cfg.MemoryLimitMB),
		"--cpus", fmt.Sprintf("%.1f", m.cfg.CPULimit),
		"--pids-limit", "256",
	}
	if m.cfg.ReadOnly {
		args = append(args, "--read-only")
	}
	args = append(args,
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
		"--tmpfs", fmt.Sprintf("%s:rw,exec,nosuid,size=512m", m.cfg.WorkDir),
		"--cap-drop", "ALL",
		// Minimal capabilities for APT's _apt sandbox: see SANDBOX_APT_CAPABILITIES.md
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE",
		"--security-opt", "no-new-privileges",
		"--workdir", m.cfg.WorkDir,
	)

	// Add extra capabilities from config (e.g. NET_RAW for nmap/ping).
	for _, cap := range m.cfg.ExtraCapabilities {
		args = append(args, "--cap-add", cap)
	}

	if !m.cfg.NetworkEnabled {
		args = append(args, "--network", "none")
	}

	for host, container := range m.cfg.VolumeMounts {
		args = append(args, "-v", fmt.Sprintf("%s:%s", host, container))
	}

	if m.cfg.SkillsPath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro", m.cfg.SkillsPath, skillsMountPoint))
	}

	// Staging dir for large secrets (e.g. SSH keys); per-container, cleaned on Cleanup.
	stagingHost, err := os.MkdirTemp("", secretsStagingName+"-*")
	if err != nil {
		return fmt.Errorf("creating secrets staging dir: %w", err)
	}
	m.stagingDir = stagingHost
	args = append(args, "-v", fmt.Sprintf("%s:%s:ro", m.stagingDir, secretsMountPoint))

	// Ensure container has a sane environment for exec'd commands (e.g. PATH).
	// Config env is applied after defaults so config can override.
	for _, env := range containerEnv(m.cfg.Env) {
		args = append(args, "-e", env)
	}

	args = append(args, m.cfg.Image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, dockerBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("docker run failed: %w: %s", err, stderr.String())
	}

	m.containerID = strings.TrimSpace(string(out))
	m.logger.Info("sandbox container started",
		zap.String("containerId", m.containerID[:12]),
		zap.String("name", m.name),
	)

	if err := m.waitReady(ctx); err != nil {
		m.cleanupLocked(context.Background())
		return fmt.Errorf("sandbox container not ready: %w", err)
	}

	return nil
}

// Exec runs a command inside the sandbox container.
func (m *Manager) Exec(ctx context.Context, command, workDir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	m.mu.Lock()
	if m.containerID == "" {
		m.mu.Unlock()
		return "", "", -1, fmt.Errorf("sandbox container not created")
	}
	cid := m.containerID
	m.mu.Unlock()

	if timeout <= 0 {
		timeout = time.Duration(m.cfg.TimeoutSeconds) * time.Second
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"exec"}
	if workDir != "" {
		args = append(args, "-w", workDir)
	}
	// -l: login shell, so bash sources /etc/profile and ~/.profile (PATH, etc.)
	args = append(args, cid, defaultShell, "-l", "-c", command)

	cmd := exec.CommandContext(execCtx, dockerBin, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	exitCode = 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", "", -1, fmt.Errorf("docker exec failed: %w", runErr)
		}
	}

	stdout = truncateOutput(stdoutBuf.String())
	stderr = truncateOutput(stderrBuf.String())

	return stdout, stderr, exitCode, nil
}

// ExecWithEnv runs a command inside the sandbox with additional environment variables.
// Env vars are passed to docker exec via -e KEY=VALUE (per-execution, not visible to other execs).
func (m *Manager) ExecWithEnv(ctx context.Context, command, workDir string, timeout time.Duration, env map[string]string) (stdout, stderr string, exitCode int, err error) {
	m.mu.Lock()
	if m.containerID == "" {
		m.mu.Unlock()
		return "", "", -1, fmt.Errorf("sandbox container not created")
	}
	cid := m.containerID
	m.mu.Unlock()

	if timeout <= 0 {
		timeout = time.Duration(m.cfg.TimeoutSeconds) * time.Second
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"exec"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	if workDir != "" {
		args = append(args, "-w", workDir)
	}
	// -l: login shell, so bash sources /etc/profile and ~/.profile (PATH, etc.)
	args = append(args, cid, defaultShell, "-l", "-c", command)

	cmd := exec.CommandContext(execCtx, dockerBin, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	exitCode = 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", "", -1, fmt.Errorf("docker exec failed: %w", runErr)
		}
	}

	stdout = truncateOutput(stdoutBuf.String())
	stderr = truncateOutput(stderrBuf.String())

	return stdout, stderr, exitCode, nil
}

// WorkDir returns the configured working directory inside the container.
func (m *Manager) WorkDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.WorkDir
}

// StagingDir returns the host path of the secrets staging directory (for writing large secrets).
// Empty if the container was not created or staging was not set up.
func (m *Manager) StagingDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stagingDir
}

// Cleanup stops and removes the sandbox container.
func (m *Manager) Cleanup(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(ctx)
}

func (m *Manager) cleanupLocked(ctx context.Context) {
	if m.containerID == "" {
		return
	}

	m.logger.Info("cleaning up sandbox container",
		zap.String("containerId", m.containerID[:12]),
	)

	cleanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cleanCtx, dockerBin, "rm", "-f", m.containerID)
	if out, err := cmd.CombinedOutput(); err != nil {
		m.logger.Warn("failed to remove sandbox container",
			zap.Error(err),
			zap.String("output", string(out)),
		)
	}

	m.containerID = ""

	if m.stagingDir != "" {
		_ = os.RemoveAll(m.stagingDir)
		m.stagingDir = ""
	}
}

// IsRunning returns true if the sandbox container is created and presumably running.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.containerID != ""
}

func (m *Manager) waitReady(ctx context.Context) error {
	for i := 0; i < healthRetries; i++ {
		cmd := exec.CommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Running}}", m.containerID)
		out, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthRetrySleep):
		}
	}
	return fmt.Errorf("container did not become ready within %v", time.Duration(healthRetries)*healthRetrySleep)
}

func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputBytes] + "\n... [output truncated at 50KB]"
}
