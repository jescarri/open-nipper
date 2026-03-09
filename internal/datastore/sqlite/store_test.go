package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jescarri/open-nipper/internal/models"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := Open(path, true, 5000)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_RunsMigrations(t *testing.T) {
	s := openTestStore(t)
	rows, err := s.db.QueryContext(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var v int
		rows.Scan(&v)
		versions = append(versions, v)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	if len(versions) != len(want) {
		t.Errorf("migrations applied: got %v, want %v", versions, want)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s1, err := Open(path, true, 5000)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// Second open should not fail even though migrations are already applied.
	s2, err := Open(path, true, 5000)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	s2.Close()
}

// --- User tests ---

func TestCreateAndGetUser(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, models.CreateUserRequest{
		Name: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("unexpected user: %+v", user)
	}
	if len(user.ID) < 5 || user.ID[:4] != "usr_" {
		t.Errorf("expected auto-generated usr_ prefixed ID, got %q", user.ID)
	}
	if !user.Enabled {
		t.Error("expected user to be enabled by default")
	}
	if user.DefaultModel != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected default model: %s", user.DefaultModel)
	}

	got, err := s.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("GetUser name mismatch")
	}
}

func TestUpdateUser(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Bob"})

	newName := "Robert"
	disabled := false
	got, err := s.UpdateUser(ctx, user.ID, models.UpdateUserRequest{
		Name:    &newName,
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got.Name != "Robert" || got.Enabled {
		t.Errorf("UpdateUser result unexpected: %+v", got)
	}
}

func TestDeleteUser(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Carol"})
	if err := s.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := s.DeleteUser(ctx, user.ID); err == nil {
		t.Error("expected error when deleting non-existent user")
	}
}

func TestListUsers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"u1", "u2", "u3"} {
		s.CreateUser(ctx, models.CreateUserRequest{Name: name})
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 3 {
		t.Errorf("expected 3 users, got %d", len(users))
	}
}

func TestIsUserEnabled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Alice"})

	enabled, err := s.IsUserEnabled(ctx, user.ID)
	if err != nil || !enabled {
		t.Errorf("expected enabled=true, got %v, err=%v", enabled, err)
	}
	enabled, err = s.IsUserEnabled(ctx, "nonexistent")
	if err != nil || enabled {
		t.Errorf("expected enabled=false for unknown user, got %v, err=%v", enabled, err)
	}
}

// --- Identity tests ---

func TestIdentityCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Alice"})

	if err := s.AddIdentity(ctx, user.ID, "whatsapp", "+1555010001@s.whatsapp.net"); err != nil {
		t.Fatalf("AddIdentity: %v", err)
	}

	resolvedID, err := s.ResolveIdentity(ctx, "whatsapp", "+1555010001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if resolvedID != user.ID {
		t.Errorf("expected %s, got %s", user.ID, resolvedID)
	}

	empty, _ := s.ResolveIdentity(ctx, "whatsapp", "unknown")
	if empty != "" {
		t.Errorf("expected empty string for unknown identity, got %s", empty)
	}

	ids, err := s.ListIdentities(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 identity, got %d", len(ids))
	}

	if err := s.RemoveIdentity(ctx, ids[0].ID); err != nil {
		t.Fatalf("RemoveIdentity: %v", err)
	}
	ids, _ = s.ListIdentities(ctx, user.ID)
	if len(ids) != 0 {
		t.Error("expected 0 identities after remove")
	}
}

// --- Allowlist tests ---

func TestAllowlistCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Alice"})
	u1 := user.ID

	if err := s.SetAllowed(ctx, u1, "whatsapp", true, "admin"); err != nil {
		t.Fatalf("SetAllowed: %v", err)
	}

	allowed, err := s.IsAllowed(ctx, u1, "whatsapp")
	if err != nil || !allowed {
		t.Errorf("expected allowed, got %v, err=%v", allowed, err)
	}

	notAllowed, _ := s.IsAllowed(ctx, u1, "slack")
	if notAllowed {
		t.Error("expected not allowed for unconfigured channel")
	}

	// Wildcard: allow all channels.
	s.SetAllowed(ctx, u1, "*", true, "admin")
	allowed, _ = s.IsAllowed(ctx, u1, "mqtt")
	if !allowed {
		t.Error("expected wildcard to grant access")
	}

	entries, err := s.ListAllowed(ctx, "whatsapp")
	if err != nil {
		t.Fatalf("ListAllowed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry for whatsapp, got %d", len(entries))
	}

	if err := s.RemoveAllowed(ctx, u1, "whatsapp"); err != nil {
		t.Fatalf("RemoveAllowed: %v", err)
	}
}

// --- Agent tests ---

func TestAgentCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Alice"})

	agent, err := s.ProvisionAgent(ctx, models.ProvisionAgentRequest{
		UserID:      user.ID,
		Label:       "primary",
		TokenHash:   "hash1",
		TokenPrefix: "npr_ab",
	})
	if err != nil {
		t.Fatalf("ProvisionAgent: %v", err)
	}
	if agent.Status != models.AgentStatusProvisioned {
		t.Errorf("expected provisioned status, got %s", agent.Status)
	}

	got, err := s.GetAgentByTokenHash(ctx, "hash1")
	if err != nil {
		t.Fatalf("GetAgentByTokenHash: %v", err)
	}
	if got.ID != agent.ID {
		t.Errorf("ID mismatch: %s != %s", got.ID, agent.ID)
	}

	if err := s.UpdateAgentStatus(ctx, agent.ID, models.AgentStatusRegistered, &models.AgentRegistrationMeta{IP: "1.2.3.4"}); err != nil {
		t.Fatalf("UpdateAgentStatus: %v", err)
	}
	got, _ = s.GetAgent(ctx, agent.ID)
	if got.Status != models.AgentStatusRegistered {
		t.Errorf("expected registered, got %s", got.Status)
	}

	agents, err := s.ListAgents(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("expected 1 agent, got %d", len(agents))
	}

	if err := s.RevokeAgent(ctx, agent.ID); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}
	got, _ = s.GetAgent(ctx, agent.ID)
	if got.Status != models.AgentStatusRevoked {
		t.Errorf("expected revoked, got %s", got.Status)
	}
}

// --- Audit tests ---

func TestAuditLog(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.LogAdminAction(ctx, models.AdminAuditEntry{
		Action:  "user.created",
		Actor:   "admin",
		Details: "{}",
	}); err != nil {
		t.Fatalf("LogAdminAction: %v", err)
	}

	entries, err := s.QueryAuditLog(ctx, models.AuditQueryFilters{})
	if err != nil {
		t.Fatalf("QueryAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Action != "user.created" {
		t.Errorf("unexpected action: %s", entries[0].Action)
	}
}

// --- Backup test ---

func TestBackup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	user, _ := s.CreateUser(ctx, models.CreateUserRequest{Name: "Alice"})

	dir := t.TempDir()
	destPath := filepath.Join(dir, "backup.db")
	if err := s.Backup(ctx, destPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Verify the backup is readable.
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("backup file not created: %v", err)
	}

	backup, err := Open(destPath, true, 5000)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()

	got, err := backup.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("backup GetUser: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("backup user name mismatch: %s", got.Name)
	}
}

// --- Ping test ---

func TestPing(t *testing.T) {
	s := openTestStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}
