package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/queue"
	"github.com/jescarri/open-nipper/internal/telemetry"
)

// --- Mock repo for health monitor tests ---

type hmMockRepo struct {
	mockRepo

	users    []*models.User
	usersErr error

	agentsByUser map[string][]*models.Agent
	agentsErr    error
}

func (r *hmMockRepo) ListUsers(_ context.Context) ([]*models.User, error) {
	return r.users, r.usersErr
}

func (r *hmMockRepo) ListAgents(_ context.Context, userID string) ([]*models.Agent, error) {
	if r.agentsErr != nil {
		return nil, r.agentsErr
	}
	return r.agentsByUser[userID], nil
}

// --- Mock Management Client for health monitor tests ---

type hmMockMgmt struct {
	queueInfo    map[string]*queue.QueueInfo // keyed by queueName
	queueInfoErr error
}

func (m *hmMockMgmt) CreateUser(context.Context, string, string) error          { return nil }
func (m *hmMockMgmt) DeleteUser(context.Context, string) error                  { return nil }
func (m *hmMockMgmt) SetVhostPermissions(context.Context, string, string, queue.VhostPermissions) error {
	return nil
}
func (m *hmMockMgmt) ListQueues(context.Context, string) ([]*queue.QueueInfo, error) {
	return nil, nil
}
func (m *hmMockMgmt) GetQueueInfo(_ context.Context, _, queueName string) (*queue.QueueInfo, error) {
	if m.queueInfoErr != nil {
		return nil, m.queueInfoErr
	}
	info, ok := m.queueInfo[queueName]
	if !ok {
		return nil, fmt.Errorf("queue %q not found", queueName)
	}
	return info, nil
}

// --- Helper to build a HealthMonitor without starting the background loop ---

func newTestHealthMonitor(repo *hmMockRepo, mgmt *hmMockMgmt, agentsCfg *config.AgentsConfig, queueCfg *config.QueueRabbitMQConfig) *HealthMonitor {
	return NewHealthMonitor(HealthMonitorDeps{
		Repo:     repo,
		Mgmt:     mgmt,
		Config:   agentsCfg,
		QueueCfg: queueCfg,
		Metrics:  nil,
		Logger:   zap.NewNop(),
	})
}

// --- DeriveAgentStatus tests ---

func TestDeriveAgentStatus_Processing(t *testing.T) {
	if s := DeriveAgentStatus(1, 0, 1); s != "processing" {
		t.Fatalf("expected processing, got %s", s)
	}
}

func TestDeriveAgentStatus_Idle(t *testing.T) {
	if s := DeriveAgentStatus(1, 0, 0); s != "idle" {
		t.Fatalf("expected idle, got %s", s)
	}
}

func TestDeriveAgentStatus_IdleWithReady(t *testing.T) {
	if s := DeriveAgentStatus(2, 3, 0); s != "idle" {
		t.Fatalf("expected idle, got %s", s)
	}
}

func TestDeriveAgentStatus_Degraded(t *testing.T) {
	if s := DeriveAgentStatus(0, 5, 0); s != "degraded" {
		t.Fatalf("expected degraded, got %s", s)
	}
}

func TestDeriveAgentStatus_Offline(t *testing.T) {
	if s := DeriveAgentStatus(0, 0, 0); s != "offline" {
		t.Fatalf("expected offline, got %s", s)
	}
}

// --- CheckAll tests ---

func TestCheckAll_HappyPath_Idle(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01", Name: "Alice"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Name: "nipper-agent-user-01", Consumers: 1, MessagesReady: 0, MessagesUnacknowledged: 0},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, &config.QueueRabbitMQConfig{VHost: "/nipper"})
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil {
		t.Fatal("expected status for user-01")
	}
	if s.Status != "idle" {
		t.Fatalf("expected idle, got %s", s.Status)
	}
	if s.QueueName != "nipper-agent-user-01" {
		t.Fatalf("unexpected queue name: %s", s.QueueName)
	}
	if s.ConsumerCount != 1 {
		t.Fatalf("expected 1 consumer, got %d", s.ConsumerCount)
	}
}

func TestCheckAll_Processing(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1, MessagesReady: 0, MessagesUnacknowledged: 2},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "processing" {
		t.Fatalf("expected processing, got %v", s)
	}
}

func TestCheckAll_Degraded(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusProvisioned}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 0, MessagesReady: 3, MessagesUnacknowledged: 0},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "degraded" {
		t.Fatalf("expected degraded, got %v", s)
	}
	if s.DegradedSince == nil {
		t.Fatal("degradedSince should be set")
	}
}

func TestCheckAll_Offline(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 0, MessagesReady: 0, MessagesUnacknowledged: 0},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "offline" {
		t.Fatalf("expected offline, got %v", s)
	}
}

func TestCheckAll_SkipsRevokedAgents(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRevoked}},
		},
	}
	mgmt := &hmMockMgmt{queueInfo: map[string]*queue.QueueInfo{}}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if s := hm.GetStatus("user-01"); s != nil {
		t.Fatalf("expected no status for user with only revoked agents, got %v", s)
	}
}

