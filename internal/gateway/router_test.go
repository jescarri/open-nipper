package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

// --- Mock channel adapter ---

type mockAdapter struct {
	ct        models.ChannelType
	msg       *models.NipperMessage
	normalErr error
	notified  int
}

func (m *mockAdapter) ChannelType() models.ChannelType { return m.ct }
func (m *mockAdapter) Start(context.Context) error      { return nil }
func (m *mockAdapter) Stop(context.Context) error       { return nil }
func (m *mockAdapter) HealthCheck(context.Context) error { return nil }

func (m *mockAdapter) NormalizeInbound(_ context.Context, _ []byte) (*models.NipperMessage, error) {
	return m.msg, m.normalErr
}

func (m *mockAdapter) DeliverResponse(_ context.Context, _ *models.NipperResponse) error { return nil }
func (m *mockAdapter) DeliverEvent(_ context.Context, _ *models.NipperEvent) error       { return nil }
func (m *mockAdapter) NotifyInbound(_ context.Context, _ *models.NipperMessage) {
	m.notified++
}

var _ channels.ChannelAdapter = (*mockAdapter)(nil)

// --- Mock allowlist checker ---

type mockGuard struct {
	allowed bool
	err     error
}

func (g *mockGuard) Check(_ context.Context, _, _, _ string) (bool, error) {
	return g.allowed, g.err
}

// --- Mock publisher ---

type mockPublisher struct {
	mu       sync.Mutex
	messages []*models.QueueItem
	controls []*models.ControlMessage
	pubErr   error
}

func (p *mockPublisher) PublishMessage(_ context.Context, item *models.QueueItem) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, item)
	return p.pubErr
}

func (p *mockPublisher) PublishControl(_ context.Context, _ string, msg *models.ControlMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.controls = append(p.controls, msg)
	return p.pubErr
}

func (p *mockPublisher) Close() error { return nil }

// --- Mock repo (only ResolveIdentity is used by the router) ---

type mockRepo struct {
	resolvedUserID string
	resolveErr     error
	identities     map[string]string
}

func (r *mockRepo) ResolveIdentity(_ context.Context, _, _ string) (string, error) {
	// key format: channelType|identity
	if len(r.identities) > 0 {
		// This mock method is used in tests that pass a fixed resolvedUserID.
		// Keep map-backed behavior optional to avoid changing existing tests.
	}
	return r.resolvedUserID, r.resolveErr
}

// All other Repository methods are stubs to satisfy the interface.
func (r *mockRepo) CreateUser(context.Context, models.CreateUserRequest) (*models.User, error) {
	return nil, nil
}
func (r *mockRepo) GetUser(context.Context, string) (*models.User, error) { return nil, nil }
func (r *mockRepo) UpdateUser(context.Context, string, models.UpdateUserRequest) (*models.User, error) {
	return nil, nil
}
func (r *mockRepo) DeleteUser(context.Context, string) error                { return nil }
func (r *mockRepo) ListUsers(context.Context) ([]*models.User, error)      { return nil, nil }
func (r *mockRepo) IsUserEnabled(context.Context, string) (bool, error)    { return true, nil }
func (r *mockRepo) AddIdentity(_ context.Context, userID, channelType, channelIdentity string) error {
	if r.identities == nil {
		r.identities = make(map[string]string)
	}
	r.identities[channelType+"|"+channelIdentity] = userID
	return nil
}
func (r *mockRepo) RemoveIdentity(context.Context, int64) error            { return nil }
func (r *mockRepo) ListIdentities(context.Context, string) ([]*models.Identity, error) {
	return nil, nil
}
func (r *mockRepo) IsAllowed(context.Context, string, string) (bool, error) { return true, nil }
func (r *mockRepo) SetAllowed(context.Context, string, string, bool, string) error {
	return nil
}
func (r *mockRepo) RemoveAllowed(context.Context, string, string) error { return nil }
func (r *mockRepo) ListAllowed(context.Context, string) ([]*models.AllowlistEntry, error) {
	return nil, nil
}
func (r *mockRepo) GetUserPolicy(context.Context, string, string) (*models.PolicyData, error) {
	return nil, nil
}
func (r *mockRepo) SetUserPolicy(context.Context, string, string, *models.PolicyData) error {
	return nil
}
func (r *mockRepo) DeleteUserPolicy(context.Context, string, string) error { return nil }
func (r *mockRepo) ListUserPolicies(context.Context, string) ([]*models.UserPolicy, error) {
	return nil, nil
}
func (r *mockRepo) ProvisionAgent(context.Context, models.ProvisionAgentRequest) (*models.Agent, error) {
	return nil, nil
}
func (r *mockRepo) GetAgent(context.Context, string) (*models.Agent, error) { return nil, nil }
func (r *mockRepo) GetAgentByTokenHash(context.Context, string) (*models.Agent, error) {
	return nil, nil
}
func (r *mockRepo) ListAgents(context.Context, string) ([]*models.Agent, error) { return nil, nil }
func (r *mockRepo) UpdateAgentStatus(context.Context, string, models.AgentStatus, *models.AgentRegistrationMeta) error {
	return nil
}
func (r *mockRepo) SetAgentRMQUsername(context.Context, string, string) error { return nil }
func (r *mockRepo) RotateAgentToken(context.Context, string, string, string) error {
	return nil
}
func (r *mockRepo) RevokeAgent(context.Context, string) error              { return nil }
func (r *mockRepo) DeleteAgent(context.Context, string) error              { return nil }
func (r *mockRepo) LogAdminAction(context.Context, models.AdminAuditEntry) error { return nil }
func (r *mockRepo) QueryAuditLog(context.Context, models.AuditQueryFilters) ([]*models.AdminAuditEntry, error) {
	return nil, nil
}
func (r *mockRepo) ListCronJobs(context.Context) ([]config.CronJob, error)              { return nil, nil }
func (r *mockRepo) ListCronJobsByUser(context.Context, string) ([]config.CronJob, error) { return nil, nil }
func (r *mockRepo) AddCronJob(context.Context, config.CronJob) error                     { return nil }
func (r *mockRepo) RemoveCronJob(context.Context, string, string) error                  { return nil }
func (r *mockRepo) ListAtJobs(context.Context) ([]config.AtJob, error)                   { return nil, nil }
func (r *mockRepo) ListAtJobsByUser(context.Context, string) ([]config.AtJob, error)     { return nil, nil }
func (r *mockRepo) AddAtJob(context.Context, config.AtJob) error                         { return nil }
func (r *mockRepo) RemoveAtJob(context.Context, string, string) error                    { return nil }
func (r *mockRepo) Backup(context.Context, string) error { return nil }
func (r *mockRepo) Close() error                          { return nil }
func (r *mockRepo) Ping(context.Context) error            { return nil }

