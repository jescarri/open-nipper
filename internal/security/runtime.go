package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// HealthChecker can report whether a subsystem is reachable.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// RuntimeMonitor performs periodic security checks while the gateway is running.
type RuntimeMonitor struct {
	cfg          *config.Config
	logger       *zap.Logger
	datastore    HealthChecker
	queueChecker HealthChecker

	mu                sync.RWMutex
	failedRegistrations int64
	findings            []AuditFinding
	datastoreOK         bool
	queueOK             bool

	cancel context.CancelFunc
	done   chan struct{}
}

// RuntimeMonitorDeps bundles the dependencies for constructing a RuntimeMonitor.
type RuntimeMonitorDeps struct {
	Config       *config.Config
	Logger       *zap.Logger
	Datastore    HealthChecker
	QueueChecker HealthChecker
}

// NewRuntimeMonitor creates a new runtime security monitor.
func NewRuntimeMonitor(deps RuntimeMonitorDeps) *RuntimeMonitor {
	return &RuntimeMonitor{
		cfg:          deps.Config,
		logger:       deps.Logger,
		datastore:    deps.Datastore,
		queueChecker: deps.QueueChecker,
		datastoreOK:  true,
		queueOK:      true,
		done:         make(chan struct{}),
	}
}

// Start launches background goroutines for periodic security checks.
func (m *RuntimeMonitor) Start(ctx context.Context) {
	monCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	go m.symlinksLoop(monCtx)
	go m.connectivityLoop(monCtx)
	go m.registrationFailureLoop(monCtx)

	m.logger.Info("runtime security monitor started")
}

// Stop terminates all background goroutines.
func (m *RuntimeMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.logger.Info("runtime security monitor stopped")
}

// RecordFailedRegistration increments the failed registration counter.
func (m *RuntimeMonitor) RecordFailedRegistration() {
	atomic.AddInt64(&m.failedRegistrations, 1)
}

// IsDatastoreHealthy reports whether the datastore is currently reachable.
func (m *RuntimeMonitor) IsDatastoreHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.datastoreOK
}

// IsQueueHealthy reports whether the queue system is currently reachable.
func (m *RuntimeMonitor) IsQueueHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.queueOK
}

// Findings returns the most recent set of runtime security findings.
func (m *RuntimeMonitor) Findings() []AuditFinding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]AuditFinding, len(m.findings))
	copy(out, m.findings)
	return out
}

func (m *RuntimeMonitor) symlinksLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkSymlinks()
		}
	}
}

func (m *RuntimeMonitor) connectivityLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkConnectivity(ctx)
		}
	}
}

func (m *RuntimeMonitor) registrationFailureLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	var lastCount int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := atomic.LoadInt64(&m.failedRegistrations)
			delta := current - lastCount
			lastCount = current

			if delta > 10 {
				m.logger.Warn("high rate of failed agent registrations",
					zap.Int64("failedInWindow", delta),
					zap.Int64("totalFailed", current),
				)
				m.addFinding(AuditFinding{
					CheckID:     "failed-registrations",
					Severity:    SeverityWarn,
					Description: fmt.Sprintf("%d failed registration attempts in the last 5 minutes", delta),
				})
			}
		}
	}
}

func (m *RuntimeMonitor) checkSymlinks() {
	usersDir := filepath.Join(nipperHomeDir(), "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		fullPath := filepath.Join(usersDir, entry.Name())
		info, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			m.logger.Error("symlink detected in user directory — removing",
				zap.String("path", fullPath),
			)
			if removeErr := os.Remove(fullPath); removeErr != nil {
				m.logger.Error("failed to remove symlink",
					zap.String("path", fullPath),
					zap.Error(removeErr),
				)
			}
			m.addFinding(AuditFinding{
				CheckID:     "runtime-symlink-detected",
				Severity:    SeverityCritical,
				Description: fmt.Sprintf("symlink detected and removed: %s", fullPath),
			})
		}
	}
}

func (m *RuntimeMonitor) checkConnectivity(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if m.datastore != nil {
		err := m.datastore.Ping(checkCtx)
		m.mu.Lock()
		m.datastoreOK = err == nil
		m.mu.Unlock()
		if err != nil {
			m.logger.Error("datastore health check failed — rejecting all new messages",
				zap.Error(err),
			)
		}
	}

	if m.queueChecker != nil {
		err := m.queueChecker.Ping(checkCtx)
		m.mu.Lock()
		m.queueOK = err == nil
		m.mu.Unlock()
		if err != nil {
			m.logger.Error("RabbitMQ health check failed — message processing halted",
				zap.Error(err),
			)
		}
	}
}

func (m *RuntimeMonitor) addFinding(f AuditFinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findings = append(m.findings, f)
	if len(m.findings) > 100 {
		m.findings = m.findings[len(m.findings)-100:]
	}
}
