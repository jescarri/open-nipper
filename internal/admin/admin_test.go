package admin_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/admin"
	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/datastore/sqlite"
	"github.com/open-nipper/open-nipper/internal/models"
)

// stubAgentHealth implements admin.AgentHealthProvider for testing.
type stubAgentHealth struct {
	statuses []models.AgentHealthInfo
}

func (s *stubAgentHealth) GetAllStatuses() []models.AgentHealthInfo {
	return s.statuses
}

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

func newTestServer(t *testing.T) *admin.Server {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	repo, err := sqlite.Open(dbPath, true, 5000)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	cfg := &config.Config{}
	cfg.Gateway.Admin.Bind = "127.0.0.1"
	cfg.Gateway.Admin.Port = 18790
	cfg.Gateway.Admin.Auth.Enabled = false

	return admin.NewServer(cfg, repo, nil, zap.NewNop())
}

func doHTTP(t *testing.T, ts *httptest.Server, method, path string, body interface{}) map[string]interface{} {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func assertOK(t *testing.T, resp map[string]interface{}, expected bool) {
	t.Helper()
	ok, _ := resp["ok"].(bool)
	if ok != expected {
		t.Errorf("expected ok=%v, got ok=%v (error: %v)", expected, ok, resp["error"])
	}
}

func formatFloat(f float64) string {
	return fmt.Sprintf("%d", int64(f))
}

// createTestUser creates a user via the admin API and returns the auto-generated ID.
func createTestUser(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	resp := doHTTP(t, ts, http.MethodPost, "/admin/users", map[string]interface{}{"name": name})
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})
	return result["id"].(string)
}

// --------------------------------------------------------------------------
// Users
// --------------------------------------------------------------------------

func TestCreateUser(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodPost, "/admin/users", map[string]interface{}{"name": "Alice"})
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})
	id, _ := result["id"].(string)
	if len(id) < 5 || id[:4] != "usr_" {
		t.Errorf("expected auto-generated usr_ prefixed ID, got %v", id)
	}
	if result["name"] != "Alice" {
		t.Errorf("expected name Alice, got %v", result["name"])
	}
}

func TestCreateUser_MissingName(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodPost, "/admin/users", map[string]interface{}{})
	assertOK(t, resp, false)
}

func TestListUsers(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	createTestUser(t, ts, "Bob")
	createTestUser(t, ts, "Carol")

	resp := doHTTP(t, ts, http.MethodGet, "/admin/users", nil)
	assertOK(t, resp, true)
	users := resp["result"].([]interface{})
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestGetUser(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Dave")
	resp := doHTTP(t, ts, http.MethodGet, "/admin/users/"+uid, nil)
	assertOK(t, resp, true)
}

func TestGetUser_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodGet, "/admin/users/nonexistent", nil)
	assertOK(t, resp, false)
}

func TestUpdateUser(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Eve")

	resp := doHTTP(t, ts, http.MethodPut, "/admin/users/"+uid, map[string]interface{}{
		"name":    "Eve Updated",
		"enabled": false,
	})
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})
	if result["name"] != "Eve Updated" {
		t.Errorf("expected updated name, got %v", result["name"])
	}
}

func TestDeleteUser(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Frank")
	resp := doHTTP(t, ts, http.MethodDelete, "/admin/users/"+uid, nil)
	assertOK(t, resp, true)

	resp2 := doHTTP(t, ts, http.MethodGet, "/admin/users/"+uid, nil)
	assertOK(t, resp2, false)
}

// --------------------------------------------------------------------------
// Identities
// --------------------------------------------------------------------------

func TestIdentityCRUD(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Greta")

	resp := doHTTP(t, ts, http.MethodPost, "/admin/users/"+uid+"/identities", map[string]interface{}{
		"channel_type":     "whatsapp",
		"channel_identity": "1555010001@s.whatsapp.net",
	})
	assertOK(t, resp, true)

	resp2 := doHTTP(t, ts, http.MethodGet, "/admin/users/"+uid+"/identities", nil)
	assertOK(t, resp2, true)
	items := resp2["result"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(items))
	}

	identity := items[0].(map[string]interface{})
	id := formatFloat(identity["id"].(float64))
	resp3 := doHTTP(t, ts, http.MethodDelete, "/admin/users/"+uid+"/identities/"+id, nil)
	assertOK(t, resp3, true)

	resp4 := doHTTP(t, ts, http.MethodGet, "/admin/users/"+uid+"/identities", nil)
	assertOK(t, resp4, true)
	items2 := resp4["result"].([]interface{})
	if len(items2) != 0 {
		t.Errorf("expected 0 identities after removal, got %d", len(items2))
	}
}

