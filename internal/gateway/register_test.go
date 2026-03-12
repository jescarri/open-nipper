package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/queue"
	"github.com/jescarri/open-nipper/internal/ratelimit"
)

// --- Mock repo for registration tests ---

type regMockRepo struct {
	mockRepo // embed the base mock with all stubs

	agent         *models.Agent
	agentErr      error
	user          *models.User
	userErr       error
	toolsPolicy   *models.PolicyData
	policyErr     error
	statusUpdated bool
	rmqUsernameSet string
	auditEntries  []models.AdminAuditEntry
	updateStatusErr error
}

func (r *regMockRepo) GetAgentByTokenHash(_ context.Context, _ string) (*models.Agent, error) {
	return r.agent, r.agentErr
}

func (r *regMockRepo) GetUser(_ context.Context, _ string) (*models.User, error) {
	return r.user, r.userErr
}

func (r *regMockRepo) GetUserPolicy(_ context.Context, _, policyType string) (*models.PolicyData, error) {
	if policyType == "tools" {
		return r.toolsPolicy, r.policyErr
	}
	return nil, nil
}

func (r *regMockRepo) UpdateAgentStatus(_ context.Context, _ string, _ models.AgentStatus, _ *models.AgentRegistrationMeta) error {
	if r.updateStatusErr != nil {
		return r.updateStatusErr
	}
	r.statusUpdated = true
	return nil
}

func (r *regMockRepo) SetAgentRMQUsername(_ context.Context, _ string, username string) error {
	r.rmqUsernameSet = username
	return nil
}

func (r *regMockRepo) LogAdminAction(_ context.Context, entry models.AdminAuditEntry) error {
	r.auditEntries = append(r.auditEntries, entry)
	return nil
}

// --- Mock Management Client ---

type regMockMgmt struct {
	createdUsers []string
	permsSet     []queue.VhostPermissions
	createErr    error
	permsErr     error
}

func (m *regMockMgmt) CreateUser(_ context.Context, username, _ string) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.createdUsers = append(m.createdUsers, username)
	return nil
}

func (m *regMockMgmt) DeleteUser(context.Context, string) error { return nil }

func (m *regMockMgmt) SetVhostPermissions(_ context.Context, _, _ string, perms queue.VhostPermissions) error {
	if m.permsErr != nil {
		return m.permsErr
	}
	m.permsSet = append(m.permsSet, perms)
	return nil
}

func (m *regMockMgmt) GetQueueInfo(context.Context, string, string) (*queue.QueueInfo, error) {
	return nil, nil
}

func (m *regMockMgmt) ListQueues(context.Context, string) ([]*queue.QueueInfo, error) {
	return nil, nil
}

var _ queue.ManagementClient = (*regMockMgmt)(nil)

// --- Helpers ---

func testToken() string { return "npr_testtoken1234567890" }

func testTokenHash() string {
	sum := sha256.Sum256([]byte(testToken()))
	return fmt.Sprintf("%x", sum[:])
}

