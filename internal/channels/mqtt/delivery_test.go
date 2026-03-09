package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

// mockPublisher records publish calls and allows controlling responses.
type mockPublisher struct {
	published []publishCall
	connected bool
	err       error
}

type publishCall struct {
	Topic    string
	QoS      byte
	Retained bool
	Payload  []byte
}

func (m *mockPublisher) Publish(topic string, qos byte, retained bool, payload []byte) error {
	m.published = append(m.published, publishCall{
		Topic:    topic,
		QoS:      qos,
		Retained: retained,
		Payload:  payload,
	})
	return m.err
}

func (m *mockPublisher) IsConnected() bool {
	return m.connected
}

func newTestDeliveryClient(pub *mockPublisher) *DeliveryClient {
	return NewDeliveryClient(pub, "nipper", 1, zap.NewNop())
}

func TestDeliverResponse_HappyPath(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		ResponseID:  "resp-001",
		SessionKey:  "user:alice:channel:mqtt:session:s1",
		UserID:      "alice",
		Text:        "Deployment complete",
		ChannelType: models.ChannelTypeMQTT,
		Timestamp:   time.Now().UTC(),
		Meta: models.MqttMeta{
			Broker:        "mqtt://localhost:1883",
			Topic:         "nipper/alice/inbox",
			QoS:           1,
			CorrelationID: "corr-42",
		},
	}

	if err := dc.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(pub.published))
	}

	call := pub.published[0]
	if call.Topic != "nipper/alice/outbox" {
		t.Fatalf("expected outbox topic, got %s", call.Topic)
	}
	if call.QoS != 1 {
		t.Fatalf("expected QoS 1, got %d", call.QoS)
	}
	if call.Retained {
		t.Fatal("expected retained=false")
	}

	var payload MQTTResponsePayload
	if err := json.Unmarshal(call.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Text != "Deployment complete" {
		t.Fatalf("unexpected text: %s", payload.Text)
	}
	if payload.CorrelationID != "corr-42" {
		t.Fatalf("unexpected correlationId: %s", payload.CorrelationID)
	}
	if payload.ResponseID != "resp-001" {
		t.Fatalf("unexpected responseId: %s", payload.ResponseID)
	}
}

func TestDeliverResponse_UsesResponseTopic(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		ResponseID:  "resp-002",
		SessionKey:  "s1",
		UserID:      "alice",
		Text:        "OK",
		ChannelType: models.ChannelTypeMQTT,
		Timestamp:   time.Now().UTC(),
		Meta: models.MqttMeta{
			ResponseTopic: "devices/sensor-01/response",
			QoS:           0,
		},
	}

	if err := dc.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pub.published[0].Topic != "devices/sensor-01/response" {
		t.Fatalf("expected custom response topic, got %s", pub.published[0].Topic)
	}
	if pub.published[0].QoS != 0 {
		t.Fatalf("expected QoS 0 from meta, got %d", pub.published[0].QoS)
	}
}

func TestDeliverResponse_FallbackOutbox_NoMeta(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		ResponseID:  "resp-003",
		SessionKey:  "s1",
		UserID:      "bob",
		Text:        "done",
		ChannelType: models.ChannelTypeMQTT,
		Timestamp:   time.Now().UTC(),
	}

	if err := dc.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pub.published[0].Topic != "nipper/bob/outbox" {
		t.Fatalf("expected fallback outbox topic, got %s", pub.published[0].Topic)
	}
	if pub.published[0].QoS != 1 {
		t.Fatalf("expected default QoS 1, got %d", pub.published[0].QoS)
	}
}

func TestDeliverResponse_NilResponse(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	if err := dc.DeliverResponse(context.Background(), nil); err != nil {
		t.Fatalf("nil response should be no-op, got error: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatal("expected no publish calls for nil response")
	}
}

func TestDeliverResponse_NilPublisher(t *testing.T) {
	dc := NewDeliveryClient(nil, "nipper", 1, zap.NewNop())
	resp := &models.NipperResponse{
		UserID: "alice",
		Text:   "test",
	}
	err := dc.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for nil publisher")
	}
}

func TestDeliverResponse_NotConnected(t *testing.T) {
	pub := &mockPublisher{connected: false}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		UserID: "alice",
		Text:   "test",
	}
	err := dc.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestDeliverResponse_PublishError(t *testing.T) {
	pub := &mockPublisher{connected: true, err: fmt.Errorf("broker unavailable")}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		UserID:    "alice",
		Text:      "test",
		Timestamp: time.Now().UTC(),
	}
	err := dc.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error on publish failure")
	}
}

func TestDeliverResponse_PayloadContainsParts(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		ResponseID:  "resp-005",
		UserID:      "alice",
		Text:        "Here's the image",
		ChannelType: models.ChannelTypeMQTT,
		Timestamp:   time.Now().UTC(),
		Parts: []models.ContentPart{
			{Type: "image", URL: "https://example.com/img.png"},
		},
	}

	if err := dc.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload MQTTResponsePayload
	json.Unmarshal(pub.published[0].Payload, &payload)
	if len(payload.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(payload.Parts))
	}
}

func TestDeliverResponse_QoSClampedFromMeta(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		UserID:    "alice",
		Text:      "test",
		Timestamp: time.Now().UTC(),
		Meta:      models.MqttMeta{QoS: 2},
	}

	dc.DeliverResponse(context.Background(), resp)
	if pub.published[0].QoS != 2 {
		t.Fatalf("expected QoS 2, got %d", pub.published[0].QoS)
	}
}

func TestDeliverResponse_InvalidQoS_FallbackToDefault(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	resp := &models.NipperResponse{
		UserID:    "alice",
		Text:      "test",
		Timestamp: time.Now().UTC(),
		Meta:      models.MqttMeta{QoS: 5},
	}

	dc.DeliverResponse(context.Background(), resp)
	if pub.published[0].QoS != 1 {
		t.Fatalf("expected default QoS 1 for invalid meta QoS, got %d", pub.published[0].QoS)
	}
}

func TestNewDeliveryClient_InvalidQoS_Clamped(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := NewDeliveryClient(pub, "nipper", 99, zap.NewNop())
	if dc.defaultQoS != 1 {
		t.Fatalf("expected clamped QoS 1, got %d", dc.defaultQoS)
	}
}

func TestDeliverResponse_TimestampInRFC3339(t *testing.T) {
	pub := &mockPublisher{connected: true}
	dc := newTestDeliveryClient(pub)

	ts := time.Date(2026, 2, 22, 15, 30, 0, 0, time.UTC)
	resp := &models.NipperResponse{
		UserID:    "alice",
		Text:      "test",
		Timestamp: ts,
	}

	dc.DeliverResponse(context.Background(), resp)

	var payload MQTTResponsePayload
	json.Unmarshal(pub.published[0].Payload, &payload)
	if payload.Timestamp != "2026-02-22T15:30:00Z" {
		t.Fatalf("unexpected timestamp: %s", payload.Timestamp)
	}
}