func TestCheckAll_MultipleUsers(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "alice"}, {ID: "bob"}},
		agentsByUser: map[string][]*models.Agent{
			"alice": {{ID: "agt-a", UserID: "alice", Status: models.AgentStatusRegistered}},
			"bob":   {{ID: "agt-b", UserID: "bob", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-alice": {Consumers: 1, MessagesReady: 0, MessagesUnacknowledged: 0},
			"nipper-agent-bob":   {Consumers: 0, MessagesReady: 2, MessagesUnacknowledged: 0},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	all := hm.GetAllStatuses()
	if len(all) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(all))
	}

	alice := hm.GetStatus("alice")
	bob := hm.GetStatus("bob")
	if alice == nil || alice.Status != "idle" {
		t.Fatalf("alice: expected idle, got %v", alice)
	}
	if bob == nil || bob.Status != "degraded" {
		t.Fatalf("bob: expected degraded, got %v", bob)
	}
}

func TestCheckAll_MgmtError_MarksOffline(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{queueInfoErr: fmt.Errorf("connection refused")}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "offline" {
		t.Fatalf("expected offline on mgmt error, got %v", s)
	}
}

func TestCheckAll_ListUsersError(t *testing.T) {
	repo := &hmMockRepo{usersErr: fmt.Errorf("db error")}
	mgmt := &hmMockMgmt{}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	all := hm.GetAllStatuses()
	if len(all) != 0 {
		t.Fatalf("expected 0 statuses on user list error, got %d", len(all))
	}
}

func TestCheckAll_ListAgentsError(t *testing.T) {
	repo := &hmMockRepo{
		users:    []*models.User{{ID: "user-01"}},
		agentsErr: fmt.Errorf("agents db error"),
	}
	mgmt := &hmMockMgmt{}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if s := hm.GetStatus("user-01"); s != nil {
		t.Fatalf("expected no status on agents list error, got %v", s)
	}
}

func TestCheckAll_NoUsersNoStatuses(t *testing.T) {
	repo := &hmMockRepo{users: []*models.User{}}
	mgmt := &hmMockMgmt{}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if len(hm.GetAllStatuses()) != 0 {
		t.Fatal("expected 0 statuses for empty user list")
	}
}

func TestCheckAll_UsersWithNoAgents(t *testing.T) {
	repo := &hmMockRepo{
		users:        []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{},
	}
	mgmt := &hmMockMgmt{}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if s := hm.GetStatus("user-01"); s != nil {
		t.Fatalf("expected no status for user without agents, got %v", s)
	}
}

// --- Degraded timeout detection ---

func TestCheckAll_DegradedPersists(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 0, MessagesReady: 5},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)

	hm.CheckAll()
	s1 := hm.GetStatus("user-01")
	if s1 == nil || s1.DegradedSince == nil {
		t.Fatal("first check: expected degradedSince to be set")
	}
	firstDegraded := *s1.DegradedSince

	hm.CheckAll()
	s2 := hm.GetStatus("user-01")
	if s2 == nil || s2.DegradedSince == nil {
		t.Fatal("second check: expected degradedSince to persist")
	}
	if !s2.DegradedSince.Equal(firstDegraded) {
		t.Fatalf("degradedSince should persist, got %v vs original %v", *s2.DegradedSince, firstDegraded)
	}
}

func TestCheckAll_DegradedResetsOnRecovery(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 0, MessagesReady: 5},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)

	hm.CheckAll()
	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "degraded" {
		t.Fatal("expected degraded")
	}

	// Agent reconnects
	mgmt.queueInfo["nipper-agent-user-01"] = &queue.QueueInfo{Consumers: 1, MessagesReady: 5, MessagesUnacknowledged: 1}
	hm.CheckAll()

	s = hm.GetStatus("user-01")
	if s == nil || s.Status != "processing" {
		t.Fatalf("expected processing after recovery, got %v", s)
	}
	if s.DegradedSince != nil {
		t.Fatal("degradedSince should be nil after recovery")
	}
}

// --- Stale entry removal ---

func TestCheckAll_RemovesStaleEntries(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if len(hm.GetAllStatuses()) != 1 {
		t.Fatal("expected 1 status")
	}

	// User removed from repo
	repo.users = []*models.User{}
	hm.CheckAll()

	if len(hm.GetAllStatuses()) != 0 {
		t.Fatal("expected 0 statuses after user removal")
	}
}

// --- Config respected ---

func TestCheckAll_UsesConfiguredVHost(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}

	customVHost := "/custom-vhost"
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, &config.QueueRabbitMQConfig{VHost: customVHost})
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "idle" {
		t.Fatalf("expected idle with custom vhost, got %v", s)
	}
}

// --- GetStatus returns copy ---

func TestGetStatus_ReturnsCopy(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s1 := hm.GetStatus("user-01")
	s2 := hm.GetStatus("user-01")

	s1.Status = "mutated"
	if s2.Status == "mutated" {
		t.Fatal("GetStatus should return independent copies")
	}
}

