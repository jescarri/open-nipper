package mqtt

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"

	channelpkg "github.com/open-nipper/open-nipper/internal/channels"
	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// Compile-time check: Adapter implements ChannelAdapter.
var _ channelpkg.ChannelAdapter = (*Adapter)(nil)

// --- Mock MQTT Token ---

type mockToken struct {
	err error
}

func (t *mockToken) Wait() bool                         { return true }
func (t *mockToken) WaitTimeout(_ time.Duration) bool   { return true }
func (t *mockToken) Done() <-chan struct{}               { ch := make(chan struct{}); close(ch); return ch }
func (t *mockToken) Error() error                       { return t.err }

// --- Mock MQTT Client ---

type mockMQTTClient struct {
	connected      bool
	subscriptions  []string
	published      []publishCall
	connectHandler pahomqtt.OnConnectHandler
	msgHandler     pahomqtt.MessageHandler
	mu             sync.Mutex
	connectErr     error
	subscribeErr   error
	publishErr     error
}

func (m *mockMQTTClient) IsConnected() bool { return m.connected }
func (m *mockMQTTClient) IsConnectionOpen() bool { return m.connected }

func (m *mockMQTTClient) Connect() pahomqtt.Token {
	if m.connectErr == nil {
		m.connected = true
		if m.connectHandler != nil {
			m.connectHandler(m)
		}
	}
	return &mockToken{err: m.connectErr}
}

func (m *mockMQTTClient) Disconnect(quiesce uint) {
	m.connected = false
}

func (m *mockMQTTClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribeErr == nil {
		m.subscriptions = append(m.subscriptions, topic)
	}
	return &mockToken{err: m.subscribeErr}
}

func (m *mockMQTTClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return &mockToken{}
}

func (m *mockMQTTClient) Unsubscribe(topics ...string) pahomqtt.Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, topic := range topics {
		for i, s := range m.subscriptions {
			if s == topic {
				m.subscriptions = append(m.subscriptions[:i], m.subscriptions[i+1:]...)
				break
			}
		}
	}
	return &mockToken{}
}

func (m *mockMQTTClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	var data []byte
	switch v := payload.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	}
	m.published = append(m.published, publishCall{
		Topic:    topic,
		QoS:      qos,
		Retained: retained,
		Payload:  data,
	})
	return &mockToken{err: m.publishErr}
}

func (m *mockMQTTClient) AddRoute(topic string, callback pahomqtt.MessageHandler)         {}
func (m *mockMQTTClient) OptionsReader() pahomqtt.ClientOptionsReader { return pahomqtt.ClientOptionsReader{} }

// --- Mock MQTT Message ---

type mockMQTTMessage struct {
	topic   string
	payload []byte
	qos     byte
}

func (m *mockMQTTMessage) Duplicate() bool    { return false }
func (m *mockMQTTMessage) Qos() byte          { return m.qos }
func (m *mockMQTTMessage) Retained() bool     { return false }
func (m *mockMQTTMessage) Topic() string      { return m.topic }
func (m *mockMQTTMessage) MessageID() uint16  { return 0 }
func (m *mockMQTTMessage) Payload() []byte    { return m.payload }
func (m *mockMQTTMessage) Ack()               {}

// --- Test helpers ---

func testMQTTConfig() config.MQTTConfig {
	return config.MQTTConfig{
		Broker:       "mqtt://localhost:1883",
		ClientID:     "nipper-test",
		TopicPrefix:  "nipper",
		QoS:          1,
		CleanSession: false,
		KeepAlive:    60,
		Reconnect: config.ReconnectConfig{
			Enabled:        true,
			InitialDelayMS: 100,
			MaxDelayMS:     1000,
		},
	}
}

func newTestAdapterWithMock(t *testing.T) (*Adapter, *mockMQTTClient) {
	t.Helper()
	mock := &mockMQTTClient{connected: false}

	a := NewAdapter(AdapterDeps{
		Config: testMQTTConfig(),
		Logger: zap.NewNop(),
		UserLister: func(ctx context.Context) ([]string, error) {
			return []string{"alice", "bob"}, nil
		},
		ClientFactory: func(opts *pahomqtt.ClientOptions) pahomqtt.Client {
			mock.connectHandler = opts.OnConnect
			mock.msgHandler = opts.DefaultPublishHandler
			return mock
		},
	})
	return a, mock
}