// --- Helpers ---

func newTestRouter(guard AllowlistChecker, pub *mockPublisher, repo *mockRepo, cfg *config.Config) *Router {
	logger := zap.NewNop()
	return NewRouter(RouterDeps{
		Logger:    logger,
		Repo:      repo,
		Guard:     guard,
		Resolver:  NewResolver(logger, "test-model"),
		Registry:  NewRegistry(),
		Publisher: pub,
		Dedup:     NewDeduplicator(30 * time.Second),
		Config:    cfg,
	})
}

func newWhatsAppMsg() *models.NipperMessage {
	return &models.NipperMessage{
		MessageID:       "msg-001",
		OriginMessageID: "wa-origin-001",
		UserID:          "user-01",
		ChannelType:     models.ChannelTypeWhatsApp,
		ChannelIdentity: "1555010001@s.whatsapp.net",
		Content:         models.MessageContent{Text: "hello"},
		Meta:            models.WhatsAppMeta{ChatJID: "1555010001@s.whatsapp.net", SenderJID: "1555010001@s.whatsapp.net"},
		DeliveryContext: models.DeliveryContext{
			ChannelType:  models.ChannelTypeWhatsApp,
			ChannelID:    "1555010001@s.whatsapp.net",
			Capabilities: models.WhatsAppCapabilities(),
		},
	}
}

// --- Tests ---

func TestRouter_HappyPath(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	repo := &mockRepo{resolvedUserID: "user-01"}
	r := newTestRouter(guard, pub, repo, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}
	ctx := context.Background()

	if err := r.HandleMessage(ctx, []byte("{}"), adapter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(pub.messages))
	}
	item := pub.messages[0]
	if item.Message.SessionKey == "" {
		t.Fatal("session key should be populated")
	}
	if item.Message.UserID != "user-01" {
		t.Fatalf("expected userId user-01, got %s", item.Message.UserID)
	}
	if adapter.notified != 1 {
		t.Fatalf("expected inbound notifier to be called once, got %d", adapter.notified)
	}
}

func TestRouter_DoesNotNotifyInboundWhenRejected(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: false}
	repo := &mockRepo{resolvedUserID: "user-01"}
	r := newTestRouter(guard, pub, repo, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}

	if err := r.HandleMessage(context.Background(), []byte("{}"), adapter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.notified != 0 {
		t.Fatalf("expected no inbound notify on rejected message, got %d", adapter.notified)
	}
}

func TestRouter_NilMessageIgnored(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: nil}

	if err := r.HandleMessage(context.Background(), []byte("{}"), adapter); err != nil {
		t.Fatalf("nil message should not produce error: %v", err)
	}
	if len(pub.messages) != 0 {
		t.Fatal("no message should be published for nil")
	}
}

func TestRouter_NormalizationError(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, normalErr: errors.New("bad payload")}

	err := r.HandleMessage(context.Background(), []byte("{}"), adapter)
	if err == nil {
		t.Fatal("expected error from normalization failure")
	}
}

func TestRouter_AllowlistReject(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: false}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}

	if err := r.HandleMessage(context.Background(), []byte("{}"), adapter); err != nil {
		t.Fatalf("allowlist reject should not error: %v", err)
	}
	if len(pub.messages) != 0 {
		t.Fatal("rejected message should not be published")
	}
}

