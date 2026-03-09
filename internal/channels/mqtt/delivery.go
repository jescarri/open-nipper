package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

const publishTimeout = 10 * time.Second

// MQTTPublisher abstracts the MQTT publish operation for testability.
type MQTTPublisher interface {
	Publish(topic string, qos byte, retained bool, payload []byte) error
	IsConnected() bool
}

// DeliveryClient delivers NipperResponses by publishing them to MQTT topics.
type DeliveryClient struct {
	publisher   MQTTPublisher
	topicPrefix string
	defaultQoS  byte
	logger      *zap.Logger
}

// NewDeliveryClient creates a delivery client backed by an MQTT publisher.
func NewDeliveryClient(publisher MQTTPublisher, topicPrefix string, defaultQoS int, logger *zap.Logger) *DeliveryClient {
	qos := byte(defaultQoS)
	if qos > 2 {
		qos = 1
	}
	return &DeliveryClient{
		publisher:   publisher,
		topicPrefix: topicPrefix,
		defaultQoS:  qos,
		logger:      logger,
	}
}

// DeliverResponse publishes a NipperResponse to the appropriate MQTT topic.
// If the inbound message specified a responseTopic in MqttMeta, that topic is used.
// Otherwise, the response is published to {topicPrefix}/{userId}/outbox.
func (d *DeliveryClient) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}

	if d.publisher == nil {
		return fmt.Errorf("mqtt delivery: publisher is nil")
	}

	if !d.publisher.IsConnected() {
		return fmt.Errorf("mqtt delivery: not connected to broker")
	}

	topic := d.resolveOutboundTopic(resp)
	qos := d.resolveQoS(resp)

	payload, err := d.buildPayload(resp)
	if err != nil {
		return fmt.Errorf("mqtt delivery: %w", err)
	}

	d.logger.Debug("publishing mqtt response",
		zap.String("topic", topic),
		zap.Int("qos", int(qos)),
		zap.String("userId", resp.UserID),
		zap.String("sessionKey", resp.SessionKey),
	)

	if err := d.publisher.Publish(topic, qos, false, payload); err != nil {
		return fmt.Errorf("mqtt delivery: publish to %s: %w", topic, err)
	}

	return nil
}

func (d *DeliveryClient) resolveOutboundTopic(resp *models.NipperResponse) string {
	if meta, ok := extractMQTTMeta(resp); ok && meta.ResponseTopic != "" {
		return meta.ResponseTopic
	}
	return OutboxTopic(d.topicPrefix, resp.UserID)
}

func (d *DeliveryClient) resolveQoS(resp *models.NipperResponse) byte {
	if meta, ok := extractMQTTMeta(resp); ok {
		qos := byte(meta.QoS)
		if qos <= 2 {
			return qos
		}
	}
	return d.defaultQoS
}

// MQTTResponsePayload is the JSON structure published to MQTT outbox topics.
type MQTTResponsePayload struct {
	ResponseID    string               `json:"responseId"`
	SessionKey    string               `json:"sessionKey"`
	UserID        string               `json:"userId"`
	Text          string               `json:"text"`
	Parts         []models.ContentPart `json:"parts,omitempty"`
	CorrelationID string               `json:"correlationId,omitempty"`
	Timestamp     string               `json:"timestamp"`
}

func (d *DeliveryClient) buildPayload(resp *models.NipperResponse) ([]byte, error) {
	payload := MQTTResponsePayload{
		ResponseID: resp.ResponseID,
		SessionKey: resp.SessionKey,
		UserID:     resp.UserID,
		Text:       resp.Text,
		Parts:      resp.Parts,
		Timestamp:  resp.Timestamp.Format(time.RFC3339),
	}

	if meta, ok := extractMQTTMeta(resp); ok {
		payload.CorrelationID = meta.CorrelationID
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal response payload: %w", err)
	}
	return data, nil
}

func extractMQTTMeta(resp *models.NipperResponse) (models.MqttMeta, bool) {
	if resp.Meta != nil {
		if m, ok := resp.Meta.(models.MqttMeta); ok {
			return m, true
		}
	}
	return models.MqttMeta{}, false
}
