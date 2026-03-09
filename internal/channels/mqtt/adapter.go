// Package mqtt implements the ChannelAdapter for MQTT-based IoT/M2M messaging.
//
// The adapter connects to an MQTT broker using paho.mqtt.golang, subscribes to
// per-user inbox topics ({topicPrefix}/{userId}/inbox), normalizes inbound JSON
// messages into NipperMessages, and delivers responses to outbox topics or
// responseTopic specified in the inbound message metadata.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// MessageHandler is the callback invoked when an MQTT message is received.
type MessageHandler func(ctx context.Context, msg *models.NipperMessage) error

// UserLister returns the IDs of all users that should have MQTT subscriptions.
type UserLister func(ctx context.Context) ([]string, error)

// ClientFactory creates paho MQTT client instances (seam for testing).
type ClientFactory func(opts *pahomqtt.ClientOptions) pahomqtt.Client

// Adapter implements channels.ChannelAdapter for the MQTT channel.
type Adapter struct {
	cfg           config.MQTTConfig
	client        pahomqtt.Client
	delivery      *DeliveryClient
	normCfg       NormalizerConfig
	logger        *zap.Logger
	handler       MessageHandler
	userLister    UserLister
	clientFactory ClientFactory

	mu          sync.Mutex
	subscribers []string
	connected   bool
}

// AdapterDeps bundles the dependencies for constructing an MQTT Adapter.
type AdapterDeps struct {
	Config        config.MQTTConfig
	Logger        *zap.Logger
	UserLister    UserLister
	ClientFactory ClientFactory
}

// NewAdapter creates an MQTT adapter. Call SetHandler before Start.
func NewAdapter(deps AdapterDeps) *Adapter {
	factory := deps.ClientFactory
	if factory == nil {
		factory = func(opts *pahomqtt.ClientOptions) pahomqtt.Client {
			return pahomqtt.NewClient(opts)
		}
	}

	normCfg := NormalizerConfig{
		Broker:      deps.Config.Broker,
		TopicPrefix: deps.Config.TopicPrefix,
		DefaultQoS:  deps.Config.QoS,
	}

	return &Adapter{
		cfg:           deps.Config,
		normCfg:       normCfg,
		logger:        deps.Logger,
		userLister:    deps.UserLister,
		clientFactory: factory,
	}
}

// ChannelType returns ChannelTypeMQTT.
func (a *Adapter) ChannelType() models.ChannelType {
	return models.ChannelTypeMQTT
}

// Start connects to the MQTT broker and subscribes to per-user inbox topics.
func (a *Adapter) Start(ctx context.Context) error {
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(a.cfg.Broker)
	opts.SetClientID(a.cfg.ClientID)
	opts.SetCleanSession(a.cfg.CleanSession)
	opts.SetAutoReconnect(a.cfg.Reconnect.Enabled)
	opts.SetOrderMatters(false)
	opts.SetDefaultPublishHandler(a.onMessage)
	opts.SetOnConnectHandler(a.onConnect)
	opts.SetConnectionLostHandler(a.onConnectionLost)

	if a.cfg.Username != "" {
		opts.SetUsername(a.cfg.Username)
	}
	if a.cfg.Password != "" {
		opts.SetPassword(a.cfg.Password)
	}

	keepAlive := a.cfg.KeepAlive
	if keepAlive <= 0 {
		keepAlive = 60
	}
	opts.SetKeepAlive(time.Duration(keepAlive) * time.Second)

	if a.cfg.Reconnect.Enabled {
		initialDelay := a.cfg.Reconnect.InitialDelayMS
		if initialDelay <= 0 {
			initialDelay = 1000
		}
		maxDelay := a.cfg.Reconnect.MaxDelayMS
		if maxDelay <= 0 {
			maxDelay = 30000
		}
		opts.SetConnectRetryInterval(time.Duration(initialDelay) * time.Millisecond)
		opts.SetMaxReconnectInterval(time.Duration(maxDelay) * time.Millisecond)
	}

	a.client = a.clientFactory(opts)
	a.delivery = NewDeliveryClient(&pahoPublisherAdapter{client: a.client}, a.cfg.TopicPrefix, a.cfg.QoS, a.logger)

	token := a.client.Connect()
	if !token.WaitTimeout(30 * time.Second) {
		a.logger.Warn("mqtt connect timed out; adapter will retry in background")
		return nil
	}
	if token.Error() != nil {
		a.logger.Warn("mqtt connect failed; adapter will retry in background",
			zap.Error(token.Error()),
		)
		return nil
	}

	a.mu.Lock()
	a.connected = true
	a.mu.Unlock()

	a.logger.Info("mqtt adapter started",
		zap.String("broker", a.cfg.Broker),
		zap.String("clientId", a.cfg.ClientID),
	)
	return nil
}

