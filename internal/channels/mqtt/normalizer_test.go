package mqtt

import (
	"encoding/json"
	"testing"

	"github.com/open-nipper/open-nipper/internal/models"
)

var testNormCfg = NormalizerConfig{
	Broker:      "mqtt://localhost:1883",
	TopicPrefix: "nipper",
	DefaultQoS:  1,
}

func TestNormalizeInbound_TextMessage(t *testing.T) {
	raw := `{"text": "Hello from IoT device", "correlationId": "corr-001"}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.ChannelType != models.ChannelTypeMQTT {
		t.Fatalf("expected channelType mqtt, got %s", msg.ChannelType)
	}
	if msg.Content.Text != "Hello from IoT device" {
		t.Fatalf("unexpected text: %q", msg.Content.Text)
	}
	if msg.UserID != "user-01" {
		t.Fatalf("unexpected userId: %s", msg.UserID)
	}
	if msg.ChannelIdentity != "mqtt:user-01" {
		t.Fatalf("unexpected channel identity: %s", msg.ChannelIdentity)
	}
	if msg.DeliveryContext.ReplyMode != "direct" {
		t.Fatalf("unexpected replyMode: %s", msg.DeliveryContext.ReplyMode)
	}
	if msg.DeliveryContext.ChannelType != models.ChannelTypeMQTT {
		t.Fatalf("unexpected delivery channel type: %s", msg.DeliveryContext.ChannelType)
	}
	if msg.DeliveryContext.ChannelID != topic {
		t.Fatalf("unexpected delivery channel ID: %s", msg.DeliveryContext.ChannelID)
	}

	meta, ok := msg.Meta.(models.MqttMeta)
	if !ok {
		t.Fatal("meta should be MqttMeta")
	}
	if meta.CorrelationID != "corr-001" {
		t.Fatalf("unexpected correlationId: %s", meta.CorrelationID)
	}
	if meta.Broker != "mqtt://localhost:1883" {
		t.Fatalf("unexpected broker: %s", meta.Broker)
	}
	if meta.Topic != topic {
		t.Fatalf("unexpected topic: %s", meta.Topic)
	}
	if meta.QoS != 1 {
		t.Fatalf("unexpected qos: %d", meta.QoS)
	}
}

func TestNormalizeInbound_WithResponseTopic(t *testing.T) {
	raw := `{"text": "status query", "responseTopic": "devices/sensor-01/response"}`
	topic := "nipper/user-02/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	meta, ok := msg.Meta.(models.MqttMeta)
	if !ok {
		t.Fatal("expected MqttMeta")
	}
	if meta.ResponseTopic != "devices/sensor-01/response" {
		t.Fatalf("unexpected response topic: %s", meta.ResponseTopic)
	}
}

func TestNormalizeInbound_WithCustomMessageID(t *testing.T) {
	raw := `{"text": "hello", "messageId": "custom-msg-001"}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.OriginMessageID != "custom-msg-001" {
		t.Fatalf("expected custom origin messageId, got: %s", msg.OriginMessageID)
	}
}

func TestNormalizeInbound_EmptyText_ReturnsNil(t *testing.T) {
	raw := `{"text": ""}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for empty text")
	}
}

func TestNormalizeInbound_WhitespaceText_ReturnsNil(t *testing.T) {
	raw := `{"text": "   "}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for whitespace-only text")
	}
}