func TestRouter_DeduplicationSuppresses(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	msg := newWhatsAppMsg()
	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: msg}

	r.HandleMessage(context.Background(), []byte("{}"), adapter)
	r.HandleMessage(context.Background(), []byte("{}"), adapter)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) != 1 {
		t.Fatalf("duplicate should be suppressed, expected 1, got %d", len(pub.messages))
	}
}

func TestRouter_PublishError(t *testing.T) {
	pub := &mockPublisher{pubErr: errors.New("broker down")}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}
	err := r.HandleMessage(context.Background(), []byte("{}"), adapter)
	if err == nil {
		t.Fatal("expected publish error to propagate")
	}
}

func TestRouter_InterruptModeSendsControl(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	cfg := &config.Config{
		Queue: config.QueueConfig{
			PerChannel: map[string]config.ChannelQueueMode{
				"whatsapp": {Mode: "interrupt", Priority: 10},
			},
		},
	}
	r := newTestRouter(guard, pub, &mockRepo{}, cfg)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}

	if err := r.HandleMessage(context.Background(), []byte("{}"), adapter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pub.messages))
	}
	if len(pub.controls) != 1 {
		t.Fatalf("expected 1 control, got %d", len(pub.controls))
	}
	if pub.controls[0].Type != models.ControlMessageInterrupt {
		t.Fatalf("expected interrupt, got %s", pub.controls[0].Type)
	}
}

func TestRouter_DefaultQueueMode(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	cfg := &config.Config{
		Queue: config.QueueConfig{DefaultMode: "steer"},
	}
	r := newTestRouter(guard, pub, &mockRepo{}, cfg)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeSlack, msg: func() *models.NipperMessage {
		m := newWhatsAppMsg()
		m.ChannelType = models.ChannelTypeSlack
		m.OriginMessageID = "slack-origin-001"
		m.Meta = models.SlackMeta{ChannelID: "C123"}
		return m
	}()}

	if err := r.HandleMessage(context.Background(), []byte("{}"), adapter); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) != 1 {
		t.Fatalf("expected 1, got %d", len(pub.messages))
	}
	if pub.messages[0].Mode != models.QueueModeSteer {
		t.Fatalf("expected steer mode, got %s", pub.messages[0].Mode)
	}
}

func TestRouter_SessionKeyAndRegistryPopulated(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}

	r.HandleMessage(context.Background(), []byte("{}"), adapter)

	pub.mu.Lock()
	key := pub.messages[0].Message.SessionKey
	pub.mu.Unlock()

	if key == "" {
		t.Fatal("session key should not be empty")
	}
	if _, _, _, ok := r.registry.Lookup(key); !ok {
		t.Fatal("delivery context should be registered")
	}
}

func TestRouter_UserResolution(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	repo := &mockRepo{resolvedUserID: "resolved-user"}
	r := newTestRouter(guard, pub, repo, nil)
	defer r.dedup.Stop()

	msg := newWhatsAppMsg()
	msg.UserID = "" // force resolution
	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: msg}

	r.HandleMessage(context.Background(), []byte("{}"), adapter)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) == 1 {
		if pub.messages[0].Message.UserID != "resolved-user" {
			t.Fatalf("expected resolved-user, got %s", pub.messages[0].Message.UserID)
		}
	}
}

func TestRouter_CollectModeBatching(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	cfg := &config.Config{
		Queue: config.QueueConfig{
			PerChannel: map[string]config.ChannelQueueMode{
				"whatsapp": {Mode: "collect", CollectCap: 3, DebounceMS: 5000},
			},
		},
	}
	r := newTestRouter(guard, pub, &mockRepo{}, cfg)
	defer r.dedup.Stop()

	for i := 0; i < 3; i++ {
		msg := newWhatsAppMsg()
		msg.OriginMessageID = "" // disable dedup
		msg.Content.Text = ""
		msg.MessageID = ""
		adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: msg}
		r.HandleMessage(context.Background(), []byte("{}"), adapter)
	}

	// Cap reached should trigger flush — give it a moment.
	time.Sleep(200 * time.Millisecond)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.messages) != 1 {
		t.Fatalf("expected 1 collected batch, got %d", len(pub.messages))
	}
	item := pub.messages[0]
	if len(item.CollectedMessages) != 3 {
		t.Fatalf("expected 3 collected msgs, got %d", len(item.CollectedMessages))
	}
}

func TestRouter_QueueItemSerialisable(t *testing.T) {
	pub := &mockPublisher{}
	guard := &mockGuard{allowed: true}
	r := newTestRouter(guard, pub, &mockRepo{}, nil)
	defer r.dedup.Stop()

	adapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: newWhatsAppMsg()}
	r.HandleMessage(context.Background(), []byte("{}"), adapter)

	pub.mu.Lock()
	defer pub.mu.Unlock()

	data, err := json.Marshal(pub.messages[0])
	if err != nil {
		t.Fatalf("QueueItem should be JSON-serialisable: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("serialised data should not be empty")
	}
}