func TestIdentity_MissingFields(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Hugo")
	resp := doHTTP(t, ts, http.MethodPost, "/admin/users/"+uid+"/identities", map[string]interface{}{
		"channel_type": "whatsapp",
	})
	assertOK(t, resp, false)
}

// --------------------------------------------------------------------------
// Allowlist
// --------------------------------------------------------------------------

func TestAllowlistCRUD(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Henry")

	resp := doHTTP(t, ts, http.MethodPost, "/admin/allowlist", map[string]interface{}{
		"user_id": uid, "channel_type": "whatsapp", "enabled": true,
	})
	assertOK(t, resp, true)

	resp2 := doHTTP(t, ts, http.MethodGet, "/admin/allowlist", nil)
	assertOK(t, resp2, true)
	items := resp2["result"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 allowlist entry, got %d", len(items))
	}

	resp3 := doHTTP(t, ts, http.MethodPut, "/admin/allowlist/"+uid+"/whatsapp", map[string]interface{}{"enabled": false})
	assertOK(t, resp3, true)

	resp4 := doHTTP(t, ts, http.MethodDelete, "/admin/allowlist/"+uid+"/whatsapp", nil)
	assertOK(t, resp4, true)
}

func TestAllowlistByChannel(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Iris")
	doHTTP(t, ts, http.MethodPost, "/admin/allowlist", map[string]interface{}{
		"user_id": uid, "channel_type": "slack", "enabled": true,
	})

	resp := doHTTP(t, ts, http.MethodGet, "/admin/allowlist/slack", nil)
	assertOK(t, resp, true)
	items := resp["result"].([]interface{})
	if len(items) != 1 {
		t.Errorf("expected 1 slack entry, got %d", len(items))
	}
}

func TestAllowlistWildcard(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Jake")
	resp := doHTTP(t, ts, http.MethodPost, "/admin/allowlist", map[string]interface{}{
		"user_id": uid, "channel_type": "*", "enabled": true,
	})
	assertOK(t, resp, true)
}

// --------------------------------------------------------------------------
// Policies
// --------------------------------------------------------------------------

func TestPolicyCRUD(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Jack")

	resp := doHTTP(t, ts, http.MethodPut, "/admin/users/"+uid+"/policies/tools", map[string]interface{}{
		"allow": []string{"read", "write"},
		"deny":  []string{"exec"},
	})
	assertOK(t, resp, true)

	resp2 := doHTTP(t, ts, http.MethodGet, "/admin/users/"+uid+"/policies", nil)
	assertOK(t, resp2, true)
	items := resp2["result"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(items))
	}

	resp3 := doHTTP(t, ts, http.MethodDelete, "/admin/users/"+uid+"/policies/tools", nil)
	assertOK(t, resp3, true)
}

// --------------------------------------------------------------------------
// Agents
// --------------------------------------------------------------------------

func TestAgentProvision(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Kate")

	resp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{
		"user_id": uid,
		"label":   "primary",
	})
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})
	token, _ := result["authToken"].(string)
	if len(token) < 10 || token[:4] != "npr_" {
		t.Errorf("expected valid token with npr_ prefix, got %q", token)
	}
}

func TestAgentProvision_UserNotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{
		"user_id": "nonexistent",
		"label":   "primary",
	})
	assertOK(t, resp, false)
}

func TestAgentProvision_MissingLabel(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Lara")
	resp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid})
	assertOK(t, resp, false)
}

func TestAgentList(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Leo")
	doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "agent1"})
	doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "agent2"})

	resp := doHTTP(t, ts, http.MethodGet, "/admin/agents?user_id="+uid, nil)
	assertOK(t, resp, true)
	items := resp["result"].([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 agents, got %d", len(items))
	}
}

