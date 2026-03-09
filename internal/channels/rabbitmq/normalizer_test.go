package rabbitmq

import (
	"encoding/json"
	"testing"

	"github.com/jescarri/open-nipper/internal/models"
)

var testNormCfg = NormalizerConfig{
	ExchangeInbound:  "nipper.inbound",
	ExchangeOutbound: "nipper.outbound",
}

func TestNormalizeInbound_TextMessage(t *testing.T) {
	raw := `{"text": "Analyze the test results"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
		Queue:      "nipper-user-01-inbox",
		MessageID:  "amqp-msg-001",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.ChannelType != models.ChannelTypeRabbitMQ {
		t.Fatalf("expected channelType rabbitmq, got %s", msg.ChannelType)
	}
	if msg.Content.Text != "Analyze the test results" {
		t.Fatalf("unexpected text: %q", msg.Content.Text)
	}
	if msg.UserID != "user-01" {
		t.Fatalf("unexpected userId: %s", msg.UserID)
	}
	if msg.ChannelIdentity != "rabbitmq:user-01" {
		t.Fatalf("unexpected channel identity: %s", msg.ChannelIdentity)
	}
	if msg.OriginMessageID != "amqp-msg-001" {
		t.Fatalf("unexpected originMessageId: %s", msg.OriginMessageID)
	}
}

func TestNormalizeInbound_WithCorrelationID(t *testing.T) {
	raw := `{"text": "run pipeline", "correlationId": "workflow-42"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-02.inbox",
		Queue:      "nipper-user-02-inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rmqMeta, ok := msg.Meta.(models.RabbitMqMeta)
	if !ok {
		t.Fatal("meta should be RabbitMqMeta")
	}
	if rmqMeta.CorrelationID != "workflow-42" {
		t.Fatalf("unexpected correlationId: %s", rmqMeta.CorrelationID)
	}
}

func TestNormalizeInbound_WithHeaderCorrelationID(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
		Headers:    map[string]string{"x-correlation-id": "header-corr-01"},
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rmqMeta := msg.Meta.(models.RabbitMqMeta)
	if rmqMeta.CorrelationID != "header-corr-01" {
		t.Fatalf("expected header correlation id, got: %s", rmqMeta.CorrelationID)
	}
}

func TestNormalizeInbound_PayloadCorrelationOverridesHeader(t *testing.T) {
	raw := `{"text": "hello", "correlationId": "payload-corr"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
		Headers:    map[string]string{"x-correlation-id": "header-corr"},
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rmqMeta := msg.Meta.(models.RabbitMqMeta)
	if rmqMeta.CorrelationID != "payload-corr" {
		t.Fatalf("payload correlation should take priority, got: %s", rmqMeta.CorrelationID)
	}
}

func TestNormalizeInbound_WithReplyTo(t *testing.T) {
	raw := `{"text": "run tests"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
		ReplyTo:    "results.user-01.outbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.DeliveryContext.ReplyMode != "reply-to" {
		t.Fatalf("expected reply-to mode, got: %s", msg.DeliveryContext.ReplyMode)
	}
	if msg.DeliveryContext.ChannelID != "results.user-01.outbox" {
		t.Fatalf("expected replyTo as channel ID, got: %s", msg.DeliveryContext.ChannelID)
	}
}

func TestNormalizeInbound_DirectMode_NoReplyTo(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.DeliveryContext.ReplyMode != "direct" {
		t.Fatalf("expected direct mode, got: %s", msg.DeliveryContext.ReplyMode)
	}
	if msg.DeliveryContext.ChannelID != "nipper-user-01-outbox" {
		t.Fatalf("expected default outbox channel ID, got: %s", msg.DeliveryContext.ChannelID)
	}
}

func TestNormalizeInbound_UserIDFromHeader(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "custom.routing.key",
		Headers:    map[string]string{"x-nipper-user": "alice"},
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.UserID != "alice" {
		t.Fatalf("expected userId alice from header, got: %s", msg.UserID)
	}
}

func TestNormalizeInbound_UserIDFromQueue(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "some.other.key",
		Queue:      "nipper-bob-inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.UserID != "bob" {
		t.Fatalf("expected userId bob from queue name, got: %s", msg.UserID)
	}
}

func TestNormalizeInbound_EmptyText_ReturnsNil(t *testing.T) {
	raw := `{"text": ""}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for empty text")
	}
}

func TestNormalizeInbound_WhitespaceText_ReturnsNil(t *testing.T) {
	raw := `{"text": "   "}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for whitespace-only text")
	}
}