func TestNormalizeInbound_InvalidJSON(t *testing.T) {
	raw := `not json`
	topic := "nipper/user-01/inbox"

	_, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizeInbound_InvalidTopic_MissingPrefix(t *testing.T) {
	raw := `{"text": "hello"}`
	topic := "other/user-01/inbox"

	_, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err == nil {
		t.Fatal("expected error for wrong topic prefix")
	}
}

func TestNormalizeInbound_InvalidTopic_NoUserID(t *testing.T) {
	raw := `{"text": "hello"}`
	topic := "nipper/"

	_, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err == nil {
		t.Fatal("expected error for missing user ID in topic")
	}
}

func TestNormalizeInbound_UUIDGenerated(t *testing.T) {
	raw := `{"text": "test"}`
	topic := "nipper/user-01/inbox"

	msg1, _ := NormalizeInbound([]byte(raw), topic, testNormCfg)
	msg2, _ := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if msg1.MessageID == msg2.MessageID {
		t.Fatal("expected unique UUIDs for each normalized message")
	}
}

func TestNormalizeInbound_Capabilities(t *testing.T) {
	raw := `{"text": "test"}`
	topic := "nipper/user-01/inbox"

	msg, _ := NormalizeInbound([]byte(raw), topic, testNormCfg)
	caps := msg.DeliveryContext.Capabilities
	if caps.SupportsStreaming {
		t.Fatal("MQTT should not support streaming")
	}
	if caps.SupportsMarkdown {
		t.Fatal("MQTT should not support markdown")
	}
	if caps.SupportsImages {
		t.Fatal("MQTT should not support images")
	}
	if caps.SupportsThreads {
		t.Fatal("MQTT should not support threads")
	}
	if caps.SupportsReactions {
		t.Fatal("MQTT should not support reactions")
	}
}

func TestNormalizeInbound_Serialisability(t *testing.T) {
	raw := `{"text": "serialise me", "correlationId": "corr-99"}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

func TestNormalizeInbound_CustomTopicPrefix(t *testing.T) {
	cfg := NormalizerConfig{
		Broker:      "mqtt://broker:1883",
		TopicPrefix: "myapp",
		DefaultQoS:  0,
	}
	raw := `{"text": "hello"}`
	topic := "myapp/alice/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.UserID != "alice" {
		t.Fatalf("expected userId alice, got %s", msg.UserID)
	}
}

func TestExtractUserIDFromTopic(t *testing.T) {
	tests := []struct {
		name   string
		topic  string
		prefix string
		want   string
		err    bool
	}{
		{"standard", "nipper/user-01/inbox", "nipper", "user-01", false},
		{"trailing slash prefix", "nipper/alice/inbox", "nipper/", "alice", false},
		{"custom prefix", "myapp/bob/inbox", "myapp", "bob", false},
		{"wrong prefix", "other/user/inbox", "nipper", "", true},
		{"empty user", "nipper//inbox", "nipper", "", true},
		{"just prefix", "nipper/", "nipper", "", true},
		{"no slash", "nipperuser", "nipper", "", true},
		{"empty prefix defaults", "nipper/user/inbox", "", "user", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractUserIDFromTopic(tt.topic, tt.prefix)
			if tt.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestOutboxTopic(t *testing.T) {
	if got := OutboxTopic("nipper", "user-01"); got != "nipper/user-01/outbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := OutboxTopic("nipper/", "alice"); got != "nipper/alice/outbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := OutboxTopic("", "bob"); got != "nipper/bob/outbox" {
		t.Fatalf("unexpected (empty defaults to nipper): %s", got)
	}
}

func TestInboxTopic(t *testing.T) {
	if got := InboxTopic("nipper", "user-01"); got != "nipper/user-01/inbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := InboxTopic("", "bob"); got != "nipper/bob/inbox" {
		t.Fatalf("unexpected (empty defaults to nipper): %s", got)
	}
}

func TestNormalizeInbound_AutoGeneratedOriginMessageID(t *testing.T) {
	raw := `{"text": "no explicit messageId"}`
	topic := "nipper/user-01/inbox"

	msg, err := NormalizeInbound([]byte(raw), topic, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.OriginMessageID == "" {
		t.Fatal("expected auto-generated origin message ID")
	}
	if len(msg.OriginMessageID) < 10 {
		t.Fatalf("origin message ID seems too short: %s", msg.OriginMessageID)
	}
}
