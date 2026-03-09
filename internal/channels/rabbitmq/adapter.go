// Package rabbitmq implements the ChannelAdapter for RabbitMQ-based
// service-to-service messaging.
//
// The adapter connects to a RabbitMQ broker, subscribes to per-user inbound
// queues ({exchangeInbound}/nipper.{userId}.inbox), normalizes inbound JSON
// messages into NipperMessages, and delivers responses to outbound exchanges
// or reply-to queues.
package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// MessageHandler is the callback invoked when an AMQP message is received.
type MessageHandler func(ctx context.Context, msg *models.NipperMessage) error

// UserLister returns the IDs of all users that should have RabbitMQ subscriptions.
type UserLister func(ctx context.Context) ([]string, error)

// ConnectionFactory creates AMQP connections (seam for testing).
type ConnectionFactory func(url string) (AMQPConnection, error)

// AMQPConnection abstracts an amqp.Connection for testability.
type AMQPConnection interface {
	Channel() (AMQPChannel, error)
	Close() error
	IsClosed() bool
	NotifyClose(receiver chan *amqp.Error) chan *amqp.Error
}

// AMQPChannel abstracts an amqp.Channel for testability.
type AMQPChannel interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	Publish(exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Qos(prefetchCount, prefetchSize int, global bool) error
	Close() error
}

// Adapter implements channels.ChannelAdapter for the RabbitMQ service-to-service channel.
type Adapter struct {
	cfg        config.RabbitMQChanConfig
	normCfg    NormalizerConfig
	delivery   *DeliveryClient
	logger     *zap.Logger
	handler    MessageHandler
	userLister UserLister
	connFactory ConnectionFactory

	mu          sync.Mutex
	conn        AMQPConnection
	channel     AMQPChannel
	consumers   []string
	connected   bool
	stopCh      chan struct{}
}

// AdapterDeps bundles the dependencies for constructing a RabbitMQ Adapter.
type AdapterDeps struct {
	Config        config.RabbitMQChanConfig
	Logger        *zap.Logger
	UserLister    UserLister
	ConnFactory   ConnectionFactory
}

// NewAdapter creates a RabbitMQ channel adapter. Call SetHandler before Start.
func NewAdapter(deps AdapterDeps) *Adapter {
	factory := deps.ConnFactory
	if factory == nil {
		factory = defaultConnectionFactory
	}

	normCfg := NormalizerConfig{
		ExchangeInbound:  deps.Config.ExchangeInbound,
		ExchangeOutbound: deps.Config.ExchangeOutbound,
	}

	return &Adapter{
		cfg:         deps.Config,
		normCfg:     normCfg,
		logger:      deps.Logger,
		userLister:  deps.UserLister,
		connFactory: factory,
		stopCh:      make(chan struct{}),
	}
}

// ChannelType returns ChannelTypeRabbitMQ.
func (a *Adapter) ChannelType() models.ChannelType {
	return models.ChannelTypeRabbitMQ
}

// Start connects to the RabbitMQ broker, declares topology, and begins consuming.
func (a *Adapter) Start(ctx context.Context) error {
	amqpURL := buildURL(a.cfg)

	conn, err := a.connFactory(amqpURL)
	if err != nil {
		a.logger.Warn("rabbitmq channel adapter connect failed; will retry in background",
			zap.Error(err),
		)
		go a.reconnectLoop(amqpURL)
		return nil
	}

	if err := a.setup(conn); err != nil {
		conn.Close()
		a.logger.Warn("rabbitmq channel adapter setup failed; will retry in background",
			zap.Error(err),
		)
		go a.reconnectLoop(amqpURL)
		return nil
	}

	go a.watchConnection(amqpURL)

	a.logger.Info("rabbitmq channel adapter started",
		zap.String("url", redactURL(amqpURL)),
		zap.String("exchangeInbound", a.cfg.ExchangeInbound),
		zap.String("exchangeOutbound", a.cfg.ExchangeOutbound),
	)
	return nil
}

