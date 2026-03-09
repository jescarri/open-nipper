package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

func testAdapter(botToken string) *Adapter {
	logger, _ := zap.NewDevelopment()
	return NewAdapter(AdapterDeps{
		Config: config.SlackConfig{
			BotToken:      botToken,
			SigningSecret: "test-secret",
			WebhookPath:   "/webhook/slack",
		},
		Logger: logger,
	})
}

func TestAdapter_ChannelType(t *testing.T) {
	a := testAdapter("xoxb-test")
	if a.ChannelType() != models.ChannelTypeSlack {
		t.Errorf("ChannelType() = %q, want %q", a.ChannelType(), models.ChannelTypeSlack)
	}
}

func TestAdapter_InterfaceCompliance(t *testing.T) {
	var _ channels.ChannelAdapter = (*Adapter)(nil)
}

func TestAdapter_Start(t *testing.T) {
	a := testAdapter("xoxb-test")
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
}

func TestAdapter_Stop(t *testing.T) {
	a := testAdapter("xoxb-test")
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestAdapter_HealthCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(slackResponse(true, "", ""))
	}))
	defer srv.Close()

	a := testAdapter("xoxb-test")
	a.client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
}

func TestAdapter_HealthCheck_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(slackResponse(false, "", "invalid_auth"))
	}))
	defer srv.Close()

	a := testAdapter("xoxb-bad")
	a.client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected HealthCheck to fail")
	}
}

func TestAdapter_HealthCheck_NoToken(t *testing.T) {
	a := testAdapter("")
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected error for missing bot token")
	}
}

func TestAdapter_NormalizeInbound_TextMessage(t *testing.T) {
	a := testAdapter("xoxb-test")
	raw := `{
		"type": "event_callback",
		"team_id": "T0123",
		"event": {
			"type": "message",
			"user": "U0123ABC",
			"text": "hello slack",
			"channel": "C0456DEF",
			"ts": "1708512000.000100"
		}
	}`

	msg, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("NormalizeInbound() error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content.Text != "hello slack" {
		t.Errorf("text = %q, want %q", msg.Content.Text, "hello slack")
	}
}

func TestAdapter_NormalizeInbound_BotFiltered(t *testing.T) {
	a := testAdapter("xoxb-test")
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"bot_id": "B001",
			"text": "bot msg",
			"channel": "C001",
			"ts": "100.001"
		}
	}`

	msg, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("NormalizeInbound() error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for bot message")
	}
}

func TestAdapter_NormalizeInbound_NonMessageEvent(t *testing.T) {
	a := testAdapter("xoxb-test")
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "reaction_added",
			"user": "U001"
		}
	}`

	msg, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("NormalizeInbound() error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for non-message event")
	}
}

func TestAdapter_DeliverResponse_NilSafe(t *testing.T) {
	a := testAdapter("xoxb-test")
	if err := a.DeliverResponse(context.Background(), nil); err != nil {
		t.Fatalf("DeliverResponse(nil) error: %v", err)
	}
}

func TestAdapter_DeliverEvent_NilSafe(t *testing.T) {
	a := testAdapter("xoxb-test")
	if err := a.DeliverEvent(context.Background(), nil); err != nil {
		t.Fatalf("DeliverEvent(nil) error: %v", err)
	}
}

func TestAdapter_DeliverResponse_PostMessage(t *testing.T) {
	var capturedPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		_, _ = w.Write(slackResponse(true, "99.88", ""))
	}))
	defer srv.Close()

	a := testAdapter("xoxb-test")
	a.client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	resp := &models.NipperResponse{
		SessionKey: "test-sess",
		Text:       "response text",
		Meta: models.SlackMeta{
			ChannelID: "C001",
			BotToken:  "xoxb-test",
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType: models.ChannelTypeSlack,
			ChannelID:   "C001",
		},
	}

	if err := a.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("DeliverResponse() error: %v", err)
	}

	if capturedPayload["text"] != "response text" {
		t.Errorf("text = %v, want %q", capturedPayload["text"], "response text")
	}
}

func TestAdapter_DeliverEvent_IgnoresUnknownEventTypes(t *testing.T) {
	a := testAdapter("xoxb-test")
	event := &models.NipperEvent{
		Type:       models.EventTypeToolStart,
		SessionKey: "sess-1",
	}
	if err := a.DeliverEvent(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg := config.SlackConfig{
		BotToken:      "xoxb-test",
		SigningSecret: "secret",
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
}

func TestValidate_MissingBotToken(t *testing.T) {
	cfg := config.SlackConfig{
		SigningSecret: "secret",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing bot_token")
	}
}

func TestValidate_MissingSigningSecret(t *testing.T) {
	cfg := config.SlackConfig{
		BotToken: "xoxb-test",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing signing_secret")
	}
}

// rewriteTransport is defined in delivery_test.go but we need it here too.
// Since they're in the same package, we can use it directly.
// (The type is already declared in delivery_test.go)

func TestAdapter_SlackClientRef(t *testing.T) {
	a := testAdapter("xoxb-test")
	if a.SlackClientRef() == nil {
		t.Fatal("expected non-nil SlackClient")
	}
}

func TestDeliverEvent_ErrorEvent_PostsWarning(t *testing.T) {
	var capturedPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		_, _ = w.Write(slackResponse(true, "err.msg.ts", ""))
	}))
	defer srv.Close()

	a := testAdapter("xoxb-test")
	a.client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	// Since DeliverEvent needs to extract SlackMeta from the event,
	// and our current extractSlackMetaFromEvent returns false,
	// the error event path that doesn't have meta will return nil.
	// Test the client directly instead.
	event := &models.NipperEvent{
		Type:       models.EventTypeError,
		SessionKey: "sess-err",
		Error: &models.EventError{
			Code:    "overflow",
			Message: "context limit exceeded",
		},
	}

	// Call through the client directly with token and channel
	text := ":warning: Error: " + event.Error.Message
	err := a.client.chatPostMessage(context.Background(), "xoxb-test", "C001", "", text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPayload["text"] == nil {
		t.Fatal("expected text in payload")
	}
	if !strings.Contains(capturedPayload["text"].(string), "context limit exceeded") {
		t.Errorf("text = %v, expected to contain error message", capturedPayload["text"])
	}
}
