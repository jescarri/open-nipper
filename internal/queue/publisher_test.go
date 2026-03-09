package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// publisherWithMockChannel bypasses the broker and injects a mock channel directly.
func publisherWithMockChannel(ch AMQPChannel) *RabbitMQPublisher {
	p := &RabbitMQPublisher{
		broker: nil, // not used — we set the channel directly
		logger: zap.NewNop(),
		ch:     ch,
	}
	return p
}

// --- helpers ---

func makeQueueItem(userID, sessionKey string, mode models.QueueMode, priority int) *models.QueueItem {
	return &models.QueueItem{
		ID:   "item-001",
		Mode: mode,
		Priority: priority,
		EnqueuedAt: time.Now(),
		Message: &models.NipperMessage{
			MessageID:   "msg-001",
			UserID:      userID,
			SessionKey:  sessionKey,
			ChannelType: models.ChannelTypeWhatsApp,
		},
	}
}

// --- tests ---

func TestPublishMessage_RoutingKey(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeSteer, 0)
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	if len(ch.publishes) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(ch.publishes))
	}
	pub := ch.publishes[0]
	wantKey := "nipper.sessions.user-01.session-abc"
	if pub.key != wantKey {
		t.Errorf("routing key: got %q, want %q", pub.key, wantKey)
	}
	if pub.exchange != ExchangeSessions {
		t.Errorf("exchange: got %q, want %q", pub.exchange, ExchangeSessions)
	}
}

func TestPublishMessage_PersistentDelivery(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeQueue, 0)
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	pub := ch.publishes[0]
	if pub.msg.DeliveryMode != amqp.Persistent {
		t.Errorf("expected Persistent delivery mode (2), got %d", pub.msg.DeliveryMode)
	}
}

func TestPublishMessage_Headers(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeCollect, 5)
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	pub := ch.publishes[0]
	headers := pub.msg.Headers

	checkHeader := func(key, want string) {
		t.Helper()
		got, ok := headers[key]
		if !ok {
			t.Errorf("header %q missing", key)
			return
		}
		if fmt.Sprintf("%v", got) != want {
			t.Errorf("header %q: got %v, want %s", key, got, want)
		}
	}

	checkHeader("x-nipper-user-id", "user-01")
	checkHeader("x-nipper-session-key", "session-abc")
	checkHeader("x-nipper-queue-mode", "collect")
}

func TestPublishMessage_MessageID(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeSteer, 0)
	item.ID = "unique-item-id"
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	pub := ch.publishes[0]
	if pub.msg.MessageId != "unique-item-id" {
		t.Errorf("MessageId: got %q, want %q", pub.msg.MessageId, "unique-item-id")
	}
}

func TestPublishMessage_ContentType(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeSteer, 0)
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	pub := ch.publishes[0]
	if pub.msg.ContentType != "application/json" {
		t.Errorf("ContentType: got %q, want application/json", pub.msg.ContentType)
	}
}

func TestPublishMessage_BodyDeserializable(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	item := makeQueueItem("user-01", "session-abc", models.QueueModeInterrupt, 10)
	if err := p.PublishMessage(context.Background(), item); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}

	pub := ch.publishes[0]
	var decoded models.QueueItem
	if err := json.Unmarshal(pub.msg.Body, &decoded); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if decoded.ID != item.ID {
		t.Errorf("decoded ID: got %q, want %q", decoded.ID, item.ID)
	}
	if decoded.Mode != item.Mode {
		t.Errorf("decoded Mode: got %q, want %q", decoded.Mode, item.Mode)
	}
}

func TestPublishMessage_NilItem(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	err := p.PublishMessage(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil item")
	}
}

func TestPublishMessage_ChannelError(t *testing.T) {
	ch := &mockChannel{publishErr: fmt.Errorf("channel closed")}
	p := publisherWithMockChannel(ch)

	// After a publish error the cached channel is cleared.
	item := makeQueueItem("user-01", "session-abc", models.QueueModeSteer, 0)
	err := p.PublishMessage(context.Background(), item)
	if err == nil {
		t.Fatal("expected error when channel publish fails")
	}

	// Channel should be cleared so the next call attempts to get a fresh one.
	p.mu.Lock()
	if p.ch != nil {
		t.Error("channel should be nil after publish error")
	}
	p.mu.Unlock()
}

