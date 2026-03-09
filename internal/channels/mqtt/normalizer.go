package mqtt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jescarri/open-nipper/internal/models"
)

// MQTTInboundPayload is the expected JSON structure received on MQTT inbox topics.
type MQTTInboundPayload struct {
	Text          string `json:"text"`
	CorrelationID string `json:"correlationId,omitempty"`
	ResponseTopic string `json:"responseTopic,omitempty"`
	MessageID     string `json:"messageId,omitempty"`
}

// NormalizerConfig holds settings for MQTT message normalization.
type NormalizerConfig struct {
	Broker      string
	TopicPrefix string
	DefaultQoS  int
}

// NormalizeInbound converts a raw MQTT message payload into a NipperMessage.
// The topic is expected in the format "{topicPrefix}/{userId}/inbox".
// Returns (nil, nil) for messages that should be silently ignored.
func NormalizeInbound(raw []byte, topic string, cfg NormalizerConfig) (*models.NipperMessage, error) {
	userID, err := ExtractUserIDFromTopic(topic, cfg.TopicPrefix)
	if err != nil {
		return nil, fmt.Errorf("mqtt normalizer: %w", err)
	}

	var payload MQTTInboundPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("mqtt normalizer: unmarshal payload: %w", err)
	}

	if strings.TrimSpace(payload.Text) == "" {
		return nil, nil
	}

	now := time.Now().UTC()

	originMsgID := payload.MessageID
	if originMsgID == "" {
		originMsgID = fmt.Sprintf("mqtt:%s:%d", userID, now.UnixNano())
	}

	meta := models.MqttMeta{
		Broker:        cfg.Broker,
		Topic:         topic,
		QoS:           cfg.DefaultQoS,
		Retain:        false,
		ClientID:      userID,
		CorrelationID: payload.CorrelationID,
		ResponseTopic: payload.ResponseTopic,
	}

	return &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: originMsgID,
		UserID:          userID,
		ChannelType:     models.ChannelTypeMQTT,
		ChannelIdentity: fmt.Sprintf("mqtt:%s", userID),
		Content: models.MessageContent{
			Text: payload.Text,
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType:  models.ChannelTypeMQTT,
			ChannelID:    topic,
			ReplyMode:    "direct",
			Capabilities: models.MQTTCapabilities(),
		},
		Meta:      meta,
		Timestamp: now,
	}, nil
}

// ExtractUserIDFromTopic extracts the userId segment from an MQTT topic.
// Expected format: "{topicPrefix}/{userId}/inbox"
func ExtractUserIDFromTopic(topic, prefix string) (string, error) {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		prefix = "nipper"
	}

	expected := prefix + "/"
	if !strings.HasPrefix(topic, expected) {
		return "", fmt.Errorf("topic %q does not start with expected prefix %q", topic, expected)
	}

	rest := strings.TrimPrefix(topic, expected)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		return "", fmt.Errorf("topic %q has no userId segment", topic)
	}

	return parts[0], nil
}

// OutboxTopic returns the outbox topic for a given user.
func OutboxTopic(topicPrefix, userID string) string {
	prefix := strings.TrimRight(topicPrefix, "/")
	if prefix == "" {
		prefix = "nipper"
	}
	return fmt.Sprintf("%s/%s/outbox", prefix, userID)
}

// InboxTopic returns the inbox subscription topic for a given user.
func InboxTopic(topicPrefix, userID string) string {
	prefix := strings.TrimRight(topicPrefix, "/")
	if prefix == "" {
		prefix = "nipper"
	}
	return fmt.Sprintf("%s/%s/inbox", prefix, userID)
}
