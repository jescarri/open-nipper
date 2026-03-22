package agent

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/registration"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/pkg/session"
)

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	logger := zap.NewNop()
	store := session.NewStore(dir, logger)
	tracker := NewUsageTracker("gpt-4o", 128000)

	cfg := &config.AgentRuntimeConfig{
		BasePath: dir,
		Inference: config.InferenceConfig{
			Provider: "openai",
			Model:    "gpt-4o",
		},
		Tools: config.AgentToolsConfig{},
		MaxSteps: 25,
	}
	reg := &registration.RegistrationResult{
		UserID: "test-user",
		User:   registration.UserInfo{ID: "test-user"},
	}

	return NewRuntime(cfg, reg, nil, nil, store, logger,
		WithUsageTracker(tracker),
	)
}

const testSessionKey = "user:test-user:channel:whatsapp:session:sess1"

func makeMsg(sessionKey, text string) *models.NipperMessage {
	return &models.NipperMessage{
		UserID:      "test-user",
		SessionKey:  sessionKey,
		ChannelType: "whatsapp",
		Content: models.MessageContent{
			Text: text,
		},
		DeliveryContext: models.DeliveryContext{},
	}
}

func TestHandleCommandHelp(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/help"))

	if result == nil {
		t.Fatal("expected command result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "Available Commands") {
		t.Errorf("help response missing header: %s", result.Response)
	}
	if !strings.Contains(result.Response, "/new") {
		t.Error("help response missing /new")
	}
	if !strings.Contains(result.Response, "/usage") {
		t.Error("help response missing /usage")
	}
}

func TestHandleCommandNotACommand(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "hello world"))
	if result != nil {
		t.Error("expected nil for non-command")
	}
}

func TestHandleCommandUsage(t *testing.T) {
	rt := newTestRuntime(t)

	rt.usageTracker.Record(testSessionKey, 500, 200, 500)

	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/usage"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "500") {
		t.Error("expected input tokens in response")
	}
	if !strings.Contains(result.Response, "200") {
		t.Error("expected output tokens in response")
	}
}

func TestHandleCommandUsageNoData(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/usage"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "No usage data") {
		t.Errorf("expected no data message, got: %s", result.Response)
	}
}

