package gateway

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/datastore"
	"github.com/open-nipper/open-nipper/internal/models"
	"github.com/open-nipper/open-nipper/internal/queue"
	"github.com/open-nipper/open-nipper/internal/telemetry"
)

// AgentHealthStatus tracks the health of a single user's agent queue.
type AgentHealthStatus struct {
	UserID                 string
	QueueName              string
	ConsumerCount          int
	MessagesReady          int
	MessagesUnacknowledged int
	Status                 string // "processing", "idle", "degraded", "offline"
	DegradedSince          *time.Time
	LastChecked            time.Time
}

// HealthMonitor periodically checks agent queue health via the RabbitMQ
// Management API and caches the results in memory. The admin health endpoint
// and WebSocket agents.status method read from this cache.
type HealthMonitor struct {
	repo     datastore.Repository
	mgmt     queue.ManagementClient
	cfg      *config.AgentsConfig
	queueCfg *config.QueueRabbitMQConfig
	metrics  *telemetry.Metrics
	logger   *zap.Logger

	mu       sync.RWMutex
	statuses map[string]*AgentHealthStatus

	stopCh chan struct{}
	done   chan struct{}
}

// HealthMonitorDeps bundles the dependencies for the health monitor.
type HealthMonitorDeps struct {
	Repo     datastore.Repository
	Mgmt     queue.ManagementClient
	Config   *config.AgentsConfig
	QueueCfg *config.QueueRabbitMQConfig
	Metrics  *telemetry.Metrics
	Logger   *zap.Logger
}

// NewHealthMonitor creates a new HealthMonitor. Call Start to begin the
// background check loop.
func NewHealthMonitor(deps HealthMonitorDeps) *HealthMonitor {
	return &HealthMonitor{
		repo:     deps.Repo,
		mgmt:     deps.Mgmt,
		cfg:      deps.Config,
		queueCfg: deps.QueueCfg,
		metrics:  deps.Metrics,
		logger:   deps.Logger,
		statuses: make(map[string]*AgentHealthStatus),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins the background health check loop.
func (hm *HealthMonitor) Start() {
	interval := 30 * time.Second
	if hm.cfg != nil && hm.cfg.HealthCheckIntervalSeconds > 0 {
		interval = time.Duration(hm.cfg.HealthCheckIntervalSeconds) * time.Second
	}

	go hm.loop(interval)

	hm.logger.Info("agent health monitor started",
		zap.Duration("interval", interval),
	)
}

// Stop signals the background goroutine to stop and waits for it to finish.
func (hm *HealthMonitor) Stop() {
	close(hm.stopCh)
	<-hm.done
	hm.logger.Info("agent health monitor stopped")
}

// GetStatus returns the cached health status for a specific user, or nil.
func (hm *HealthMonitor) GetStatus(userID string) *AgentHealthStatus {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	s, ok := hm.statuses[userID]
	if !ok {
		return nil
	}
	cp := *s
	return &cp
}

// GetAllStatuses returns a snapshot of all tracked agent health statuses
// as models.AgentHealthInfo values suitable for JSON serialization.
func (hm *HealthMonitor) GetAllStatuses() []models.AgentHealthInfo {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	result := make([]models.AgentHealthInfo, 0, len(hm.statuses))
	for _, s := range hm.statuses {
		result = append(result, models.AgentHealthInfo{
			UserID:        s.UserID,
			Queue:         s.QueueName,
			ConsumerCount: s.ConsumerCount,
			MessagesReady: s.MessagesReady,
			Status:        s.Status,
		})
	}
	return result
}

func (hm *HealthMonitor) loop(interval time.Duration) {
	defer close(hm.done)

	hm.checkAll()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hm.checkAll()
		case <-hm.stopCh:
			return
		}
	}
}

// CheckAll runs a single health check cycle. Exported for testing.
func (hm *HealthMonitor) CheckAll() {
	hm.checkAll()
}

