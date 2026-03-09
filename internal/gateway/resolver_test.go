package gateway_test

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/jescarri/open-nipper/internal/gateway"
	"github.com/jescarri/open-nipper/internal/models"
)

func newResolver(t *testing.T) *gateway.Resolver {
	t.Helper()
	return gateway.NewResolver(zaptest.NewLogger(t), "claude-default")
}

func baseMsg(userID string, ct models.ChannelType) *models.NipperMessage {
	return &models.NipperMessage{
		MessageID:       "msg-001",
		UserID:          userID,
		ChannelType:     ct,
		ChannelIdentity: "fallback-identity",
	}
}

// ---- WhatsApp ---------------------------------------------------------------

func TestResolver_WhatsApp_UsesChatJID(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("alice", models.ChannelTypeWhatsApp)
	msg.Meta = models.WhatsAppMeta{
		SenderJID: "1555010001@s.whatsapp.net",
		ChatJID:   "12025550100@g.us",
	}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !strings.Contains(key, "12025550100-g-us") {
		t.Errorf("expected key to contain sanitized chatJID, got %q", key)
	}
	if strings.Contains(key, "1555010001") {
		t.Errorf("key must use chatJID (not senderJID), got %q", key)
	}
}

func TestResolver_WhatsApp_FallbackToChannelIdentity(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("alice", models.ChannelTypeWhatsApp)
	msg.ChannelIdentity = "1555010001"

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if !strings.Contains(key, "1555010001") {
		t.Errorf("expected key to contain channel identity fallback, got %q", key)
	}
}

// ---- Slack ------------------------------------------------------------------

func TestResolver_Slack_UsesChannelID(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("bob", models.ChannelTypeSlack)
	msg.Meta = models.SlackMeta{ChannelID: "C0123ABC"}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "C0123ABC") {
		t.Errorf("expected key to contain channel ID, got %q", key)
	}
}

func TestResolver_Slack_UsesChannelAndThreadTS(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("bob", models.ChannelTypeSlack)
	msg.Meta = models.SlackMeta{
		ChannelID: "C0123ABC",
		ThreadTS:  "1708512000.000100",
	}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "C0123ABC") {
		t.Errorf("expected key to contain channel ID, got %q", key)
	}
}

func TestResolver_Slack_DifferentThreadsDifferentSessions(t *testing.T) {
	r := newResolver(t)
	base := &models.NipperMessage{
		MessageID:   "m1",
		UserID:      "bob",
		ChannelType: models.ChannelTypeSlack,
	}
	msg1 := *base
	msg1.Meta = models.SlackMeta{ChannelID: "C0123ABC", ThreadTS: "1111"}
	msg2 := *base
	msg2.Meta = models.SlackMeta{ChannelID: "C0123ABC", ThreadTS: "2222"}

	k1, _ := r.Resolve(context.Background(), &msg1)
	k2, _ := r.Resolve(context.Background(), &msg2)
	if k1 == k2 {
		t.Error("different threads should produce different session keys")
	}
}

func TestResolver_Slack_SameThreadSameSession(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("bob", models.ChannelTypeSlack)
	msg.Meta = models.SlackMeta{ChannelID: "C0123ABC", ThreadTS: "9999"}

	k1, _ := r.Resolve(context.Background(), msg)
	k2, _ := r.Resolve(context.Background(), msg)
	if k1 != k2 {
		t.Errorf("same thread should return same session key; got %q and %q", k1, k2)
	}
}

// ---- Cron -------------------------------------------------------------------

func TestResolver_Cron_UsesJobID(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("carol", models.ChannelTypeCron)
	msg.Meta = models.CronMeta{JobID: "daily-report"}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	expected := "user:carol:channel:cron:session:daily-report"
	if key != expected {
		t.Errorf("expected cron key %q, got %q", expected, key)
	}
}

func TestResolver_Cron_SameJobIDSameSession(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("carol", models.ChannelTypeCron)
	msg.Meta = models.CronMeta{JobID: "hourly-sync"}

	k1, _ := r.Resolve(context.Background(), msg)
	k2, _ := r.Resolve(context.Background(), msg)
	if k1 != k2 {
		t.Errorf("same job should return same session key; got %q and %q", k1, k2)
	}
}

// ---- MQTT -------------------------------------------------------------------

func TestResolver_MQTT_UsesClientID(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("dave", models.ChannelTypeMQTT)
	msg.Meta = models.MqttMeta{ClientID: "device-sensor-01", Topic: "nipper/dave/inbox"}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "device-sensor-01") {
		t.Errorf("expected key to contain client ID, got %q", key)
	}
}

// ---- RabbitMQ ---------------------------------------------------------------

func TestResolver_RabbitMQ_UsesCorrelationID(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("eve", models.ChannelTypeRabbitMQ)
	msg.Meta = models.RabbitMqMeta{
		CorrelationID: "req-abc-123",
		RoutingKey:    "nipper.eve.inbox",
	}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "req-abc-123") {
		t.Errorf("expected key to contain correlationId, got %q", key)
	}
}

func TestResolver_RabbitMQ_FallsBackToRoutingKey(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("eve", models.ChannelTypeRabbitMQ)
	msg.Meta = models.RabbitMqMeta{RoutingKey: "nipper.eve.inbox"}

	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "nipper") {
		t.Errorf("expected key to contain routing key fragment, got %q", key)
	}
}

// ---- Determinism ------------------------------------------------------------

func TestResolver_SameInputSameKey(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("frank", models.ChannelTypeSlack)
	msg.Meta = models.SlackMeta{ChannelID: "C0001"}

	k1, _ := r.Resolve(context.Background(), msg)
	k2, _ := r.Resolve(context.Background(), msg)
	if k1 != k2 {
		t.Errorf("same input should return same key; got %q and %q", k1, k2)
	}
}

// ---- Errors -----------------------------------------------------------------

func TestResolver_MissingUserIDReturnsError(t *testing.T) {
	r := newResolver(t)
	msg := &models.NipperMessage{
		MessageID:   "m1",
		ChannelType: models.ChannelTypeSlack,
	}
	_, err := r.Resolve(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for empty userId")
	}
}

// ---- sanitize integration ---------------------------------------------------

func TestResolver_KeyHasNoSpecialChars(t *testing.T) {
	r := newResolver(t)
	msg := baseMsg("henry", models.ChannelTypeWhatsApp)
	msg.Meta = models.WhatsAppMeta{ChatJID: "1555010001@s.whatsapp.net"}

	key, _ := r.Resolve(context.Background(), msg)
	// The session ID portion must not contain @ or .
	_, _, sid, err := func() (string, string, string, error) {
		parts := strings.SplitN(key, ":session:", 2)
		if len(parts) != 2 {
			return "", "", "", nil
		}
		return "", "", parts[1], nil
	}()
	if err == nil && strings.ContainsAny(sid, "@.") {
		t.Errorf("session ID must not contain @ or . characters, got %q", sid)
	}
}

func TestResolver_EmptyMetaFallsToChannelIdentity(t *testing.T) {
	r := newResolver(t)
	msg := &models.NipperMessage{
		MessageID:   "m1",
		UserID:      "ivan",
		ChannelType: models.ChannelTypeWhatsApp,
	}
	key, err := r.Resolve(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key == "" {
		t.Error("expected non-empty key even when no meta/identity provided")
	}
}