func TestHandleCommandNewSession(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	sessionKey := testSessionKey
	_, err := rt.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      "test-user",
		SessionID:   "sess1",
		ChannelType: "whatsapp",
		Model:       "gpt-4o",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Record usage.
	rt.usageTracker.Record(sessionKey, 100, 50, 100)

	result := rt.handleCommand(ctx, makeMsg(sessionKey, "/new"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if result.Action != ActionNewSession {
		t.Errorf("expected ActionNewSession, got %v", result.Action)
	}
	if !strings.Contains(result.Response, "cleared") {
		t.Errorf("expected 'cleared' in response: %s", result.Response)
	}

	// Usage should be reset.
	usage := rt.usageTracker.Get(sessionKey)
	if usage != nil {
		t.Error("expected usage to be reset after /new")
	}
}

func TestHandleCommandReset(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	sessionKey := testSessionKey
	_, err := rt.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      "test-user",
		SessionID:   "sess1",
		ChannelType: "whatsapp",
		Model:       "gpt-4o",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	result := rt.handleCommand(ctx, makeMsg(sessionKey, "/reset"))
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Action != ActionNewSession {
		t.Errorf("expected ActionNewSession for /reset, got %v", result.Action)
	}
}

func TestHandleCommandStatus(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	sessionKey := testSessionKey
	_, err := rt.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      "test-user",
		SessionID:   "sess1",
		ChannelType: "whatsapp",
		Model:       "gpt-4o",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	result := rt.handleCommand(ctx, makeMsg(sessionKey, "/status"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "Session Status") {
		t.Errorf("missing status header: %s", result.Response)
	}
	if !strings.Contains(result.Response, "gpt-4o") {
		t.Errorf("missing model name: %s", result.Response)
	}
}

func TestHandleCommandPersona(t *testing.T) {
	rt := newTestRuntime(t)
	sessionKey := testSessionKey

	result := rt.handleCommand(context.Background(), makeMsg(sessionKey, "/persona You are a pirate"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "Persona updated") {
		t.Errorf("expected persona confirmation: %s", result.Response)
	}

	rt.mu.RLock()
	persona := rt.sessionPersonas[sessionKey]
	rt.mu.RUnlock()
	if persona != "You are a pirate" {
		t.Errorf("persona not stored: %s", persona)
	}
}

func TestHandleCommandPersonaEmpty(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/persona"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Usage:") {
		t.Errorf("expected usage message: %s", result.Response)
	}
}

func TestHandleCommandCompact(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	sessionKey := testSessionKey
	_, err := rt.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      "test-user",
		SessionID:   "sess1",
		ChannelType: "whatsapp",
		Model:       "gpt-4o",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	result := rt.handleCommand(ctx, makeMsg(sessionKey, "/compact"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "compaction") {
		t.Errorf("expected compaction message: %s", result.Response)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/foobar"))
	if result != nil {
		t.Errorf("expected nil for unknown command, got: %+v", result)
	}
}

func TestHandleCommandCaseInsensitive(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/HELP"))
	if result == nil {
		t.Fatal("expected result for /HELP")
	}
	if !result.Handled {
		t.Error("expected handled")
	}
}

func TestHandleCommandSetupShowProfile(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "Profile Settings") {
		t.Errorf("expected profile display, got: %s", result.Response)
	}
}

func TestHandleCommandSetupSetField(t *testing.T) {
	rt := newTestRuntime(t)

	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup name Alice"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if !strings.Contains(result.Response, "Alice") {
		t.Errorf("expected Alice in response: %s", result.Response)
	}
	if !strings.Contains(result.Response, "User Name") {
		t.Errorf("expected field label in response: %s", result.Response)
	}

	// Verify the profile was persisted.
	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.UserName != "Alice" {
		t.Errorf("profile.UserName = %q, want Alice", profile.UserName)
	}
}

func TestHandleCommandSetupSetByNumber(t *testing.T) {
	rt := newTestRuntime(t)

	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup 6 Spanish"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Spanish") {
		t.Errorf("expected Spanish in response: %s", result.Response)
	}
	if !strings.Contains(result.Response, "Preferred Language") {
		t.Errorf("expected field label in response: %s", result.Response)
	}

	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.Language != "Spanish" {
		t.Errorf("profile.Language = %q, want Spanish", profile.Language)
	}
}

func TestHandleCommandSetupUnknownField(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup foobar baz"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Unknown field") {
		t.Errorf("expected unknown field message: %s", result.Response)
	}
}

func TestHandleCommandSetupNoValue(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup name"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "provide a value") {
		t.Errorf("expected value prompt: %s", result.Response)
	}
}

func TestHandleCommandSetupMultipleFields(t *testing.T) {
	rt := newTestRuntime(t)

	rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup name Bob"))
	rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup location Tokyo, Japan"))
	rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup skill expert"))

	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.UserName != "Bob" {
		t.Errorf("UserName = %q, want Bob", profile.UserName)
	}
	if profile.Location != "Tokyo, Japan" {
		t.Errorf("Location = %q, want Tokyo, Japan", profile.Location)
	}
	if profile.SkillLevel != "expert" {
		t.Errorf("SkillLevel = %q, want expert", profile.SkillLevel)
	}
}

