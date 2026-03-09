package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

type mockPublisher struct {
	published []publishCall
	connected bool
	err       error
}

type publishCall struct {
	Exchange   string
	RoutingKey string
	Mandatory  bool
	Immediate  bool
	Body       []byte
	Headers    map[string]interface{}
}

func (m *mockPublisher) Publish(exchange, routingKey string, mandatory, immediate bool, body []byte, headers map[string]interface{}) error {
	if m.err != nil {
		return m.err
	}
	m.published = append(m.published, publishCall{
		Exchange:   exchange,
		RoutingKey: routingKey,
		Mandatory:  mandatory,
		Immediate:  immediate,
		Body:       body,
		Headers:    headers,
	})
	return nil
}

func (m *mockPublisher) IsConnected() bool {
	return m.connected
}

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestDeliverResponse_BasicText(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-001",
		SessionKey: "user:user-01:channel:rabbitmq:session:sess-01",
		UserID:     "user-01",
		Text:       "Test results: all passed",
		Timestamp:  time.Date(2026, 2, 22, 10, 0, 0, 0, time.UTC),
	}

	if err := client.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.published))
	}

	call := pub.published[0]
	if call.Exchange != "nipper.outbound" {
		t.Fatalf("expected exchange nipper.outbound, got %s", call.Exchange)
	}
	if call.RoutingKey != "nipper.user-01.outbox" {
		t.Fatalf("expected routing key nipper.user-01.outbox, got %s", call.RoutingKey)
	}

	var payload RabbitMQResponsePayload
	if err := json.Unmarshal(call.Body, &payload); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if payload.ResponseID != "resp-001" {
		t.Fatalf("unexpected responseId: %s", payload.ResponseID)
	}
	if payload.Text != "Test results: all passed" {
		t.Fatalf("unexpected text: %s", payload.Text)
	}
	if payload.UserID != "user-01" {
		t.Fatalf("unexpected userId: %s", payload.UserID)
	}
}

func TestDeliverResponse_WithReplyTo(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-002",
		UserID:     "user-01",
		Text:       "reply content",
		Timestamp:  time.Now(),
		Meta: models.RabbitMqMeta{
			ReplyTo:       "results.user-01.outbox",
			CorrelationID: "corr-42",
		},
	}

	if err := client.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call := pub.published[0]
	if call.Exchange != "" {
		t.Fatalf("expected empty exchange for direct queue publish, got %s", call.Exchange)
	}
	if call.RoutingKey != "results.user-01.outbox" {
		t.Fatalf("expected replyTo as routing key, got %s", call.RoutingKey)
	}

	var payload RabbitMQResponsePayload
	json.Unmarshal(call.Body, &payload)
	if payload.CorrelationID != "corr-42" {
		t.Fatalf("expected correlation ID in payload, got: %s", payload.CorrelationID)
	}
}

func TestDeliverResponse_WithCorrelationIDHeader(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-003",
		UserID:     "user-01",
		Text:       "test",
		Timestamp:  time.Now(),
		Meta: models.RabbitMqMeta{
			CorrelationID: "workflow-789",
		},
	}

	client.DeliverResponse(context.Background(), resp)

	call := pub.published[0]
	if call.Headers["x-correlation-id"] != "workflow-789" {
		t.Fatalf("expected x-correlation-id header, got: %v", call.Headers["x-correlation-id"])
	}
}

func TestDeliverResponse_NilResponse(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	if err := client.DeliverResponse(context.Background(), nil); err != nil {
		t.Fatalf("nil response should not error: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatal("should not publish for nil response")
	}
}

func TestDeliverResponse_NilPublisher(t *testing.T) {
	client := NewDeliveryClient(nil, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-004",
		UserID:     "user-01",
		Text:       "test",
		Timestamp:  time.Now(),
	}

	if err := client.DeliverResponse(context.Background(), resp); err == nil {
		t.Fatal("expected error for nil publisher")
	}
}

func TestDeliverResponse_NotConnected(t *testing.T) {
	pub := &mockPublisher{connected: false}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-005",
		UserID:     "user-01",
		Text:       "test",
		Timestamp:  time.Now(),
	}

	if err := client.DeliverResponse(context.Background(), resp); err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestDeliverResponse_PublishError(t *testing.T) {
	pub := &mockPublisher{connected: true, err: fmt.Errorf("connection reset")}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-006",
		UserID:     "user-01",
		Text:       "test",
		Timestamp:  time.Now(),
	}

	if err := client.DeliverResponse(context.Background(), resp); err == nil {
		t.Fatal("expected publish error to propagate")
	}
}

func TestDeliverResponse_HeadersAlwaysPresent(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID: "resp-007",
		UserID:     "user-01",
		Text:       "test",
		Timestamp:  time.Now(),
	}

	client.DeliverResponse(context.Background(), resp)

	call := pub.published[0]
	if call.Headers["x-nipper-response-id"] != "resp-007" {
		t.Fatalf("expected x-nipper-response-id header")
	}
	if call.Headers["content-type"] != "application/json" {
		t.Fatalf("expected content-type header")
	}
}

func TestDeliverResponse_OriginMessageID(t *testing.T) {
	pub := &mockPublisher{connected: true}
	client := NewDeliveryClient(pub, "nipper.outbound", testLogger())

	resp := &models.NipperResponse{
		ResponseID:      "resp-008",
		UserID:          "user-01",
		Text:            "reply",
		OriginMessageID: "original-msg-001",
		Timestamp:       time.Now(),
	}

	client.DeliverResponse(context.Background(), resp)

	var payload RabbitMQResponsePayload
	json.Unmarshal(pub.published[0].Body, &payload)
	if payload.InReplyTo != "original-msg-001" {
		t.Fatalf("expected inReplyTo, got: %s", payload.InReplyTo)
	}
}
