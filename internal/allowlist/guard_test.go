package allowlist_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/open-nipper/open-nipper/internal/allowlist"
	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// ---- minimal stub repository -----------------------------------------------

type stubRepo struct {
	users           map[string]bool            // userId -> enabled
	allowed         map[string]map[string]bool // userId -> channelType -> allowed
	audit           []models.AdminAuditEntry

	errIsUserEnabled error
	errIsAllowed     error
	errLogAudit      error
}

func newStub() *stubRepo {
	return &stubRepo{
		users:   make(map[string]bool),
		allowed: make(map[string]map[string]bool),
	}
}

func (r *stubRepo) setEnabled(id string, v bool) { r.users[id] = v }
func (r *stubRepo) setAllowed(id, ch string, v bool) {
	if r.allowed[id] == nil {
		r.allowed[id] = make(map[string]bool)
	}
	r.allowed[id][ch] = v
}

// --- Repository interface ---

func (r *stubRepo) IsUserEnabled(_ context.Context, id string) (bool, error) {
	if r.errIsUserEnabled != nil {
		return false, r.errIsUserEnabled
	}
	return r.users[id], nil
}
func (r *stubRepo) IsAllowed(_ context.Context, id, ch string) (bool, error) {
	if r.errIsAllowed != nil {
		return false, r.errIsAllowed
	}
	m := r.allowed[id]
	if m == nil {
		return false, nil
	}
	return m[ch], nil
}
func (r *stubRepo) LogAdminAction(_ context.Context, e models.AdminAuditEntry) error {
	if r.errLogAudit != nil {
		return r.errLogAudit
	}
	r.audit = append(r.audit, e)
	return nil
}

// No-op stubs for all other Repository methods.
func (r *stubRepo) CreateUser(_ context.Context, _ models.CreateUserRequest) (*models.User, error) {
	return nil, nil
}
func (r *stubRepo) GetUser(_ context.Context, _ string) (*models.User, error)  { return nil, nil }
func (r *stubRepo) UpdateUser(_ context.Context, _ string, _ models.UpdateUserRequest) (*models.User, error) {
	return nil, nil
}
func (r *stubRepo) DeleteUser(_ context.Context, _ string) error                       { return nil }
func (r *stubRepo) ListUsers(_ context.Context) ([]*models.User, error)                { return nil, nil }
func (r *stubRepo) AddIdentity(_ context.Context, _, _, _ string) error                { return nil }
func (r *stubRepo) RemoveIdentity(_ context.Context, _ int64) error                    { return nil }
func (r *stubRepo) ListIdentities(_ context.Context, _ string) ([]*models.Identity, error) {
	return nil, nil
}
func (r *stubRepo) ResolveIdentity(_ context.Context, _, _ string) (string, error) { return "", nil }
func (r *stubRepo) SetAllowed(_ context.Context, _, _ string, _ bool, _ string) error {
	return nil
}
func (r *stubRepo) RemoveAllowed(_ context.Context, _, _ string) error { return nil }
func (r *stubRepo) ListAllowed(_ context.Context, _ string) ([]*models.AllowlistEntry, error) {
	return nil, nil
}
func (r *stubRepo) GetUserPolicy(_ context.Context, _, _ string) (*models.PolicyData, error) {
	return nil, nil
}
func (r *stubRepo) SetUserPolicy(_ context.Context, _, _ string, _ *models.PolicyData) error {
	return nil
}
func (r *stubRepo) DeleteUserPolicy(_ context.Context, _, _ string) error { return nil }
func (r *stubRepo) ListUserPolicies(_ context.Context, _ string) ([]*models.UserPolicy, error) {
	return nil, nil
}
func (r *stubRepo) ProvisionAgent(_ context.Context, _ models.ProvisionAgentRequest) (*models.Agent, error) {
	return nil, nil
}
func (r *stubRepo) GetAgent(_ context.Context, _ string) (*models.Agent, error) { return nil, nil }
func (r *stubRepo) GetAgentByTokenHash(_ context.Context, _ string) (*models.Agent, error) {
	return nil, nil
}
func (r *stubRepo) ListAgents(_ context.Context, _ string) ([]*models.Agent, error) {
	return nil, nil
}
func (r *stubRepo) UpdateAgentStatus(_ context.Context, _ string, _ models.AgentStatus, _ *models.AgentRegistrationMeta) error {
	return nil
}
func (r *stubRepo) SetAgentRMQUsername(_ context.Context, _, _ string) error { return nil }
func (r *stubRepo) RotateAgentToken(_ context.Context, _, _, _ string) error { return nil }
func (r *stubRepo) RevokeAgent(_ context.Context, _ string) error            { return nil }
func (r *stubRepo) DeleteAgent(_ context.Context, _ string) error            { return nil }
func (r *stubRepo) ListCronJobs(_ context.Context) ([]config.CronJob, error) {
	return nil, nil
}
func (r *stubRepo) ListCronJobsByUser(_ context.Context, _ string) ([]config.CronJob, error) {
	return nil, nil
}
func (r *stubRepo) AddCronJob(_ context.Context, _ config.CronJob) error { return nil }
func (r *stubRepo) RemoveCronJob(_ context.Context, _, _ string) error   { return nil }
func (r *stubRepo) ListAtJobs(_ context.Context) ([]config.AtJob, error) {
	return nil, nil
}
func (r *stubRepo) ListAtJobsByUser(_ context.Context, _ string) ([]config.AtJob, error) {
	return nil, nil
}
func (r *stubRepo) AddAtJob(_ context.Context, _ config.AtJob) error { return nil }
func (r *stubRepo) RemoveAtJob(_ context.Context, _, _ string) error { return nil }
func (r *stubRepo) QueryAuditLog(_ context.Context, _ models.AuditQueryFilters) ([]*models.AdminAuditEntry, error) {
	return nil, nil
}
func (r *stubRepo) Backup(_ context.Context, _ string) error { return nil }
func (r *stubRepo) Close() error                             { return nil }
func (r *stubRepo) Ping(_ context.Context) error             { return nil }