func (hm *HealthMonitor) checkAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	users, err := hm.repo.ListUsers(ctx)
	if err != nil {
		hm.logger.Error("health monitor: failed to list users", zap.Error(err))
		return
	}

	var totalConsumers int64
	seen := make(map[string]bool, len(users))

	for _, user := range users {
		agents, err := hm.repo.ListAgents(ctx, user.ID)
		if err != nil {
			hm.logger.Error("health monitor: failed to list agents",
				zap.String("userId", user.ID),
				zap.Error(err),
			)
			continue
		}

		hasActiveAgent := false
		for _, agent := range agents {
			if agent.Status != models.AgentStatusRevoked {
				hasActiveAgent = true
				break
			}
		}
		if !hasActiveAgent {
			continue
		}

		seen[user.ID] = true

		queueName := queue.UserAgentQueue(user.ID)
		vhost := "/nipper"
		if hm.queueCfg != nil && hm.queueCfg.VHost != "" {
			vhost = hm.queueCfg.VHost
		}

		info, err := hm.mgmt.GetQueueInfo(ctx, vhost, queueName)
		if err != nil {
			hm.logger.Warn("health monitor: failed to get queue info",
				zap.String("userId", user.ID),
				zap.String("queue", queueName),
				zap.Error(err),
			)
			hm.updateStatus(user.ID, queueName, 0, 0, 0)
			continue
		}

		hm.updateStatus(user.ID, queueName, info.Consumers, info.MessagesReady, info.MessagesUnacknowledged)
		totalConsumers += int64(info.Consumers)
	}

	// Remove stale entries for users who no longer have active agents.
	hm.mu.Lock()
	for uid := range hm.statuses {
		if !seen[uid] {
			delete(hm.statuses, uid)
		}
	}
	agentCount := int64(len(hm.statuses))
	hm.mu.Unlock()

	if hm.metrics != nil {
		hm.metrics.AgentConsumerCount.Set(totalConsumers)
		hm.metrics.AgentCount.Set(agentCount)
	}
}

func (hm *HealthMonitor) updateStatus(userID, queueName string, consumers, ready, unacked int) {
	status := DeriveAgentStatus(consumers, ready, unacked)

	hm.mu.Lock()
	defer hm.mu.Unlock()

	existing := hm.statuses[userID]
	now := time.Now()

	entry := &AgentHealthStatus{
		UserID:                 userID,
		QueueName:              queueName,
		ConsumerCount:          consumers,
		MessagesReady:          ready,
		MessagesUnacknowledged: unacked,
		Status:                 status,
		LastChecked:            now,
	}

	if status == "degraded" {
		if existing != nil && existing.Status == "degraded" && existing.DegradedSince != nil {
			entry.DegradedSince = existing.DegradedSince

			timeout := 60 * time.Second
			if hm.cfg != nil && hm.cfg.ConsumerTimeoutSeconds > 0 {
				timeout = time.Duration(hm.cfg.ConsumerTimeoutSeconds) * time.Second
			}

			if now.Sub(*existing.DegradedSince) > timeout {
				hm.logger.Warn("agent degraded beyond timeout",
					zap.String("userId", userID),
					zap.String("queue", queueName),
					zap.Int("messagesReady", ready),
					zap.Duration("degradedFor", now.Sub(*existing.DegradedSince)),
				)
			}
		} else {
			entry.DegradedSince = &now
		}
	}

	hm.statuses[userID] = entry
}

// DeriveAgentStatus computes the status string from queue metrics.
// Exported so tests and other packages can use the same logic.
func DeriveAgentStatus(consumers, messagesReady, messagesUnacked int) string {
	switch {
	case consumers > 0 && messagesUnacked > 0:
		return "processing"
	case consumers > 0:
		return "idle"
	case consumers == 0 && messagesReady > 0:
		return "degraded"
	default:
		return "offline"
	}
}
