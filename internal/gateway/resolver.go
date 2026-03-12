// Package gateway contains the HTTP server, message pipeline, and routing
// logic for the Open-Nipper gateway process.
package gateway

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/models"
	pkgsess "github.com/jescarri/open-nipper/pkg/session"
)

// Resolver determines the session key for an inbound NipperMessage.
//
// In the distributed architecture the gateway never touches session files.
// This resolver is a pure function: it derives a deterministic, filesystem-safe
// session key from channel-specific context and populates msg.SessionKey.
// Session creation and lifecycle management are handled by the agent that
// consumes the message.
type Resolver struct {
	logger       *zap.Logger
	defaultModel string
}

// NewResolver creates a Resolver.  The defaultModel value is included in the
// NipperMessage metadata so that agents know which model to use when creating
// a new session.
func NewResolver(logger *zap.Logger, defaultModel string) *Resolver {
	return &Resolver{
		logger:       logger,
		defaultModel: defaultModel,
	}
}

// DefaultModel returns the configured default model name.
func (r *Resolver) DefaultModel() string {
	return r.defaultModel
}

// Resolve returns the canonical session key for msg.
// It does NOT create session files — that is the agent's responsibility.
func (r *Resolver) Resolve(_ context.Context, msg *models.NipperMessage) (string, error) {
	if msg.UserID == "" {
		return "", fmt.Errorf("resolver: message has no userId")
	}

	sessionID := r.deriveSessionID(msg)
	sessionKey := pkgsess.BuildSessionKey(msg.UserID, string(msg.ChannelType), sessionID)

	r.logger.Debug("session key resolved",
		zap.String("userId", msg.UserID),
		zap.String("sessionKey", sessionKey),
		zap.String("channelType", string(msg.ChannelType)),
	)
	return sessionKey, nil
}

// deriveSessionID returns the stable, filesystem-safe session identifier for
// msg based on channel-specific context:
//
//   - WhatsApp  → chatJID  (group or contact JID — NOT senderJID)
//   - Slack     → channelID [+ threadTS when in a thread]
//   - Cron      → jobID
//   - MQTT      → clientID
//   - RabbitMQ  → correlationID if present, otherwise routingKey
//   - fallback  → channelIdentity or messageID
func (r *Resolver) deriveSessionID(msg *models.NipperMessage) string {
	switch msg.ChannelType {
	case models.ChannelTypeWhatsApp:
		if meta, ok := msg.Meta.(models.WhatsAppMeta); ok && meta.ChatJID != "" {
			return pkgsess.SanitizeSessionID(meta.ChatJID)
		}

	case models.ChannelTypeSlack:
		if meta, ok := msg.Meta.(models.SlackMeta); ok && meta.ChannelID != "" {
			if meta.ThreadTS != "" {
				return pkgsess.SanitizeSessionID(meta.ChannelID + "-" + meta.ThreadTS)
			}
			return pkgsess.SanitizeSessionID(meta.ChannelID)
		}

	case models.ChannelTypeCron:
		if meta, ok := msg.Meta.(models.CronMeta); ok && meta.JobID != "" {
			return pkgsess.SanitizeSessionID(meta.JobID)
		}

	case models.ChannelTypeMQTT:
		if meta, ok := msg.Meta.(models.MqttMeta); ok && meta.ClientID != "" {
			return pkgsess.SanitizeSessionID(meta.ClientID)
		}

	case models.ChannelTypeRabbitMQ:
		if meta, ok := msg.Meta.(models.RabbitMqMeta); ok {
			if meta.CorrelationID != "" {
				return pkgsess.SanitizeSessionID(meta.CorrelationID)
			}
			if meta.RoutingKey != "" {
				return pkgsess.SanitizeSessionID(meta.RoutingKey)
			}
		}
	}

	if msg.ChannelIdentity != "" {
		return pkgsess.SanitizeSessionID(msg.ChannelIdentity)
	}
	return pkgsess.SanitizeSessionID(msg.MessageID)
}