func TestAgentGet(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Mark")
	provResp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "main"})
	result := provResp["result"].(map[string]interface{})
	agent := result["agent"].(map[string]interface{})
	agentID := agent["id"].(string)

	resp := doHTTP(t, ts, http.MethodGet, "/admin/agents/"+agentID, nil)
	assertOK(t, resp, true)
}

func TestAgentRotateToken(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Mia")
	provResp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "main"})
	result := provResp["result"].(map[string]interface{})
	agent := result["agent"].(map[string]interface{})
	agentID := agent["id"].(string)
	oldToken := result["authToken"].(string)

	rotResp := doHTTP(t, ts, http.MethodPost, "/admin/agents/"+agentID+"/rotate", nil)
	assertOK(t, rotResp, true)
	rotResult := rotResp["result"].(map[string]interface{})
	newToken := rotResult["authToken"].(string)
	if newToken == oldToken {
		t.Error("rotated token should differ from old token")
	}
	if newToken[:4] != "npr_" {
		t.Errorf("new token should have npr_ prefix, got %q", newToken)
	}
}

func TestAgentRevoke(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Nina")
	provResp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "main"})
	result := provResp["result"].(map[string]interface{})
	agent := result["agent"].(map[string]interface{})
	agentID := agent["id"].(string)

	resp := doHTTP(t, ts, http.MethodPost, "/admin/agents/"+agentID+"/revoke", nil)
	assertOK(t, resp, true)

	getResp := doHTTP(t, ts, http.MethodGet, "/admin/agents/"+agentID, nil)
	assertOK(t, getResp, true)
	agentResult := getResp["result"].(map[string]interface{})
	if agentResult["status"] != "revoked" {
		t.Errorf("expected status revoked, got %v", agentResult["status"])
	}
}

func TestAgentDelete(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Oscar")
	provResp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{"user_id": uid, "label": "main"})
	result := provResp["result"].(map[string]interface{})
	agent := result["agent"].(map[string]interface{})
	agentID := agent["id"].(string)

	resp := doHTTP(t, ts, http.MethodDelete, "/admin/agents/"+agentID, nil)
	assertOK(t, resp, true)

	getResp := doHTTP(t, ts, http.MethodGet, "/admin/agents/"+agentID, nil)
	assertOK(t, getResp, false)
}

// --------------------------------------------------------------------------
// System / Health
// --------------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodGet, "/admin/health", nil)
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})
	if result["status"] != "healthy" {
		t.Errorf("expected healthy, got %v", result["status"])
	}
	components, _ := result["components"].(map[string]interface{})
	ds, _ := components["datastore"].(map[string]interface{})
	if ds["status"] != "ok" {
		t.Errorf("expected datastore ok, got %v", ds["status"])
	}
}

func TestHealthEndpoint_WithAgentHealth(t *testing.T) {
	srv := newTestServer(t)

	srv.SetAgentHealthProvider(&stubAgentHealth{
		statuses: []models.AgentHealthInfo{
			{UserID: "alice", Queue: "nipper-agent-alice", ConsumerCount: 1, MessagesReady: 0, Status: "idle"},
			{UserID: "bob", Queue: "nipper-agent-bob", ConsumerCount: 0, MessagesReady: 3, Status: "degraded"},
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodGet, "/admin/health", nil)
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})

	agents, ok := result["agents"].([]interface{})
	if !ok {
		t.Fatal("expected agents array in health response")
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	a0 := agents[0].(map[string]interface{})
	a1 := agents[1].(map[string]interface{})
	found := map[string]bool{}
	for _, a := range []map[string]interface{}{a0, a1} {
		uid := a["user_id"].(string)
		found[uid] = true
		if uid == "alice" {
			if a["status"] != "idle" {
				t.Errorf("alice: expected idle, got %v", a["status"])
			}
			if int(a["consumer_count"].(float64)) != 1 {
				t.Errorf("alice: expected 1 consumer, got %v", a["consumer_count"])
			}
		}
		if uid == "bob" {
			if a["status"] != "degraded" {
				t.Errorf("bob: expected degraded, got %v", a["status"])
			}
			if int(a["messages_ready"].(float64)) != 3 {
				t.Errorf("bob: expected 3 ready, got %v", a["messages_ready"])
			}
		}
	}
	if !found["alice"] || !found["bob"] {
		t.Errorf("missing user in agents: %v", found)
	}
}

func TestHealthEndpoint_WithoutAgentHealth(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodGet, "/admin/health", nil)
	assertOK(t, resp, true)
	result := resp["result"].(map[string]interface{})

	// agents should be nil/absent when no provider is set
	if agents, ok := result["agents"]; ok && agents != nil {
		t.Errorf("expected no agents field without provider, got %v", agents)
	}
}

