package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/models"
)

// Publisher routes inbound messages and control signals to the correct RabbitMQ exchanges.
type Publisher interface {
	// PublishMessage serialises item and publishes it to nipper.sessions with the
	// routing key nipper.sessions.{userId}.{sessionId}.
	PublishMessage(ctx context.Context, item *models.QueueItem) error

	// PublishControl sends a control signal (interrupt / abort) for a specific user.
	PublishControl(ctx context.Context, userID string, msg *models.ControlMessage) error

	// Close releases the underlying AMQP channel.
	Close() error
}

// RabbitMQPublisher implements Publisher against a live broker.
type RabbitMQPublisher struct {
	broker *Broker
	logger *zap.Logger

	mu  sync.Mutex
	ch  AMQPChannel
}

// NewRabbitMQPublisher creates a publisher and opens an initial AMQP channel.
// If the broker is not yet connected, channel creation is deferred to the first publish.
func NewRabbitMQPublisher(broker *Broker, logger *zap.Logger) (*RabbitMQPublisher, error) {
	p := &RabbitMQPublisher{
		broker: broker,
		logger: logger,
	}

	// Attempt to get an initial channel; failure is non-fatal — it will be retried
	// on the first publish call.
	if ch, err := broker.PublishChannel(); err == nil {
		p.ch = ch
	}

	// Re-create the channel after every broker reconnect.
	broker.OnReconnect(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if ch, err := broker.PublishChannel(); err == nil {
			p.ch = ch
		}
	})

	return p, nil
}

// PublishMessage serialises the QueueItem and publishes it to nipper.sessions exchange.
func (p *RabbitMQPublisher) PublishMessage(ctx context.Context, item *models.QueueItem) error {
	if item == nil {
		return fmt.Errorf("item must not be nil")
	}

	body, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshalling QueueItem: %w", err)
	}

	// Routing key: nipper.sessions.{userId}.{sessionId}
	sessionID := ""
	if item.Message != nil {
		sessionID = item.Message.SessionKey
	}
	userID := ""
	if item.Message != nil {
		userID = item.Message.UserID
	}
	routingKey := fmt.Sprintf("nipper.sessions.%s.%s", userID, sessionID)

	headers := amqp.Table{
		"x-nipper-user-id":     userID,
		"x-nipper-session-key": sessionID,
		"x-nipper-queue-mode":  string(item.Mode),
		"x-nipper-priority":    int32(item.Priority),
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    item.ID,
		Timestamp:    time.Now(),
		Headers:      headers,
		Body:         body,
	}

	ch, err := p.getChannel()
	if err != nil {
		return fmt.Errorf("getting AMQP channel: %w", err)
	}

	if err := ch.PublishWithContext(ctx, ExchangeSessions, routingKey, false, false, publishing); err != nil {
		// Invalidate the cached channel so the next call creates a fresh one.
		p.mu.Lock()
		p.ch = nil
		p.mu.Unlock()
		return fmt.Errorf("publishing to %q with key %q: %w", ExchangeSessions, routingKey, err)
	}

	p.logger.Debug("message published to queue",
		zap.String("userId", userID),
		zap.String("sessionKey", sessionID),
		zap.String("routingKey", routingKey),
		zap.String("queueMode", string(item.Mode)),
		zap.String("messageId", item.ID),
	)

	return nil
}

// PublishControl publishes a ControlMessage to the nipper.control exchange.
func (p *RabbitMQPublisher) PublishControl(ctx context.Context, userID string, msg *models.ControlMessage) error {
	if msg == nil {
		return fmt.Errorf("control message must not be nil")
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling ControlMessage: %w", err)
	}

	// Routing key: nipper.control.{userId}
	routingKey := fmt.Sprintf("nipper.control.%s", userID)

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	}

	ch, err := p.getChannel()
	if err != nil {
		return fmt.Errorf("getting AMQP channel: %w", err)
	}

	if err := ch.PublishWithContext(ctx, ExchangeControl, routingKey, false, false, publishing); err != nil {
		p.mu.Lock()
		p.ch = nil
		p.mu.Unlock()
		return fmt.Errorf("publishing control to %q with key %q: %w", ExchangeControl, routingKey, err)
	}

	p.logger.Debug("control message published",
		zap.String("userId", userID),
		zap.String("controlType", string(msg.Type)),
	)

	return nil
}

// Close releases the AMQP channel held by this publisher.
func (p *RabbitMQPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch.Close()
	}
	return nil
}

// getChannel returns the cached channel, re-creating it if it has been closed.
func (p *RabbitMQPublisher) getChannel() (AMQPChannel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}

	ch, err := p.broker.PublishChannel()
	if err != nil {
		return nil, err
	}
	p.ch = ch
	return p.ch, nil
}