func TestGetStatus_NilForUnknownUser(t *testing.T) {
	hm := newTestHealthMonitor(&hmMockRepo{}, &hmMockMgmt{}, nil, nil)
	if s := hm.GetStatus("no-such-user"); s != nil {
		t.Fatalf("expected nil for unknown user, got %v", s)
	}
}

// --- GetAllStatuses ---

func TestGetAllStatuses_Empty(t *testing.T) {
	hm := newTestHealthMonitor(&hmMockRepo{}, &hmMockMgmt{}, nil, nil)
	all := hm.GetAllStatuses()
	if len(all) != 0 {
		t.Fatalf("expected empty, got %d", len(all))
	}
}

func TestGetAllStatuses_PopulatesAllFields(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Name: "nipper-agent-user-01", Consumers: 2, MessagesReady: 3},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	all := hm.GetAllStatuses()
	if len(all) != 1 {
		t.Fatalf("expected 1, got %d", len(all))
	}
	info := all[0]
	if info.UserID != "user-01" {
		t.Fatalf("unexpected user ID: %s", info.UserID)
	}
	if info.Queue != "nipper-agent-user-01" {
		t.Fatalf("unexpected queue: %s", info.Queue)
	}
	if info.ConsumerCount != 2 {
		t.Fatalf("expected 2 consumers, got %d", info.ConsumerCount)
	}
	if info.MessagesReady != 3 {
		t.Fatalf("expected 3 ready, got %d", info.MessagesReady)
	}
	if info.Status != "idle" {
		t.Fatalf("expected idle, got %s", info.Status)
	}
}

// --- Metrics ---

func TestCheckAll_UpdatesMetrics(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "alice"}, {ID: "bob"}},
		agentsByUser: map[string][]*models.Agent{
			"alice": {{ID: "agt-a", UserID: "alice", Status: models.AgentStatusRegistered}},
			"bob":   {{ID: "agt-b", UserID: "bob", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-alice": {Consumers: 2},
			"nipper-agent-bob":   {Consumers: 1},
		},
	}
	metrics := &telemetry.Metrics{}
	hm := NewHealthMonitor(HealthMonitorDeps{
		Repo:    repo,
		Mgmt:    mgmt,
		Metrics: metrics,
		Logger:  zap.NewNop(),
	})
	hm.CheckAll()

	if metrics.AgentConsumerCount.Load() != 3 {
		t.Fatalf("expected total consumer count 3, got %d", metrics.AgentConsumerCount.Load())
	}
}

// --- Start/Stop ---

func TestStartStop(t *testing.T) {
	repo := &hmMockRepo{users: []*models.User{}}
	mgmt := &hmMockMgmt{}
	agentsCfg := &config.AgentsConfig{HealthCheckIntervalSeconds: 1}
	hm := newTestHealthMonitor(repo, mgmt, agentsCfg, nil)

	hm.Start()
	time.Sleep(100 * time.Millisecond)
	hm.Stop()
}

func TestStartStop_WithInterval(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	agentsCfg := &config.AgentsConfig{HealthCheckIntervalSeconds: 1}
	hm := newTestHealthMonitor(repo, mgmt, agentsCfg, nil)

	hm.Start()
	time.Sleep(150 * time.Millisecond)

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "idle" {
		t.Fatalf("expected idle after start, got %v", s)
	}

	hm.Stop()
}

// --- Mixed agent statuses ---

func TestCheckAll_MixedAgentStatuses(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {
				{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRevoked},
				{ID: "agt-02", UserID: "user-01", Status: models.AgentStatusRegistered},
			},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "idle" {
		t.Fatalf("expected idle (has one registered agent), got %v", s)
	}
}

func TestCheckAll_AllAgentsRevoked(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {
				{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRevoked},
				{ID: "agt-02", UserID: "user-01", Status: models.AgentStatusRevoked},
			},
		},
	}
	mgmt := &hmMockMgmt{}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	if s := hm.GetStatus("user-01"); s != nil {
		t.Fatalf("expected no status when all agents revoked, got %v", s)
	}
}

// --- Default vhost ---

func TestCheckAll_DefaultVHost(t *testing.T) {
	repo := &hmMockRepo{
		users: []*models.User{{ID: "user-01"}},
		agentsByUser: map[string][]*models.Agent{
			"user-01": {{ID: "agt-01", UserID: "user-01", Status: models.AgentStatusRegistered}},
		},
	}
	mgmt := &hmMockMgmt{
		queueInfo: map[string]*queue.QueueInfo{
			"nipper-agent-user-01": {Consumers: 1},
		},
	}
	hm := newTestHealthMonitor(repo, mgmt, nil, nil)
	hm.CheckAll()

	s := hm.GetStatus("user-01")
	if s == nil || s.Status != "idle" {
		t.Fatalf("expected idle with default vhost, got %v", s)
	}
}
