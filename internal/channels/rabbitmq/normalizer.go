package rabbitmq

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/open-nipper/open-nipper/internal/models"
)

// RabbitMQInboundPayload is the expected JSON body consumed from inbound AMQP queues.
type RabbitMQInboundPayload struct {
	Text          string            `json:"text"`
	Source        string            `json:"source,omitempty"`
	CorrelationID string           `json:"correlationId,omitempty"`
	MessageID     string            `json:"messageId,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
}

// NormalizerConfig holds settings for RabbitMQ channel message normalization.
type NormalizerConfig struct {
	ExchangeInbound  string
	ExchangeOutbound string
}

// InboundMeta carries AMQP delivery metadata needed for normalization.
type InboundMeta struct {
	Exchange    string
	RoutingKey  string
	Queue       string
	ConsumerTag string
	DeliveryTag uint64
	ReplyTo     string
	MessageID   string
	Headers     map[string]string
}

// NormalizeInbound converts a raw RabbitMQ JSON payload into a NipperMessage.
// The userID is extracted from the routing key or the x-nipper-user header.
// Returns (nil, nil) for messages that should be silently ignored.
func NormalizeInbound(raw []byte, meta InboundMeta, cfg NormalizerConfig) (*models.NipperMessage, error) {
	var payload RabbitMQInboundPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("rabbitmq normalizer: unmarshal payload: %w", err)
	}

	if strings.TrimSpace(payload.Text) == "" {
		return nil, nil
	}

	userID := resolveUserID(meta)
	if userID == "" {
		return nil, fmt.Errorf("rabbitmq normalizer: cannot determine userId from routing key %q or headers", meta.RoutingKey)
	}

	now := time.Now().UTC()

	originMsgID := payload.MessageID
	if originMsgID == "" {
		originMsgID = meta.MessageID
	}
	if originMsgID == "" {
		originMsgID = fmt.Sprintf("rabbitmq:%s:%d", meta.RoutingKey, now.UnixNano())
	}

	correlationID := payload.CorrelationID
	if correlationID == "" {
		correlationID = meta.Headers["x-correlation-id"]
	}

	replyTo := meta.ReplyTo
	if replyTo == "" {
		replyTo = meta.Headers["x-reply-to"]
	}

	rmqMeta := models.RabbitMqMeta{
		Exchange:      meta.Exchange,
		RoutingKey:    meta.RoutingKey,
		CorrelationID: correlationID,
		ReplyTo:       replyTo,
		MessageID:     meta.MessageID,
	}

	outboundChannelID := fmt.Sprintf("nipper-%s-outbox", userID)
	if replyTo != "" {
		outboundChannelID = replyTo
	}

	return &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: originMsgID,
		UserID:          userID,
		ChannelType:     models.ChannelTypeRabbitMQ,
		ChannelIdentity: fmt.Sprintf("rabbitmq:%s", userID),
		Content: models.MessageContent{
			Text: payload.Text,
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType:  models.ChannelTypeRabbitMQ,
			ChannelID:    outboundChannelID,
			ReplyMode:    resolveReplyMode(replyTo),
			Capabilities: models.RabbitMQCapabilities(),
		},
		Meta:      rmqMeta,
		Timestamp: now,
	}, nil
}

// resolveUserID extracts a user ID from the AMQP delivery metadata.
// Priority: x-nipper-user header > routing key segment > queue name segment.
func resolveUserID(meta InboundMeta) string {
	if uid := meta.Headers["x-nipper-user"]; uid != "" {
		return uid
	}
	if uid := extractUserIDFromRoutingKey(meta.RoutingKey); uid != "" {
		return uid
	}
	if uid := extractUserIDFromQueue(meta.Queue); uid != "" {
		return uid
	}
	return ""
}

// extractUserIDFromRoutingKey parses "nipper.{userId}.inbox" style keys.
func extractUserIDFromRoutingKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) >= 3 && parts[0] == "nipper" && parts[len(parts)-1] == "inbox" {
		return strings.Join(parts[1:len(parts)-1], ".")
	}
	return ""
}

// extractUserIDFromQueue parses "nipper-{userId}-inbox" style queue names.
func extractUserIDFromQueue(queue string) string {
	if !strings.HasPrefix(queue, "nipper-") || !strings.HasSuffix(queue, "-inbox") {
		return ""
	}
	trimmed := strings.TrimPrefix(queue, "nipper-")
	trimmed = strings.TrimSuffix(trimmed, "-inbox")
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func resolveReplyMode(replyTo string) string {
	if replyTo != "" {
		return "reply-to"
	}
	return "direct"
}

// OutboundRoutingKey returns the routing key for publishing responses.
func OutboundRoutingKey(userID string) string {
	return fmt.Sprintf("nipper.%s.outbox", userID)
}

// InboundRoutingKey returns the routing key pattern for consuming.
func InboundRoutingKey(userID string) string {
	return fmt.Sprintf("nipper.%s.inbox", userID)
}

// InboundQueueName returns the inbound queue name for a given user.
func InboundQueueName(userID string) string {
	return fmt.Sprintf("nipper-%s-inbox", userID)
}

// OutboundQueueName returns the outbound queue name for a given user.
func OutboundQueueName(userID string) string {
	return fmt.Sprintf("nipper-%s-outbox", userID)
}