func TestAuditLog(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	createTestUser(t, ts, "Paula")

	resp := doHTTP(t, ts, http.MethodGet, "/admin/audit", nil)
	assertOK(t, resp, true)
	entries := resp["result"].([]interface{})
	if len(entries) == 0 {
		t.Error("expected at least one audit entry after user creation")
	}
}

func TestAuditLogActionFilter(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	createTestUser(t, ts, "Quinn")

	resp := doHTTP(t, ts, http.MethodGet, "/admin/audit?action=user.created", nil)
	assertOK(t, resp, true)
	entries := resp["result"].([]interface{})
	if len(entries) == 0 {
		t.Error("expected at least one user.created audit entry")
	}
}

func TestConfigEndpoint(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := doHTTP(t, ts, http.MethodGet, "/admin/config", nil)
	assertOK(t, resp, true)
}

// --------------------------------------------------------------------------
// Auth middleware
// --------------------------------------------------------------------------

func TestAuthMiddleware_RejectsWithoutToken(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	repo, err := sqlite.Open(dbPath, true, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer repo.Close()

	cfg := &config.Config{}
	cfg.Gateway.Admin.Bind = "127.0.0.1"
	cfg.Gateway.Admin.Port = 18790
	cfg.Gateway.Admin.Auth.Enabled = true
	cfg.Gateway.Admin.Auth.Token = "test-secret"

	srv := admin.NewServer(cfg, repo, nil, zap.NewNop())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/users", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_RejectsWrongToken(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	repo, _ := sqlite.Open(dbPath, true, 5000)
	defer repo.Close()

	cfg := &config.Config{}
	cfg.Gateway.Admin.Bind = "127.0.0.1"
	cfg.Gateway.Admin.Port = 18790
	cfg.Gateway.Admin.Auth.Enabled = true
	cfg.Gateway.Admin.Auth.Token = "correct-secret"

	srv := admin.NewServer(cfg, repo, nil, zap.NewNop())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/users", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_AllowsCorrectToken(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	repo, _ := sqlite.Open(dbPath, true, 5000)
	defer repo.Close()

	cfg := &config.Config{}
	cfg.Gateway.Admin.Bind = "127.0.0.1"
	cfg.Gateway.Admin.Port = 18790
	cfg.Gateway.Admin.Auth.Enabled = true
	cfg.Gateway.Admin.Auth.Token = "correct-secret"

	srv := admin.NewServer(cfg, repo, nil, zap.NewNop())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/users", nil)
	req.Header.Set("Authorization", "Bearer correct-secret")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with correct token, got %d", resp.StatusCode)
	}
}

// --------------------------------------------------------------------------
// Token uniqueness
// --------------------------------------------------------------------------

func TestTokenUniqueness(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	uid := createTestUser(t, ts, "Rachel")

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		resp := doHTTP(t, ts, http.MethodPost, "/admin/agents", map[string]interface{}{
			"user_id": uid,
			"label":   "agent-" + string(rune('a'+i)),
		})
		if ok, _ := resp["ok"].(bool); !ok {
			t.Fatalf("provision failed at i=%d: %v", i, resp["error"])
		}
		result := resp["result"].(map[string]interface{})
		token := result["authToken"].(string)
		if token[:4] != "npr_" {
			t.Errorf("token missing npr_ prefix: %q", token)
		}
		if seen[token] {
			t.Errorf("duplicate token generated: %q", token)
		}
		seen[token] = true
	}
}
