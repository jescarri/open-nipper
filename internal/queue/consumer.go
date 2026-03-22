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

// EventHandler is the callback invoked for each received NipperEvent.
// A non-nil error causes the message to be nacked (requeue=false).
type EventHandler func(ctx context.Context, event *models.NipperEvent) error

// EventConsumer consumes NipperEvents from the nipper-events-gateway queue and
// dispatches them to a registered handler.
type EventConsumer interface {
	// SetHandler registers the function that will be called for each event.
	// It must be called before Start.
	SetHandler(fn EventHandler)

	// Start begins consuming.  It blocks until ctx is cancelled or Stop is called.
	Start(ctx context.Context) error

	// Stop signals the consumer to stop after the current batch.
	Stop()
}

// RabbitMQConsumer implements EventConsumer against a live broker.
type RabbitMQConsumer struct {
	broker  *Broker
	logger  *zap.Logger
	handler EventHandler

	mu     sync.Mutex
	stopCh chan struct{}
}

// NewRabbitMQConsumer creates a consumer.  Call SetHandler before Start.
func NewRabbitMQConsumer(broker *Broker, logger *zap.Logger) *RabbitMQConsumer {
	return &RabbitMQConsumer{
		broker: broker,
		logger: logger,
		stopCh: make(chan struct{}),
	}
}

// SetHandler registers the event handler.
func (c *RabbitMQConsumer) SetHandler(fn EventHandler) {
	c.handler = fn
}

// Start opens a channel, sets prefetch, and begins consuming from nipper-events-gateway.
// It returns only when ctx is cancelled, Stop is called, or a terminal channel error occurs.
func (c *RabbitMQConsumer) Start(ctx context.Context) error {
	if c.handler == nil {
		return fmt.Errorf("handler must be set before starting consumer")
	}

	for {
		if err := c.consume(ctx); err != nil {
			// Distinguish cancellation from channel errors.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.stopCh:
				return nil
			default:
				c.logger.Warn("consumer channel error, reconnecting", zap.Error(err))
			}
		}

		// If stopped or context cancelled, exit; otherwise back off before retrying.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stopCh:
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// consume runs a single consume loop until the delivery channel is closed or context done.
func (c *RabbitMQConsumer) consume(ctx context.Context) error {
	ch, err := c.broker.ConsumeChannel()
	if err != nil {
		return fmt.Errorf("opening consume channel: %w", err)
	}
	defer ch.Close()

	if err := ch.Qos(eventsGatewayPrefetch, 0, false); err != nil {
		return fmt.Errorf("setting QoS: %w", err)
	}

	deliveries, err := ch.Consume(
		QueueEventsGateway,
		"",    // consumer tag — auto-generated
		false, // autoAck — we ack manually
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,
	)
	if err != nil {
		return fmt.Errorf("starting consume: %w", err)
	}

	c.logger.Info("event consumer started", zap.String("queue", QueueEventsGateway))

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.stopCh:
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed")
			}
			c.handleDelivery(ctx, delivery)
		}
	}
}

// handleDelivery decodes one delivery and calls the registered handler.
func (c *RabbitMQConsumer) handleDelivery(ctx context.Context, d amqp.Delivery) {
	var event models.NipperEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		c.logger.Error("failed to unmarshal NipperEvent",
			zap.Error(err),
			zap.String("messageId", d.MessageId),
			zap.ByteString("body", d.Body),
		)
		// Malformed message: nack without requeue to avoid poison-pill loops.
		_ = d.Nack(false, false)
		return
	}

	if err := c.handler(ctx, &event); err != nil {
		c.logger.Error("event handler returned error",
			zap.Error(err),
			zap.String("eventId", event.EventID),
			zap.String("eventType", string(event.Type)),
			zap.String("sessionKey", event.SessionKey),
		)
		// Nack without requeue — unrecoverable handler error.
		_ = d.Nack(false, false)
		return
	}

	if err := d.Ack(false); err != nil {
		c.logger.Warn("ack failed",
			zap.Error(err),
			zap.String("messageId", d.MessageId),
		)
	}
}

// Stop signals the consumer to stop after completing the current dispatch.
func (c *RabbitMQConsumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.stopCh:
		// Already stopped.
	default:
		close(c.stopCh)
	}
}