func TestNormalizeInbound_InvalidJSON(t *testing.T) {
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	_, err := NormalizeInbound([]byte("not json"), meta, testNormCfg)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizeInbound_NoUserID(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "custom.key",
		Queue:      "some-queue",
	}

	_, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err == nil {
		t.Fatal("expected error when userId cannot be resolved")
	}
}

func TestNormalizeInbound_UniqueMessageIDs(t *testing.T) {
	raw := `{"text": "test"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg1, _ := NormalizeInbound([]byte(raw), meta, testNormCfg)
	msg2, _ := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if msg1.MessageID == msg2.MessageID {
		t.Fatal("expected unique UUIDs")
	}
}

func TestNormalizeInbound_AutoOriginMessageID(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.OriginMessageID == "" {
		t.Fatal("expected auto-generated origin message ID")
	}
}

func TestNormalizeInbound_CustomPayloadMessageID(t *testing.T) {
	raw := `{"text": "hello", "messageId": "custom-001"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.OriginMessageID != "custom-001" {
		t.Fatalf("expected custom origin messageId, got: %s", msg.OriginMessageID)
	}
}

func TestNormalizeInbound_Capabilities(t *testing.T) {
	raw := `{"text": "test"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, _ := NormalizeInbound([]byte(raw), meta, testNormCfg)
	caps := msg.DeliveryContext.Capabilities
	if caps.SupportsStreaming {
		t.Fatal("RabbitMQ should not support streaming")
	}
	if caps.SupportsMarkdown {
		t.Fatal("RabbitMQ should not support markdown")
	}
	if caps.SupportsImages {
		t.Fatal("RabbitMQ should not support images")
	}
	if caps.SupportsThreads {
		t.Fatal("RabbitMQ should not support threads")
	}
}

func TestNormalizeInbound_Serialisability(t *testing.T) {
	raw := `{"text": "serialise me", "correlationId": "corr-99"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
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

func TestNormalizeInbound_MetaFields(t *testing.T) {
	raw := `{"text": "hello"}`
	meta := InboundMeta{
		Exchange:   "nipper.inbound",
		RoutingKey: "nipper.user-01.inbox",
		ReplyTo:    "reply-queue",
		MessageID:  "amqp-123",
	}

	msg, err := NormalizeInbound([]byte(raw), meta, testNormCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rmqMeta, ok := msg.Meta.(models.RabbitMqMeta)
	if !ok {
		t.Fatal("expected RabbitMqMeta")
	}
	if rmqMeta.Exchange != "nipper.inbound" {
		t.Fatalf("unexpected exchange: %s", rmqMeta.Exchange)
	}
	if rmqMeta.RoutingKey != "nipper.user-01.inbox" {
		t.Fatalf("unexpected routingKey: %s", rmqMeta.RoutingKey)
	}
	if rmqMeta.ReplyTo != "reply-queue" {
		t.Fatalf("unexpected replyTo: %s", rmqMeta.ReplyTo)
	}
	if rmqMeta.MessageID != "amqp-123" {
		t.Fatalf("unexpected messageId: %s", rmqMeta.MessageID)
	}
}

func TestExtractUserIDFromRoutingKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"standard", "nipper.user-01.inbox", "user-01"},
		{"compound user", "nipper.user-01.inbox", "user-01"},
		{"no match prefix", "other.user-01.inbox", ""},
		{"no match suffix", "nipper.user-01.outbox", ""},
		{"too short", "nipper.inbox", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUserIDFromRoutingKey(tt.key)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestExtractUserIDFromQueue(t *testing.T) {
	tests := []struct {
		name  string
		queue string
		want  string
	}{
		{"standard", "nipper-user-01-inbox", "user-01"},
		{"no prefix", "other-user-01-inbox", ""},
		{"no suffix", "nipper-user-01-outbox", ""},
		{"empty middle", "nipper--inbox", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUserIDFromQueue(tt.queue)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestHelperFunctions(t *testing.T) {
	if got := OutboundRoutingKey("user-01"); got != "nipper.user-01.outbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := InboundRoutingKey("user-01"); got != "nipper.user-01.inbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := InboundQueueName("user-01"); got != "nipper-user-01-inbox" {
		t.Fatalf("unexpected: %s", got)
	}
	if got := OutboundQueueName("user-01"); got != "nipper-user-01-outbox" {
		t.Fatalf("unexpected: %s", got)
	}
}