func testAgent() *models.Agent {
	return &models.Agent{
		ID:          "agt-user01-01",
		UserID:      "user-01",
		Label:       "anthropic-primary",
		TokenHash:   testTokenHash(),
		TokenPrefix: "npr_test",
		Status:      models.AgentStatusProvisioned,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func testUser() *models.User {
	return &models.User{
		ID:           "user-01",
		Name:         "Alice",
		Enabled:      true,
		DefaultModel: "claude-sonnet-4-20250514",
		Preferences:  map[string]any{"theme": "dark"},
	}
}

func testConfig() *config.Config {
	return &config.Config{
		Queue: config.QueueConfig{
			RabbitMQ: config.QueueRabbitMQConfig{
				URL:   "amqp://localhost:5672",
				VHost: "/nipper",
			},
		},
		Agents: config.AgentsConfig{
			Registration: config.AgentRegistrationConfig{
				Enabled:   true,
				RateLimit: 10,
			},
		},
		Security: config.SecurityConfig{
			Tools: config.ToolsConfig{
				Policy: config.ToolPolicy{
					Allow: []string{"read", "write", "exec"},
					Deny:  []string{"session_spawn"},
				},
			},
		},
	}
}

func newRegisterHandler(repo *regMockRepo, mgmt queue.ManagementClient, limiter *ratelimit.Limiter) *RegisterHandler {
	return NewRegisterHandler(RegisterHandlerDeps{
		Repo:    repo,
		Mgmt:    mgmt,
		Limiter: limiter,
		Config:  testConfig(),
		Logger:  zap.NewNop(),
	})
}

func doRegister(h *RegisterHandler, token string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/agents/register", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.168.1.100:12345"
	rr := httptest.NewRecorder()
	h.Handle(rr, req)
	return rr
}

func parseResponse(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp
}

// --- Tests ---

func TestRegister_HappyPath(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), `{"agent_type":"anthropic-sdk","version":"1.0.0"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	if resp["ok"] != true {
		t.Fatal("expected ok=true")
	}

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result object")
	}

	if result["agent_id"] != "agt-user01-01" {
		t.Fatalf("unexpected agent_id: %v", result["agent_id"])
	}
	if result["user_id"] != "user-01" {
		t.Fatalf("unexpected user_id: %v", result["user_id"])
	}
	if result["user_name"] != "Alice" {
		t.Fatalf("unexpected user_name: %v", result["user_name"])
	}

	// Check RMQ section
	rmq, ok := result["rabbitmq"].(map[string]any)
	if !ok {
		t.Fatal("expected rabbitmq object")
	}
	if rmq["url"] != "amqp://localhost:5672" {
		t.Fatalf("unexpected url: %v", rmq["url"])
	}
	if rmq["vhost"] != "/nipper" {
		t.Fatalf("unexpected vhost: %v", rmq["vhost"])
	}
	if !strings.HasPrefix(rmq["username"].(string), "agent-") {
		t.Fatalf("unexpected username: %v", rmq["username"])
	}
	if rmq["password"] == nil || rmq["password"] == "" {
		t.Fatal("expected non-empty password")
	}

	queues, ok := rmq["queues"].(map[string]any)
	if !ok {
		t.Fatal("expected queues object")
	}
	if queues["agent"] != "nipper-agent-user-01" {
		t.Fatalf("unexpected agent queue: %v", queues["agent"])
	}
	if queues["control"] != "nipper-control-user-01" {
		t.Fatalf("unexpected control queue: %v", queues["control"])
	}

	// Verify RMQ user was created
	if len(mgmt.createdUsers) != 1 {
		t.Fatalf("expected 1 RMQ user, got %d", len(mgmt.createdUsers))
	}
	if mgmt.createdUsers[0] != "agent-agt-user01-01" {
		t.Fatalf("unexpected RMQ username: %s", mgmt.createdUsers[0])
	}

	// Verify permissions were set
	if len(mgmt.permsSet) != 1 {
		t.Fatalf("expected 1 permission set, got %d", len(mgmt.permsSet))
	}
	perms := mgmt.permsSet[0]
	if !strings.Contains(perms.Configure, "nipper-agent-user-01") {
		t.Fatalf("configure perm should reference agent queue: %s", perms.Configure)
	}
	if !strings.Contains(perms.Read, "nipper-control-user-01") {
		t.Fatalf("read perm should reference control queue: %s", perms.Read)
	}
	if !strings.Contains(perms.Write, "nipper\\.events") {
		t.Fatalf("write perm should reference events exchange: %s", perms.Write)
	}

	// Verify datastore updates
	if !repo.statusUpdated {
		t.Fatal("expected agent status to be updated")
	}
	if repo.rmqUsernameSet != "agent-agt-user01-01" {
		t.Fatalf("unexpected RMQ username set: %s", repo.rmqUsernameSet)
	}

	// Verify audit entry
	found := false
	for _, e := range repo.auditEntries {
		if e.Action == "agent.registered" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected agent.registered audit entry")
	}
}

func TestRegister_MissingAuthHeader(t *testing.T) {
	repo := &regMockRepo{agent: testAgent(), user: testUser()}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	req := httptest.NewRequest(http.MethodPost, "/agents/register", nil)
	rr := httptest.NewRecorder()
	h.Handle(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	if resp["error"] != "unauthorized" {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
}

func TestRegister_MalformedAuthHeader(t *testing.T) {
	repo := &regMockRepo{agent: testAgent(), user: testUser()}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	req := httptest.NewRequest(http.MethodPost, "/agents/register", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()
	h.Handle(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestRegister_InvalidToken(t *testing.T) {
	repo := &regMockRepo{
		agentErr: fmt.Errorf("not found"),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, "npr_badtoken", "")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	if resp["error"] != "unauthorized" {
		t.Fatalf("unexpected error: %v", resp["error"])
	}

	// Verify audit logged
	found := false
	for _, e := range repo.auditEntries {
		if e.Action == "agent.register_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected audit entry for failed registration")
	}
}

func TestRegister_RevokedAgent(t *testing.T) {
	agent := testAgent()
	agent.Status = models.AgentStatusRevoked
	repo := &regMockRepo{
		agent: agent,
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	found := false
	for _, e := range repo.auditEntries {
		if e.Action == "agent.register_failed" && strings.Contains(e.Details, "agent_revoked") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected audit entry with reason agent_revoked")
	}
}

func TestRegister_UserNotFound(t *testing.T) {
	repo := &regMockRepo{
		agent:   testAgent(),
		userErr: fmt.Errorf("not found"),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestRegister_UserDisabled(t *testing.T) {
	user := testUser()
	user.Enabled = false
	repo := &regMockRepo{
		agent: testAgent(),
		user:  user,
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	if resp["error"] != "user_disabled" {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
}

func TestRegister_RateLimited(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	limiter := ratelimit.NewLimiter(1, time.Minute)
	h := newRegisterHandler(repo, mgmt, limiter)

	// First request should succeed
	rr := doRegister(h, testToken(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on first request, got %d: %s", rr.Code, rr.Body.String())
	}

	// Second request should be rate limited
	rr = doRegister(h, testToken(), "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	if resp["error"] != "rate_limited" {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	if resp["retry_after"] == nil {
		t.Fatal("expected retry_after field")
	}

	// Check Retry-After header
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}
}

func TestRegister_ManagementAPIUnavailable(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	// nil management client
	h := newRegisterHandler(repo, nil, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	if resp["error"] != "service_unavailable" {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
}

func TestRegister_RMQCreateUserFails(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{createErr: fmt.Errorf("connection refused")}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestRegister_RMQSetPermsFails(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{permsErr: fmt.Errorf("permission denied")}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestRegister_UpdateStatusFails(t *testing.T) {
	repo := &regMockRepo{
		agent:           testAgent(),
		user:            testUser(),
		updateStatusErr: fmt.Errorf("database locked"),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestRegister_ResponseIncludesUserPolicies(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
		toolsPolicy: &models.PolicyData{
			Allow: []string{"read", "write"},
			Deny:  []string{"exec", "session_spawn"},
		},
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	resp := parseResponse(t, rr)
	result := resp["result"].(map[string]any)
	policies := result["policies"].(map[string]any)
	tools := policies["tools"].(map[string]any)

	allow := tools["allow"].([]any)
	if len(allow) != 2 || allow[0] != "read" {
		t.Fatalf("unexpected allow policy: %v", allow)
	}
}

func TestRegister_FallbackToDefaultToolsPolicy(t *testing.T) {
	repo := &regMockRepo{
		agent:       testAgent(),
		user:        testUser(),
		toolsPolicy: nil,
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	resp := parseResponse(t, rr)
	result := resp["result"].(map[string]any)
	policies := result["policies"].(map[string]any)
	tools := policies["tools"].(map[string]any)

	allow := tools["allow"].([]any)
	if len(allow) != 3 {
		t.Fatalf("expected 3 default allow tools, got %d: %v", len(allow), allow)
	}
}

func TestRegister_ResponseIncludesExchanges(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")
	resp := parseResponse(t, rr)
	result := resp["result"].(map[string]any)
	rmq := result["rabbitmq"].(map[string]any)
	exchanges := rmq["exchanges"].(map[string]any)

	expected := map[string]string{
		"sessions": "nipper.sessions",
		"events":   "nipper.events",
		"control":  "nipper.control",
		"logs":     "nipper.logs",
	}
	for k, v := range expected {
		if exchanges[k] != v {
			t.Fatalf("expected exchanges[%s]=%s, got %v", k, v, exchanges[k])
		}
	}
}

func TestRegister_ResponseIncludesRoutingKeys(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")
	resp := parseResponse(t, rr)
	result := resp["result"].(map[string]any)
	rmq := result["rabbitmq"].(map[string]any)
	keys := rmq["routing_keys"].(map[string]any)

	eventsKey := keys["events_publish"].(string)
	if !strings.Contains(eventsKey, "user-01") {
		t.Fatalf("events routing key should contain userId: %s", eventsKey)
	}
	if !strings.Contains(eventsKey, "{sessionId}") {
		t.Fatalf("events routing key should contain {sessionId}: %s", eventsKey)
	}
}

func TestRegister_ResponseIncludesUserDetails(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")
	resp := parseResponse(t, rr)
	result := resp["result"].(map[string]any)
	user := result["user"].(map[string]any)

	if user["id"] != "user-01" {
		t.Fatalf("unexpected user id: %v", user["id"])
	}
	if user["name"] != "Alice" {
		t.Fatalf("unexpected user name: %v", user["name"])
	}
	if user["default_model"] != "claude-sonnet-4-20250514" {
		t.Fatalf("unexpected default model: %v", user["default_model"])
	}
	prefs := user["preferences"].(map[string]any)
	if prefs["theme"] != "dark" {
		t.Fatalf("unexpected preferences: %v", prefs)
	}
}

func TestRegister_EmptyBody(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with empty body, got %d", rr.Code)
	}
}

func TestRegister_PasswordIsUnique(t *testing.T) {
	passwords := make(map[string]bool)
	for i := 0; i < 10; i++ {
		pw, err := generateRMQPassword()
		if err != nil {
			t.Fatal(err)
		}
		if passwords[pw] {
			t.Fatalf("duplicate password on attempt %d", i)
		}
		passwords[pw] = true
	}
}

func TestRegister_PasswordLength(t *testing.T) {
	pw, err := generateRMQPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 20 {
		t.Fatalf("password too short: %d chars", len(pw))
	}
}

func TestRegister_HashToken(t *testing.T) {
	token := "npr_sometoken"
	sum := sha256.Sum256([]byte(token))
	expected := fmt.Sprintf("%x", sum[:])
	got := hashToken(token)
	if got != expected {
		t.Fatalf("hash mismatch: got %s, want %s", got, expected)
	}
}

func TestRegister_ExtractBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{"valid", "Bearer npr_abc123", "npr_abc123"},
		{"no prefix", "npr_abc123", ""},
		{"basic auth", "Basic dXNlcjpwYXNz", ""},
		{"empty", "", ""},
		{"bearer only", "Bearer ", ""},
		{"extra spaces", "Bearer  npr_abc123 ", "npr_abc123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got := extractBearerToken(req)
			if got != tc.expected {
				t.Fatalf("extractBearerToken(%q) = %q, want %q", tc.header, got, tc.expected)
			}
		})
	}
}

func TestRegister_ClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xri        string
		expected   string
	}{
		{"remote addr", "10.0.0.1:8080", "", "", "10.0.0.1"},
		{"x-forwarded-for", "10.0.0.1:8080", "203.0.113.50, 70.41.3.18", "", "203.0.113.50"},
		{"x-real-ip", "10.0.0.1:8080", "", "198.51.100.5", "198.51.100.5"},
		{"xff takes precedence", "10.0.0.1:8080", "203.0.113.50", "198.51.100.5", "203.0.113.50"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				req.Header.Set("X-Real-IP", tc.xri)
			}
			got := clientIP(req)
			if got != tc.expected {
				t.Fatalf("clientIP() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestRegister_VhostPermissionsFormat(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	doRegister(h, testToken(), "")

	if len(mgmt.permsSet) != 1 {
		t.Fatalf("expected 1 permission set, got %d", len(mgmt.permsSet))
	}

	perms := mgmt.permsSet[0]

	// Verify regex patterns are correct
	if perms.Configure != "^(nipper-agent-user-01|nipper-control-user-01)$" {
		t.Fatalf("unexpected configure perm: %s", perms.Configure)
	}
	if perms.Write != `^(nipper\.events|nipper\.logs)$` {
		t.Fatalf("unexpected write perm: %s", perms.Write)
	}
	if perms.Read != "^(nipper-agent-user-01|nipper-control-user-01)$" {
		t.Fatalf("unexpected read perm: %s", perms.Read)
	}
}

func TestRegister_ContentTypeJSON(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	rr := doRegister(h, testToken(), "")

	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
}

func TestRegister_BearerTokenWithSpaces(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	req := httptest.NewRequest(http.MethodPost, "/agents/register", nil)
	req.Header.Set("Authorization", "Bearer   "+testToken()+"  ")
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	h.Handle(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 even with whitespace around token, got %d", rr.Code)
	}
}

func TestRegister_AuditOnAllFailurePaths(t *testing.T) {
	tests := []struct {
		name       string
		agent      *models.Agent
		agentErr   error
		user       *models.User
		userErr    error
		wantAction string
	}{
		{
			name:       "invalid token",
			agentErr:   fmt.Errorf("not found"),
			wantAction: "agent.register_failed",
		},
		{
			name:       "revoked",
			agent:      func() *models.Agent { a := testAgent(); a.Status = models.AgentStatusRevoked; return a }(),
			user:       testUser(),
			wantAction: "agent.register_failed",
		},
		{
			name:       "user not found",
			agent:      testAgent(),
			userErr:    fmt.Errorf("not found"),
			wantAction: "agent.register_failed",
		},
		{
			name:       "user disabled",
			agent:      testAgent(),
			user:       func() *models.User { u := testUser(); u.Enabled = false; return u }(),
			wantAction: "agent.register_failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &regMockRepo{
				agent:    tc.agent,
				agentErr: tc.agentErr,
				user:     tc.user,
				userErr:  tc.userErr,
			}
			mgmt := &regMockMgmt{}
			h := newRegisterHandler(repo, mgmt, nil)

			doRegister(h, testToken(), "")

			found := false
			for _, e := range repo.auditEntries {
				if e.Action == tc.wantAction {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected audit entry with action %q, got entries: %v", tc.wantAction, repo.auditEntries)
			}
		})
	}
}

func TestRegister_RMQUsernameFormat(t *testing.T) {
	repo := &regMockRepo{
		agent: testAgent(),
		user:  testUser(),
	}
	mgmt := &regMockMgmt{}
	h := newRegisterHandler(repo, mgmt, nil)

	doRegister(h, testToken(), "")

	if len(mgmt.createdUsers) != 1 {
		t.Fatal("expected one RMQ user created")
	}

	username := mgmt.createdUsers[0]
	if username != "agent-agt-user01-01" {
		t.Fatalf("RMQ username should be agent-{agentId}, got: %s", username)
	}
}
