package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	"github.com/google/uuid"
	"github.com/jescarri/open-nipper/internal/agent/registration"
	"github.com/jescarri/open-nipper/internal/models"
)

// EventPublisher publishes NipperEvent messages to the Gateway events exchange.
type EventPublisher struct {
	reg    *registration.RegistrationResult
	conn   *amqp.Connection
	logger *zap.Logger

	mu sync.Mutex
	ch *amqp.Channel
}

// NewEventPublisher creates a publisher that publishes to the events exchange.
func NewEventPublisher(conn *amqp.Connection, reg *registration.RegistrationResult, logger *zap.Logger) (*EventPublisher, error) {
	p := &EventPublisher{
		reg:    reg,
		conn:   conn,
		logger: logger,
	}
	if err := p.openChannel(); err != nil {
		return nil, fmt.Errorf("opening event publisher channel: %w", err)
	}
	return p, nil
}

// PublishEvent publishes a NipperEvent to the Gateway.
func (p *EventPublisher) PublishEvent(ctx context.Context, event *models.NipperEvent) error {
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.UserID == "" {
		event.UserID = p.reg.UserID
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}

	routingKey := p.routingKey(event.SessionKey)

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureChannel(); err != nil {
		return fmt.Errorf("ensuring channel: %w", err)
	}

	if err := p.ch.PublishWithContext(
		ctx,
		p.reg.RabbitMQ.Exchanges.Events,
		routingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    event.EventID,
			Timestamp:    event.Timestamp,
			Body:         body,
		},
	); err != nil {
		p.ch = nil // invalidate so next call reopens
		return fmt.Errorf("publishing event %s: %w", event.Type, err)
	}

	p.logger.Debug("event published",
		zap.String("type", string(event.Type)),
		zap.String("sessionKey", event.SessionKey),
		zap.String("routingKey", routingKey),
	)
	return nil
}

// PublishDone is a helper that sends a "done" event with the final text response.
// If contextUsage is non-nil, it is included so the gateway can show context window usage and percentage.
func (p *EventPublisher) PublishDone(ctx context.Context, sessionKey, responseID, text string, contextUsage *models.ContextUsage) error {
	ev := &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: responseID,
		Delta:      &models.EventDelta{Text: text},
	}
	if contextUsage != nil {
		ev.ContextUsage = contextUsage
	}
	return p.PublishEvent(ctx, ev)
}

// PublishError is a helper that sends an "error" event.
func (p *EventPublisher) PublishError(ctx context.Context, sessionKey, code, message string, recoverable bool) error {
	return p.PublishEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeError,
		SessionKey: sessionKey,
		Error: &models.EventError{
			Code:        code,
			Message:     message,
			Recoverable: recoverable,
		},
	})
}

// Close closes the underlying AMQP channel.
func (p *EventPublisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		_ = p.ch.Close()
		p.ch = nil
	}
}

func (p *EventPublisher) routingKey(sessionKey string) string {
	template := p.reg.RabbitMQ.RoutingKeys.EventsPublish
	return strings.ReplaceAll(template, "{sessionId}", sessionKey)
}

func (p *EventPublisher) openChannel() error {
	ch, err := p.conn.Channel()
	if err != nil {
		return err
	}
	p.ch = ch
	return nil
}

func (p *EventPublisher) ensureChannel() error {
	if p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	return p.openChannel()
}