// ---- helpers ----------------------------------------------------------------

func auditDetails(t *testing.T, entry models.AdminAuditEntry) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(entry.Details), &m); err != nil {
		t.Fatalf("failed to parse audit details: %v", err)
	}
	return m
}

// ---- tests ------------------------------------------------------------------

func TestGuard_AllowedMessage(t *testing.T) {
	repo := newStub()
	repo.setEnabled("alice", true)
	repo.setAllowed("alice", "whatsapp", true)

	g := allowlist.New(repo, zaptest.NewLogger(t))
	ok, err := g.Check(context.Background(), "alice", "whatsapp", "1555010001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allowed=true")
	}
	if len(repo.audit) != 0 {
		t.Fatalf("expected no audit entries for allowed message, got %d", len(repo.audit))
	}
}

func TestGuard_UnknownIdentity(t *testing.T) {
	repo := newStub()
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "", "whatsapp", "unknown@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected allowed=false")
	}
	if len(repo.audit) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(repo.audit))
	}
	d := auditDetails(t, repo.audit[0])
	if d["reason"] != "unknown_identity" {
		t.Errorf("expected reason=unknown_identity, got %q", d["reason"])
	}
	if repo.audit[0].Action != "message.rejected" {
		t.Errorf("expected action=message.rejected, got %q", repo.audit[0].Action)
	}
	if repo.audit[0].Actor != "system" {
		t.Errorf("expected actor=system, got %q", repo.audit[0].Actor)
	}
}

func TestGuard_UserDisabled(t *testing.T) {
	repo := newStub()
	repo.setEnabled("bob", false)
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "bob", "whatsapp", "bob@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected allowed=false")
	}
	d := auditDetails(t, repo.audit[0])
	if d["reason"] != "user_disabled" {
		t.Errorf("expected reason=user_disabled, got %q", d["reason"])
	}
}