// --- Tests ---

func TestAdapter_ChannelType(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	if a.ChannelType() != models.ChannelTypeMQTT {
		t.Fatalf("expected mqtt, got %s", a.ChannelType())
	}
}

func TestAdapter_InterfaceCompliance(t *testing.T) {
	var _ channelpkg.ChannelAdapter = &Adapter{}
}

func TestAdapter_Start_ConnectsAndSubscribes(t *testing.T) {
	a, mock := newTestAdapterWithMock(t)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.connected {
		t.Fatal("expected client to be connected")
	}
	if !a.IsConnected() {
		t.Fatal("expected adapter to report connected")
	}

	if len(mock.subscriptions) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d: %v", len(mock.subscriptions), mock.subscriptions)
	}
	if mock.subscriptions[0] != "nipper/alice/inbox" {
		t.Fatalf("unexpected subscription: %s", mock.subscriptions[0])
	}
	if mock.subscriptions[1] != "nipper/bob/inbox" {
		t.Fatalf("unexpected subscription: %s", mock.subscriptions[1])
	}
}

func TestAdapter_Start_ConnectError_WarnsButContinues(t *testing.T) {
	mock := &mockMQTTClient{connectErr: nil}
	a := NewAdapter(AdapterDeps{
		Config: testMQTTConfig(),
		Logger: zap.NewNop(),
		ClientFactory: func(opts *pahomqtt.ClientOptions) pahomqtt.Client {
			mock.connectErr = nil
			return mock
		},
	})

	err := a.Start(context.Background())
	if err != nil {
		t.Fatalf("start should not return error: %v", err)
	}
}

func TestAdapter_Stop_Disconnects(t *testing.T) {
	a, mock := newTestAdapterWithMock(t)
	a.Start(context.Background())

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.connected {
		t.Fatal("expected client to be disconnected after stop")
	}
	if a.IsConnected() {
		t.Fatal("expected adapter to report disconnected")
	}
}

func TestAdapter_HealthCheck_Connected(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	a.Start(context.Background())

	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check should pass when connected: %v", err)
	}
}

