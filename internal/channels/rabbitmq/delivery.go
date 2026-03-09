package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

// AMQPPublisher abstracts the AMQP publish operation for testability.
type AMQPPublisher interface {
	Publish(exchange, routingKey string, mandatory, immediate bool, body []byte, headers map[string]interface{}) error
	IsConnected() bool
}

// DeliveryClient delivers NipperResponses by publishing them to RabbitMQ exchanges.
type DeliveryClient struct {
	publisher        AMQPPublisher
	exchangeOutbound string
	logger           *zap.Logger
}

// NewDeliveryClient creates a delivery client backed by an AMQP publisher.
func NewDeliveryClient(publisher AMQPPublisher, exchangeOutbound string, logger *zap.Logger) *DeliveryClient {
	return &DeliveryClient{
		publisher:        publisher,
		exchangeOutbound: exchangeOutbound,
		logger:           logger,
	}
}

// RabbitMQResponsePayload is the JSON structure published to outbound exchanges/queues.
type RabbitMQResponsePayload struct {
	ResponseID    string               `json:"responseId"`
	InReplyTo     string               `json:"inReplyTo,omitempty"`
	SessionKey    string               `json:"sessionKey"`
	UserID        string               `json:"userId"`
	Text          string               `json:"text"`
	Parts         []models.ContentPart `json:"parts,omitempty"`
	CorrelationID string               `json:"correlationId,omitempty"`
	Timestamp     string               `json:"timestamp"`
}

// DeliverResponse publishes a NipperResponse to the appropriate RabbitMQ exchange or queue.
// If the inbound message specified a replyTo in RabbitMqMeta, the response is published
// directly to that queue. Otherwise, it goes to the outbound exchange.
func (d *DeliveryClient) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}
	if d.publisher == nil {
		return fmt.Errorf("rabbitmq delivery: publisher is nil")
	}
	if !d.publisher.IsConnected() {
		return fmt.Errorf("rabbitmq delivery: not connected to broker")
	}

	exchange, routingKey := d.resolveTarget(resp)
	headers := d.buildHeaders(resp)
	payload, err := d.buildPayload(resp)
	if err != nil {
		return fmt.Errorf("rabbitmq delivery: %w", err)
	}

	d.logger.Debug("publishing rabbitmq response",
		zap.String("exchange", exchange),
		zap.String("routingKey", routingKey),
		zap.String("userId", resp.UserID),
		zap.String("sessionKey", resp.SessionKey),
	)

	if err := d.publisher.Publish(exchange, routingKey, false, false, payload, headers); err != nil {
		return fmt.Errorf("rabbitmq delivery: publish to %s/%s: %w", exchange, routingKey, err)
	}

	return nil
}

func (d *DeliveryClient) resolveTarget(resp *models.NipperResponse) (exchange, routingKey string) {
	if meta, ok := extractRabbitMQMeta(resp); ok && meta.ReplyTo != "" {
		return "", meta.ReplyTo
	}
	return d.exchangeOutbound, OutboundRoutingKey(resp.UserID)
}

func (d *DeliveryClient) buildHeaders(resp *models.NipperResponse) map[string]interface{} {
	headers := map[string]interface{}{
		"x-nipper-response-id": resp.ResponseID,
		"content-type":         "application/json",
	}
	if meta, ok := extractRabbitMQMeta(resp); ok && meta.CorrelationID != "" {
		headers["x-correlation-id"] = meta.CorrelationID
	}
	return headers
}

func (d *DeliveryClient) buildPayload(resp *models.NipperResponse) ([]byte, error) {
	payload := RabbitMQResponsePayload{
		ResponseID:    resp.ResponseID,
		InReplyTo:     resp.OriginMessageID,
		SessionKey:    resp.SessionKey,
		UserID:        resp.UserID,
		Text:          resp.Text,
		Parts:         resp.Parts,
		Timestamp:     resp.Timestamp.Format(time.RFC3339),
	}

	if meta, ok := extractRabbitMQMeta(resp); ok {
		payload.CorrelationID = meta.CorrelationID
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal response payload: %w", err)
	}
	return data, nil
}

func extractRabbitMQMeta(resp *models.NipperResponse) (models.RabbitMqMeta, bool) {
	if resp.Meta != nil {
		if m, ok := resp.Meta.(models.RabbitMqMeta); ok {
			return m, true
		}
	}
	return models.RabbitMqMeta{}, false
}