// Stop gracefully shuts down the adapter.
func (a *Adapter) Stop(_ context.Context) error {
	close(a.stopCh)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.channel != nil {
		a.channel.Close()
	}
	if a.conn != nil && !a.conn.IsClosed() {
		a.conn.Close()
	}
	a.connected = false

	a.logger.Info("rabbitmq channel adapter stopped")
	return nil
}

// HealthCheck returns nil if connected, error otherwise.
func (a *Adapter) HealthCheck(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected {
		return fmt.Errorf("rabbitmq channel: not connected to broker")
	}
	return nil
}

// NormalizeInbound converts a raw AMQP JSON payload into a NipperMessage.
func (a *Adapter) NormalizeInbound(_ context.Context, raw []byte) (*models.NipperMessage, error) {
	var wrapper struct {
		Body    json.RawMessage   `json:"_body"`
		Meta    InboundMeta       `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("rabbitmq normalise: unmarshal wrapper: %w", err)
	}

	body := []byte(wrapper.Body)
	if len(body) == 0 || string(body) == "null" {
		return nil, fmt.Errorf("rabbitmq normalise: _body is required")
	}

	return NormalizeInbound(body, wrapper.Meta, a.normCfg)
}

// DeliverResponse sends a fully-assembled NipperResponse via RabbitMQ.
func (a *Adapter) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}
	if a.delivery == nil {
		return fmt.Errorf("rabbitmq channel: delivery client not initialized")
	}
	return a.delivery.DeliverResponse(ctx, resp)
}

// DeliverEvent is a no-op for RabbitMQ (non-streaming channel).
func (a *Adapter) DeliverEvent(_ context.Context, _ *models.NipperEvent) error {
	return nil
}

// SetHandler sets the message handler invoked for each inbound message.
func (a *Adapter) SetHandler(h MessageHandler) {
	a.handler = h
}

// IsConnected returns true if the AMQP connection is active.
func (a *Adapter) IsConnected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connected
}

// ConsumerCount returns the number of currently consuming user queues.
func (a *Adapter) ConsumerCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.consumers)
}

// Validate checks that the RabbitMQ configuration has all required fields.
func Validate(cfg config.RabbitMQChanConfig) error {
	if cfg.URL == "" {
		return fmt.Errorf("rabbitmq channel: url is required")
	}
	if cfg.ExchangeInbound == "" {
		return fmt.Errorf("rabbitmq channel: exchange_inbound is required")
	}
	if cfg.ExchangeOutbound == "" {
		return fmt.Errorf("rabbitmq channel: exchange_outbound is required")
	}
	return nil
}

func (a *Adapter) setup(conn AMQPConnection) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("opening AMQP channel: %w", err)
	}

	prefetch := a.cfg.Prefetch
	if prefetch <= 0 {
		prefetch = 1
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		ch.Close()
		return fmt.Errorf("setting QoS: %w", err)
	}

	if err := ch.ExchangeDeclare(a.cfg.ExchangeInbound, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		return fmt.Errorf("declaring inbound exchange: %w", err)
	}
	if err := ch.ExchangeDeclare(a.cfg.ExchangeOutbound, "topic", true, false, false, false, nil); err != nil {
		ch.Close()
		return fmt.Errorf("declaring outbound exchange: %w", err)
	}
	if a.cfg.ExchangeDLX != "" {
		if err := ch.ExchangeDeclare(a.cfg.ExchangeDLX, "topic", true, false, false, false, nil); err != nil {
			ch.Close()
			return fmt.Errorf("declaring DLX exchange: %w", err)
		}
	}

	a.mu.Lock()
	a.conn = conn
	a.channel = ch
	a.connected = true
	a.mu.Unlock()

	publisher := &amqpChannelPublisher{ch: ch, connected: &a.connected, mu: &a.mu}
	a.delivery = NewDeliveryClient(publisher, a.cfg.ExchangeOutbound, a.logger)

	a.subscribeAll()
	return nil
}

func (a *Adapter) subscribeAll() {
	if a.userLister == nil {
		a.logger.Debug("rabbitmq channel: no user lister, skipping subscriptions")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	users, err := a.userLister(ctx)
	if err != nil {
		a.logger.Error("rabbitmq channel: failed to list users", zap.Error(err))
		return
	}

	a.mu.Lock()
	a.consumers = make([]string, 0, len(users))
	ch := a.channel
	a.mu.Unlock()

	if ch == nil {
		return
	}

	for _, userID := range users {
		queueName := InboundQueueName(userID)
		routingKey := InboundRoutingKey(userID)

		args := amqp.Table{}
		if a.cfg.ExchangeDLX != "" {
			args["x-dead-letter-exchange"] = a.cfg.ExchangeDLX
		}

		if _, err := ch.QueueDeclare(queueName, true, false, false, false, args); err != nil {
			a.logger.Warn("rabbitmq channel: queue declare failed",
				zap.String("queue", queueName), zap.Error(err))
			continue
		}

		if err := ch.QueueBind(queueName, routingKey, a.cfg.ExchangeInbound, false, nil); err != nil {
			a.logger.Warn("rabbitmq channel: queue bind failed",
				zap.String("queue", queueName), zap.String("key", routingKey), zap.Error(err))
			continue
		}

		consumerTag := fmt.Sprintf("nipper-channel-%s", userID)
		deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
		if err != nil {
			a.logger.Warn("rabbitmq channel: consume failed",
				zap.String("queue", queueName), zap.Error(err))
			continue
		}

		a.mu.Lock()
		a.consumers = append(a.consumers, userID)
		a.mu.Unlock()

		go a.consumeLoop(userID, deliveries)

		a.logger.Debug("rabbitmq channel: consuming",
			zap.String("queue", queueName), zap.String("userId", userID))
	}

	a.logger.Info("rabbitmq channel: subscriptions complete",
		zap.Int("total", len(users)),
		zap.Int("consuming", a.ConsumerCount()),
	)
}

func (a *Adapter) consumeLoop(userID string, deliveries <-chan amqp.Delivery) {
	for {
		select {
		case <-a.stopCh:
			return
		case d, ok := <-deliveries:
			if !ok {
				a.logger.Warn("rabbitmq channel: delivery channel closed",
					zap.String("userId", userID))
				return
			}
			a.handleDelivery(userID, d)
		}
	}
}

func (a *Adapter) handleDelivery(userID string, d amqp.Delivery) {
	if a.handler == nil {
		a.logger.Warn("rabbitmq channel: received message but no handler set")
		d.Nack(false, true) //nolint:errcheck
		return
	}

	headers := make(map[string]string)
	for k, v := range d.Headers {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}

	meta := InboundMeta{
		Exchange:    d.Exchange,
		RoutingKey:  d.RoutingKey,
		Queue:       InboundQueueName(userID),
		ConsumerTag: d.ConsumerTag,
		DeliveryTag: d.DeliveryTag,
		ReplyTo:     d.ReplyTo,
		MessageID:   d.MessageId,
		Headers:     headers,
	}

	msg, err := NormalizeInbound(d.Body, meta, a.normCfg)
	if err != nil {
		a.logger.Warn("rabbitmq channel: normalize failed",
			zap.String("userId", userID),
			zap.String("routingKey", d.RoutingKey),
			zap.Error(err),
		)
		d.Nack(false, false) //nolint:errcheck
		return
	}
	if msg == nil {
		d.Ack(false) //nolint:errcheck
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.handler(ctx, msg); err != nil {
		a.logger.Error("rabbitmq channel: handler error",
			zap.String("userId", msg.UserID),
			zap.String("routingKey", d.RoutingKey),
			zap.Error(err),
		)
		d.Nack(false, true) //nolint:errcheck
		return
	}
	d.Ack(false) //nolint:errcheck
}

func (a *Adapter) watchConnection(amqpURL string) {
	for {
		a.mu.Lock()
		conn := a.conn
		a.mu.Unlock()

		if conn == nil {
			return
		}

		notifyClose := conn.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-a.stopCh:
			return
		case amqpErr, ok := <-notifyClose:
			if !ok {
				select {
				case <-a.stopCh:
					return
				default:
				}
			}
			a.mu.Lock()
			a.connected = false
			a.consumers = nil
			a.mu.Unlock()

			if amqpErr != nil {
				a.logger.Warn("rabbitmq channel: connection lost",
					zap.String("reason", amqpErr.Reason),
					zap.Int("code", int(amqpErr.Code)),
				)
			} else {
				a.logger.Warn("rabbitmq channel: connection closed")
			}
		}

		a.reconnectLoop(amqpURL)

		select {
		case <-a.stopCh:
			return
		default:
		}
	}
}

func (a *Adapter) reconnectLoop(amqpURL string) {
	initial := time.Duration(a.cfg.Reconnect.InitialDelayMS) * time.Millisecond
	if initial <= 0 {
		initial = time.Second
	}
	maxDelay := time.Duration(a.cfg.Reconnect.MaxDelayMS) * time.Millisecond
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}

	delay := initial
	for {
		select {
		case <-a.stopCh:
			return
		case <-time.After(delay):
		}

		conn, err := a.connFactory(amqpURL)
		if err != nil {
			a.logger.Warn("rabbitmq channel: reconnect failed",
				zap.Error(err),
				zap.Duration("nextRetry", delay),
			)
			next := time.Duration(math.Min(float64(delay*2), float64(maxDelay)))
			delay = next
			continue
		}

		if err := a.setup(conn); err != nil {
			conn.Close()
			a.logger.Warn("rabbitmq channel: setup after reconnect failed",
				zap.Error(err),
				zap.Duration("nextRetry", delay),
			)
			next := time.Duration(math.Min(float64(delay*2), float64(maxDelay)))
			delay = next
			continue
		}

		a.logger.Info("rabbitmq channel: connection restored")
		return
	}
}

func defaultConnectionFactory(rawURL string) (AMQPConnection, error) {
	conn, err := amqp.Dial(rawURL)
	if err != nil {
		return nil, err
	}
	return &realAMQPConnection{conn: conn}, nil
}

func buildURL(cfg config.RabbitMQChanConfig) string {
	if cfg.URL == "" {
		return "amqp://localhost:5672"
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return cfg.URL
	}
	if cfg.Username != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	if cfg.VHost != "" {
		u.Path = "/" + cfg.VHost
		u.RawPath = "/" + url.PathEscape(cfg.VHost)
	}
	return u.String()
}

func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable]"
	}
	u.User = nil
	return u.String()
}

// realAMQPConnection wraps *amqp.Connection to implement AMQPConnection.
type realAMQPConnection struct {
	conn *amqp.Connection
}

func (c *realAMQPConnection) Channel() (AMQPChannel, error) {
	return c.conn.Channel()
}
func (c *realAMQPConnection) Close() error                                     { return c.conn.Close() }
func (c *realAMQPConnection) IsClosed() bool                                   { return c.conn.IsClosed() }
func (c *realAMQPConnection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return c.conn.NotifyClose(receiver)
}

// amqpChannelPublisher wraps an AMQPChannel to implement AMQPPublisher.
type amqpChannelPublisher struct {
	ch        AMQPChannel
	connected *bool
	mu        *sync.Mutex
}

func (p *amqpChannelPublisher) Publish(exchange, routingKey string, mandatory, immediate bool, body []byte, headers map[string]interface{}) error {
	pub := amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
		Headers:     amqp.Table(headers),
		Timestamp:   time.Now().UTC(),
	}
	return p.ch.Publish(exchange, routingKey, mandatory, immediate, pub)
}

func (p *amqpChannelPublisher) IsConnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.connected
}