// --- PublishControl tests ---

func TestPublishControl_RoutingKey(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	msg := &models.ControlMessage{
		Type:      models.ControlMessageInterrupt,
		UserID:    "user-01",
		Timestamp: time.Now(),
	}
	if err := p.PublishControl(context.Background(), "user-01", msg); err != nil {
		t.Fatalf("PublishControl: %v", err)
	}

	if len(ch.publishes) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(ch.publishes))
	}
	pub := ch.publishes[0]
	wantKey := "nipper.control.user-01"
	if pub.key != wantKey {
		t.Errorf("routing key: got %q, want %q", pub.key, wantKey)
	}
	if pub.exchange != ExchangeControl {
		t.Errorf("exchange: got %q, want %q", pub.exchange, ExchangeControl)
	}
}

func TestPublishControl_BodyDeserializable(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	msg := &models.ControlMessage{
		Type:      models.ControlMessageAbort,
		UserID:    "user-01",
		SessionKey: "session-xyz",
		Timestamp: time.Now(),
	}
	if err := p.PublishControl(context.Background(), "user-01", msg); err != nil {
		t.Fatalf("PublishControl: %v", err)
	}

	pub := ch.publishes[0]
	var decoded models.ControlMessage
	if err := json.Unmarshal(pub.msg.Body, &decoded); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if decoded.Type != models.ControlMessageAbort {
		t.Errorf("decoded Type: got %q, want abort", decoded.Type)
	}
}

func TestPublishControl_NilMessage(t *testing.T) {
	ch := &mockChannel{}
	p := publisherWithMockChannel(ch)

	err := p.PublishControl(context.Background(), "user-01", nil)
	if err == nil {
		t.Fatal("expected error for nil control message")
	}
}

// --- broker URL tests (placed here for simplicity) ---

func TestBuildAMQPURL_Basic(t *testing.T) {
	cfg := &config.QueueRabbitMQConfig{
		URL: "amqp://localhost:5672",
	}
	got, err := buildAMQPURL(cfg)
	if err != nil {
		t.Fatalf("buildAMQPURL: %v", err)
	}
	if got != "amqp://localhost:5672" {
		t.Errorf("got %q, want %q", got, "amqp://localhost:5672")
	}
}

func TestBuildAMQPURL_WithCredentials(t *testing.T) {
	cfg := &config.QueueRabbitMQConfig{
		URL:      "amqp://localhost:5672",
		Username: "guest",
		Password: "secret",
	}
	got, err := buildAMQPURL(cfg)
	if err != nil {
		t.Fatalf("buildAMQPURL: %v", err)
	}
	want := "amqp://guest:secret@localhost:5672"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAMQPURL_WithVHost(t *testing.T) {
	cfg := &config.QueueRabbitMQConfig{
		URL:      "amqp://localhost:5672",
		Username: "guest",
		Password: "pass",
		VHost:    "nipper",
	}
	got, err := buildAMQPURL(cfg)
	if err != nil {
		t.Fatalf("buildAMQPURL: %v", err)
	}
	want := "amqp://guest:pass@localhost:5672/nipper"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAMQPURL_WithSlashVHost(t *testing.T) {
	cfg := &config.QueueRabbitMQConfig{
		URL:      "amqp://localhost:5672",
		Username: "user",
		Password: "pass",
		VHost:    "/nipper",
	}
	got, err := buildAMQPURL(cfg)
	if err != nil {
		t.Fatalf("buildAMQPURL: %v", err)
	}
	// /nipper vhost should be encoded as %2Fnipper
	want := "amqp://user:pass@localhost:5672/%2Fnipper"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildAMQPURL_EmptyURL(t *testing.T) {
	cfg := &config.QueueRabbitMQConfig{URL: ""}
	_, err := buildAMQPURL(cfg)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}