// Stop gracefully disconnects from the MQTT broker.
func (a *Adapter) Stop(_ context.Context) error {
	if a.client != nil && a.client.IsConnected() {
		a.client.Disconnect(5000)
	}
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()
	a.logger.Info("mqtt adapter stopped")
	return nil
}

// HealthCheck returns nil if the MQTT client is connected, error otherwise.
func (a *Adapter) HealthCheck(_ context.Context) error {
	if a.client == nil {
		return fmt.Errorf("mqtt: client not initialized")
	}
	if !a.client.IsConnected() {
		return fmt.Errorf("mqtt: not connected to broker")
	}
	return nil
}

// NormalizeInbound converts a raw MQTT JSON payload into a NipperMessage.
// The topic parameter must be provided via the raw payload wrapper or set
// externally before calling this method.
func (a *Adapter) NormalizeInbound(_ context.Context, raw []byte) (*models.NipperMessage, error) {
	var wrapper struct {
		Topic   string          `json:"_topic"`
		Payload json.RawMessage `json:"_payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("mqtt normalise: unmarshal wrapper: %w", err)
	}

	if wrapper.Topic == "" {
		return nil, fmt.Errorf("mqtt normalise: _topic is required")
	}

	payload := []byte(wrapper.Payload)
	if len(payload) == 0 || string(payload) == "null" {
		return nil, fmt.Errorf("mqtt normalise: _payload is required")
	}

	return NormalizeInbound(payload, wrapper.Topic, a.normCfg)
}

// DeliverResponse sends a fully-assembled NipperResponse via MQTT.
func (a *Adapter) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}
	if a.delivery == nil {
		return fmt.Errorf("mqtt: delivery client not initialized")
	}
	return a.delivery.DeliverResponse(ctx, resp)
}

// DeliverEvent is a no-op for MQTT (non-streaming channel).
func (a *Adapter) DeliverEvent(_ context.Context, _ *models.NipperEvent) error {
	return nil
}

// SetHandler sets the message handler invoked for each inbound MQTT message.
func (a *Adapter) SetHandler(h MessageHandler) {
	a.handler = h
}

// DeliveryClientRef returns the underlying delivery client (for testing).
func (a *Adapter) DeliveryClientRef() *DeliveryClient {
	return a.delivery
}

// Validate checks that the MQTT configuration has all required fields.
func Validate(cfg config.MQTTConfig) error {
	if cfg.Broker == "" {
		return fmt.Errorf("mqtt: broker is required")
	}
	if cfg.ClientID == "" {
		return fmt.Errorf("mqtt: client_id is required")
	}
	if cfg.TopicPrefix == "" {
		return fmt.Errorf("mqtt: topic_prefix is required")
	}
	return nil
}

func (a *Adapter) onConnect(client pahomqtt.Client) {
	a.mu.Lock()
	a.connected = true
	a.mu.Unlock()

	a.logger.Info("mqtt connected to broker")
	a.subscribeAll()
}

func (a *Adapter) onConnectionLost(_ pahomqtt.Client, err error) {
	a.mu.Lock()
	a.connected = false
	a.mu.Unlock()

	a.logger.Warn("mqtt connection lost", zap.Error(err))
}

func (a *Adapter) subscribeAll() {
	if a.userLister == nil {
		a.logger.Debug("mqtt: no user lister configured, skipping subscriptions")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	users, err := a.userLister(ctx)
	if err != nil {
		a.logger.Error("mqtt: failed to list users for subscriptions", zap.Error(err))
		return
	}

	qos := byte(a.cfg.QoS)
	if qos > 2 {
		qos = 1
	}

	a.mu.Lock()
	a.subscribers = make([]string, 0, len(users))
	a.mu.Unlock()

	for _, userID := range users {
		topic := InboxTopic(a.cfg.TopicPrefix, userID)
		token := a.client.Subscribe(topic, qos, nil)
		if token.WaitTimeout(5 * time.Second) && token.Error() == nil {
			a.mu.Lock()
			a.subscribers = append(a.subscribers, userID)
			a.mu.Unlock()
			a.logger.Debug("mqtt subscribed", zap.String("topic", topic), zap.String("userId", userID))
		} else {
			errMsg := "timeout"
			if token.Error() != nil {
				errMsg = token.Error().Error()
			}
			a.logger.Warn("mqtt subscribe failed",
				zap.String("topic", topic),
				zap.String("userId", userID),
				zap.String("error", errMsg),
			)
		}
	}

	a.logger.Info("mqtt subscriptions complete",
		zap.Int("total", len(users)),
		zap.Int("subscribed", a.SubscriberCount()),
	)
}

// onMessage is the default publish handler for incoming MQTT messages.
func (a *Adapter) onMessage(_ pahomqtt.Client, msg pahomqtt.Message) {
	if a.handler == nil {
		a.logger.Warn("mqtt: received message but no handler set")
		return
	}

	topic := msg.Topic()
	payload := msg.Payload()

	a.logger.Debug("mqtt message received",
		zap.String("topic", topic),
		zap.Int("payloadSize", len(payload)),
	)

	nipperMsg, err := NormalizeInbound(payload, topic, a.normCfg)
	if err != nil {
		a.logger.Warn("mqtt: normalize failed",
			zap.String("topic", topic),
			zap.Error(err),
		)
		return
	}
	if nipperMsg == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.handler(ctx, nipperMsg); err != nil {
		a.logger.Error("mqtt: handler error",
			zap.String("topic", topic),
			zap.String("userId", nipperMsg.UserID),
			zap.Error(err),
		)
	}
}

// SubscriberCount returns the number of currently subscribed user topics.
func (a *Adapter) SubscriberCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.subscribers)
}

// IsConnected returns true if the MQTT client is connected.
func (a *Adapter) IsConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connected
}

// pahoPublisherAdapter wraps paho.mqtt.golang.Client to implement MQTTPublisher.
type pahoPublisherAdapter struct {
	client pahomqtt.Client
}

func (p *pahoPublisherAdapter) Publish(topic string, qos byte, retained bool, payload []byte) error {
	token := p.client.Publish(topic, qos, retained, payload)
	if !token.WaitTimeout(publishTimeout) {
		return fmt.Errorf("mqtt publish to %s timed out", topic)
	}
	return token.Error()
}

func (p *pahoPublisherAdapter) IsConnected() bool {
	return p.client.IsConnected()
}

// SubscribeUser subscribes to a single user's inbox topic (for dynamic user addition).
func (a *Adapter) SubscribeUser(userID string) error {
	if a.client == nil || !a.client.IsConnected() {
		return fmt.Errorf("mqtt: not connected")
	}

	topic := InboxTopic(a.cfg.TopicPrefix, userID)
	qos := byte(a.cfg.QoS)
	if qos > 2 {
		qos = 1
	}

	token := a.client.Subscribe(topic, qos, nil)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt subscribe to %s timed out", topic)
	}
	if token.Error() != nil {
		return fmt.Errorf("mqtt subscribe to %s: %w", topic, token.Error())
	}

	a.mu.Lock()
	found := false
	for _, s := range a.subscribers {
		if s == userID {
			found = true
			break
		}
	}
	if !found {
		a.subscribers = append(a.subscribers, userID)
	}
	a.mu.Unlock()

	return nil
}

// UnsubscribeUser unsubscribes from a user's inbox topic.
func (a *Adapter) UnsubscribeUser(userID string) error {
	if a.client == nil || !a.client.IsConnected() {
		return fmt.Errorf("mqtt: not connected")
	}

	topic := InboxTopic(a.cfg.TopicPrefix, userID)
	token := a.client.Unsubscribe(topic)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt unsubscribe from %s timed out", topic)
	}
	if token.Error() != nil {
		return fmt.Errorf("mqtt unsubscribe from %s: %w", topic, token.Error())
	}

	a.mu.Lock()
	for i, s := range a.subscribers {
		if s == userID {
			a.subscribers = append(a.subscribers[:i], a.subscribers[i+1:]...)
			break
		}
	}
	a.mu.Unlock()

	return nil
}

// Subscriptions returns a copy of the currently subscribed user IDs.
func (a *Adapter) Subscriptions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.subscribers))
	copy(out, a.subscribers)
	return out
}

// InboxTopicForUser is a convenience method returning the inbox topic for a user.
func (a *Adapter) InboxTopicForUser(userID string) string {
	return InboxTopic(a.cfg.TopicPrefix, userID)
}

// OutboxTopicForUser is a convenience method returning the outbox topic for a user.
func (a *Adapter) OutboxTopicForUser(userID string) string {
	return OutboxTopic(a.cfg.TopicPrefix, userID)
}

// RedactBrokerURL returns the broker URL with credentials redacted (for logging).
func RedactBrokerURL(brokerURL string) string {
	if idx := strings.Index(brokerURL, "@"); idx > 0 {
		prefix := brokerURL[:strings.Index(brokerURL, "://")+3]
		suffix := brokerURL[idx:]
		return prefix + "[REDACTED]" + suffix
	}
	return brokerURL
}