func TestGuard_NotInAllowlist(t *testing.T) {
	repo := newStub()
	repo.setEnabled("carol", true)
	// No allowlist entry for carol
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "carol", "whatsapp", "carol@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected allowed=false")
	}
	d := auditDetails(t, repo.audit[0])
	if d["reason"] != "not_in_allowlist" {
		t.Errorf("expected reason=not_in_allowlist, got %q", d["reason"])
	}
}

func TestGuard_WildcardAllowlist(t *testing.T) {
	repo := newStub()
	repo.setEnabled("dave", true)
	repo.setAllowed("dave", "*", true)
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "dave", "slack", "U0123ABC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allowed=true via wildcard")
	}
	if len(repo.audit) != 0 {
		t.Fatalf("expected no audit entries for allowed message via wildcard")
	}
}

func TestGuard_WildcardFallbackWhenChannelNotSet(t *testing.T) {
	repo := newStub()
	repo.setEnabled("eve", true)
	// channel-specific entry is absent; wildcard is present
	repo.setAllowed("eve", "*", true)
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "eve", "mqtt", "device-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allowed=true via wildcard fallback")
	}
}

func TestGuard_ChannelDisabledWildcardEnabled(t *testing.T) {
	// When the channel-specific entry returns false (not in map), the wildcard is checked.
	// Having *=true should allow the message.
	repo := newStub()
	repo.setEnabled("frank", true)
	repo.setAllowed("frank", "whatsapp", false)
	repo.setAllowed("frank", "*", true)
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "frank", "whatsapp", "frank@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected allowed=true: wildcard should grant when channel entry is false")
	}
}

func TestGuard_DatastoreErrorIsUserEnabled(t *testing.T) {
	repo := newStub()
	repo.setEnabled("grace", true)
	repo.errIsUserEnabled = errors.New("db failure")
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "grace", "whatsapp", "grace@s.whatsapp.net")
	if err == nil {
		t.Fatal("expected error from datastore failure")
	}
	if ok {
		t.Fatal("expected allowed=false on error")
	}
}

func TestGuard_DatastoreErrorIsAllowed(t *testing.T) {
	repo := newStub()
	repo.setEnabled("henry", true)
	repo.errIsAllowed = errors.New("db failure")
	g := allowlist.New(repo, zaptest.NewLogger(t))

	ok, err := g.Check(context.Background(), "henry", "whatsapp", "henry@s.whatsapp.net")
	if err == nil {
		t.Fatal("expected error from datastore failure")
	}
	if ok {
		t.Fatal("expected allowed=false on error")
	}
}

func TestGuard_AuditEntryHasRedactedIdentity(t *testing.T) {
	repo := newStub()
	g := allowlist.New(repo, zaptest.NewLogger(t))

	// Send with unknown identity containing PII.
	_, _ = g.Check(context.Background(), "", "whatsapp", "SECRET_PHONE_NUMBER_5551234567")

	if len(repo.audit) == 0 {
		t.Fatal("expected audit entry")
	}
	d := auditDetails(t, repo.audit[0])
	if d["channel_identity"] != "[REDACTED]" {
		t.Errorf("expected channel_identity to be [REDACTED], got %q", d["channel_identity"])
	}
}

func TestGuard_AuditLogFailureDoesNotCrash(t *testing.T) {
	repo := newStub()
	repo.errLogAudit = errors.New("audit store unavailable")
	g := allowlist.New(repo, zaptest.NewLogger(t))

	// Should not panic or return an error — audit failure is logged but not propagated.
	ok, err := g.Check(context.Background(), "", "whatsapp", "test@s.whatsapp.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected allowed=false")
	}
}

func TestGuard_AllowlistAuditChannelTypePreserved(t *testing.T) {
	repo := newStub()
	g := allowlist.New(repo, zaptest.NewLogger(t))

	_, _ = g.Check(context.Background(), "", "slack", "U0123ABC")

	if len(repo.audit) == 0 {
		t.Fatal("expected audit entry")
	}
	d := auditDetails(t, repo.audit[0])
	if d["channel_type"] != "slack" {
		t.Errorf("expected channel_type=slack in audit details, got %q", d["channel_type"])
	}
}