func TestHandleCommandSetupSurvivesNewSession(t *testing.T) {
	rt := newTestRuntime(t)
	ctx := context.Background()

	// Set up a profile.
	rt.handleCommand(ctx, makeMsg(testSessionKey, "/setup name Alice"))
	rt.handleCommand(ctx, makeMsg(testSessionKey, "/setup language French"))

	// Create a session then reset it.
	_, err := rt.sessions.CreateSession(ctx, session.CreateSessionRequest{
		UserID:      "test-user",
		SessionID:   "sess1",
		ChannelType: "whatsapp",
		Model:       "gpt-4o",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	rt.handleCommand(ctx, makeMsg(testSessionKey, "/new"))

	// Profile should still be intact.
	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.UserName != "Alice" {
		t.Errorf("UserName after /new = %q, want Alice", profile.UserName)
	}
	if profile.Language != "French" {
		t.Errorf("Language after /new = %q, want French", profile.Language)
	}
}

func TestHandleCommandSetupCoords(t *testing.T) {
	rt := newTestRuntime(t)

	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup coords 45.49,-75.66"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Coordinates") {
		t.Errorf("expected Coordinates in response: %s", result.Response)
	}
	if !strings.Contains(result.Response, "45.49") {
		t.Errorf("expected lat in response: %s", result.Response)
	}

	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.Latitude != "45.49" {
		t.Errorf("Latitude = %q, want 45.49", profile.Latitude)
	}
	if profile.Longitude != "-75.66" {
		t.Errorf("Longitude = %q, want -75.66", profile.Longitude)
	}
}

func TestHandleCommandSetupCoordsInvalid(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup coords abc"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Invalid coordinates") {
		t.Errorf("expected invalid coordinates message: %s", result.Response)
	}
}

func TestHandleCommandSetupCoordsEmpty(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup coords"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "provide coordinates") {
		t.Errorf("expected provide coordinates message: %s", result.Response)
	}
}

func TestHandleCommandSetupLatitude(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/setup lat 45.49"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "Latitude") {
		t.Errorf("expected Latitude in response: %s", result.Response)
	}

	profile, err := LoadProfile(rt.cfg.BasePath, "test-user")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.Latitude != "45.49" {
		t.Errorf("Latitude = %q, want 45.49", profile.Latitude)
	}
}

func TestHandleCommandHelpIncludesSetup(t *testing.T) {
	rt := newTestRuntime(t)
	result := rt.handleCommand(context.Background(), makeMsg(testSessionKey, "/help"))
	if result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Response, "/setup") {
		t.Error("help response missing /setup")
	}
}

func TestExtractLocationFromMessage_NoLocation(t *testing.T) {
	msg := makeMsg(testSessionKey, "hello")
	lat, lon, ok := extractLocationFromMessage(msg)
	if ok {
		t.Errorf("expected no location, got lat=%f lon=%f", lat, lon)
	}
}

func TestExtractLocationFromMessage_WithLocation(t *testing.T) {
	msg := &models.NipperMessage{
		UserID:      "test-user",
		SessionKey:  testSessionKey,
		ChannelType: "whatsapp",
		Content: models.MessageContent{
			Text: "My location",
			Parts: []models.ContentPart{
				{Type: "location", Latitude: 49.1044, Longitude: -122.6607},
			},
		},
	}
	lat, lon, ok := extractLocationFromMessage(msg)
	if !ok {
		t.Fatal("expected location to be found")
	}
	if lat != 49.1044 || lon != -122.6607 {
		t.Errorf("got lat=%f lon=%f, want 49.1044 -122.6607", lat, lon)
	}
}

func TestExtractLocationFromMessage_FirstLocationWins(t *testing.T) {
	msg := &models.NipperMessage{
		UserID:      "test-user",
		SessionKey:  testSessionKey,
		ChannelType: "whatsapp",
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "text", Text: "ignore"},
				{Type: "location", Latitude: 45.5, Longitude: -73.6},
				{Type: "location", Latitude: 50.0, Longitude: -120.0},
			},
		},
	}
	lat, lon, ok := extractLocationFromMessage(msg)
	if !ok {
		t.Fatal("expected location to be found")
	}
	if lat != 45.5 || lon != -73.6 {
		t.Errorf("expected first location part, got lat=%f lon=%f", lat, lon)
	}
}