func TestAdapter_HealthCheck_NotConnected(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	// don't call Start

	err := a.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestAdapter_HealthCheck_NilClient(t *testing.T) {
	a := &Adapter{logger: zap.NewNop()}
	err := a.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}

func TestAdapter_NormalizeInbound_ValidWrapper(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	wrapper := map[string]interface{}{
		"_topic":   "nipper/alice/inbox",
		"_payload": map[string]string{"text": "hello from IoT"},
	}
	raw, _ := json.Marshal(wrapper)

	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.UserID != "alice" {
		t.Fatalf("expected userId alice, got %s", msg.UserID)
	}
	if msg.Content.Text != "hello from IoT" {
		t.Fatalf("unexpected text: %s", msg.Content.Text)
	}
}

func TestAdapter_NormalizeInbound_MissingTopic(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	raw := `{"_payload": {"text": "test"}}`
	_, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err == nil {
		t.Fatal("expected error for missing _topic")
	}
}

func TestAdapter_NormalizeInbound_MissingPayload(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	raw := `{"_topic": "nipper/alice/inbox"}`
	_, err := a.NormalizeInbound(context.Background(), []byte(raw))
	if err == nil {
		t.Fatal("expected error for missing _payload")
	}
}

func TestAdapter_NormalizeInbound_InvalidJSON(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	_, err := a.NormalizeInbound(context.Background(), []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestAdapter_DeliverEvent_IsNoOp(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	err := a.DeliverEvent(context.Background(), &models.NipperEvent{
		Type: models.EventTypeDelta,
	})
	if err != nil {
		t.Fatalf("DeliverEvent should be no-op: %v", err)
	}
}

func TestAdapter_DeliverResponse_NilSafe(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	a.Start(context.Background())

	if err := a.DeliverResponse(context.Background(), nil); err != nil {
		t.Fatalf("nil response should be no-op: %v", err)
	}
}

func TestAdapter_DeliverResponse_Publishes(t *testing.T) {
	a, mock := newTestAdapterWithMock(t)
	a.Start(context.Background())

	resp := &models.NipperResponse{
		ResponseID:  "resp-001",
		UserID:      "alice",
		Text:        "done",
		ChannelType: models.ChannelTypeMQTT,
		Timestamp:   time.Now().UTC(),
		Meta: models.MqttMeta{
			QoS: 1,
		},
	}

	if err := a.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(mock.published))
	}
	if mock.published[0].Topic != "nipper/alice/outbox" {
		t.Fatalf("unexpected topic: %s", mock.published[0].Topic)
	}
}

func TestAdapter_OnMessage_HandlerCalled(t *testing.T) {
	var received *models.NipperMessage
	var mu sync.Mutex

	a, _ := newTestAdapterWithMock(t)
	a.SetHandler(func(ctx context.Context, msg *models.NipperMessage) error {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	a.Start(context.Background())

	mqttMsg := &mockMQTTMessage{
		topic:   "nipper/alice/inbox",
		payload: []byte(`{"text": "sensor data"}`),
	}
	a.onMessage(nil, mqttMsg)

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("expected handler to be called")
	}
	if received.UserID != "alice" {
		t.Fatalf("expected userId alice, got %s", received.UserID)
	}
	if received.Content.Text != "sensor data" {
		t.Fatalf("unexpected text: %s", received.Content.Text)
	}
}

func TestAdapter_OnMessage_NoHandler(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	a.Start(context.Background())
	// No handler set — should not panic
	a.onMessage(nil, &mockMQTTMessage{
		topic:   "nipper/alice/inbox",
		payload: []byte(`{"text": "test"}`),
	})
}

func TestAdapter_OnMessage_NormalizeError(t *testing.T) {
	called := false
	a, _ := newTestAdapterWithMock(t)
	a.SetHandler(func(ctx context.Context, msg *models.NipperMessage) error {
		called = true
		return nil
	})
	a.Start(context.Background())

	a.onMessage(nil, &mockMQTTMessage{
		topic:   "nipper/alice/inbox",
		payload: []byte(`not json`),
	})
	if called {
		t.Fatal("handler should not be called on normalize error")
	}
}

func TestAdapter_OnMessage_EmptyText_Ignored(t *testing.T) {
	called := false
	a, _ := newTestAdapterWithMock(t)
	a.SetHandler(func(ctx context.Context, msg *models.NipperMessage) error {
		called = true
		return nil
	})
	a.Start(context.Background())

	a.onMessage(nil, &mockMQTTMessage{
		topic:   "nipper/alice/inbox",
		payload: []byte(`{"text": ""}`),
	})
	if called {
		t.Fatal("handler should not be called for empty text")
	}
}

func TestAdapter_Subscriptions_CopySemantics(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	a.Start(context.Background())

	subs := a.Subscriptions()
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	subs[0] = "modified"
	if a.Subscriptions()[0] == "modified" {
		t.Fatal("Subscriptions should return a copy")
	}
}

func TestAdapter_SubscriberCount(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)
	a.Start(context.Background())

	if a.SubscriberCount() != 2 {
		t.Fatalf("expected 2, got %d", a.SubscriberCount())
	}
}

func TestAdapter_InboxOutboxTopics(t *testing.T) {
	a, _ := newTestAdapterWithMock(t)

	if got := a.InboxTopicForUser("alice"); got != "nipper/alice/inbox" {
		t.Fatalf("unexpected inbox: %s", got)
	}
	if got := a.OutboxTopicForUser("bob"); got != "nipper/bob/outbox" {
		t.Fatalf("unexpected outbox: %s", got)
	}
}

func TestAdapter_NoUserLister_SkipsSubscriptions(t *testing.T) {
	mock := &mockMQTTClient{connected: false}
	a := NewAdapter(AdapterDeps{
		Config: testMQTTConfig(),
		Logger: zap.NewNop(),
		ClientFactory: func(opts *pahomqtt.ClientOptions) pahomqtt.Client {
			mock.connectHandler = opts.OnConnect
			return mock
		},
	})

	a.Start(context.Background())
	if len(mock.subscriptions) != 0 {
		t.Fatalf("expected 0 subscriptions without user lister, got %d", len(mock.subscriptions))
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg := config.MQTTConfig{
		Broker:      "mqtt://localhost:1883",
		ClientID:    "nipper-test",
		TopicPrefix: "nipper",
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MissingBroker(t *testing.T) {
	cfg := config.MQTTConfig{ClientID: "test", TopicPrefix: "nipper"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing broker")
	}
}

func TestValidate_MissingClientID(t *testing.T) {
	cfg := config.MQTTConfig{Broker: "mqtt://localhost:1883", TopicPrefix: "nipper"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing client_id")
	}
}

func TestValidate_MissingTopicPrefix(t *testing.T) {
	cfg := config.MQTTConfig{Broker: "mqtt://localhost:1883", ClientID: "test"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for missing topic_prefix")
	}
}

func TestRedactBrokerURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"mqtt://localhost:1883", "mqtt://localhost:1883"},
		{"mqtt://user:pass@broker:1883", "mqtt://[REDACTED]@broker:1883"},
		{"mqtts://admin:secret@secure.broker.io:8883", "mqtts://[REDACTED]@secure.broker.io:8883"},
	}
	for _, tt := range tests {
		got := RedactBrokerURL(tt.input)
		if got != tt.expected {
			t.Errorf("RedactBrokerURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestAdapter_DeliverResponse_NotStarted(t *testing.T) {
	a := NewAdapter(AdapterDeps{
		Config: testMQTTConfig(),
		Logger: zap.NewNop(),
	})
	resp := &models.NipperResponse{UserID: "alice", Text: "test"}
	err := a.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error when delivery client not initialized")
	}
}

func TestAdapter_OnConnect_ResubscribesAll(t *testing.T) {
	mock := &mockMQTTClient{connected: false}
	var connectCb pahomqtt.OnConnectHandler

	a := NewAdapter(AdapterDeps{
		Config: testMQTTConfig(),
		Logger: zap.NewNop(),
		UserLister: func(ctx context.Context) ([]string, error) {
			return []string{"alice"}, nil
		},
		ClientFactory: func(opts *pahomqtt.ClientOptions) pahomqtt.Client {
			connectCb = opts.OnConnect
			mock.connectHandler = opts.OnConnect
			return mock
		},
	})
	a.Start(context.Background())

	mock.mu.Lock()
	initialSubs := len(mock.subscriptions)
	mock.mu.Unlock()

	if initialSubs != 1 {
		t.Fatalf("expected 1 initial subscription, got %d", initialSubs)
	}

	// Simulate reconnect
	mock.mu.Lock()
	mock.subscriptions = nil
	mock.mu.Unlock()
	connectCb(mock)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.subscriptions) != 1 {
		t.Fatalf("expected resubscribe on reconnect, got %d", len(mock.subscriptions))
	}
}

func TestMQTTCapabilities(t *testing.T) {
	caps := models.MQTTCapabilities()
	if caps.SupportsStreaming {
		t.Fatal("MQTT should not support streaming")
	}
	if caps.SupportsMarkdown {
		t.Fatal("MQTT should not support markdown")
	}
	if caps.SupportsImages {
		t.Fatal("MQTT should not support images")
	}
	if caps.SupportsDocuments {
		t.Fatal("MQTT should not support documents")
	}
	if caps.SupportsAudio {
		t.Fatal("MQTT should not support audio")
	}
	if caps.SupportsReactions {
		t.Fatal("MQTT should not support reactions")
	}
	if caps.SupportsThreads {
		t.Fatal("MQTT should not support threads")
	}
	if caps.SupportsMessageEdits {
		t.Fatal("MQTT should not support message edits")
	}
}
