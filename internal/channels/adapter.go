// Package channels defines the ChannelAdapter interface that every inbound
// channel (WhatsApp, Slack, MQTT, RabbitMQ, Cron) must implement.
package channels

import (
	"context"

	"github.com/jescarri/open-nipper/internal/models"
)

// TypingIndicator is an optional interface that channel adapters can implement
// to show typing/presence indicators when the agent starts processing.
// The dispatcher calls SendTypingIndicator on the first "thinking" or
// "tool_start" event for a session, giving users immediate feedback.
type TypingIndicator interface {
	SendTypingIndicator(ctx context.Context, dc models.DeliveryContext, meta models.ChannelMeta) error
}

// TypingIndicatorRemover is an optional interface for adapters that need to
// explicitly remove the typing indicator when processing completes (e.g.
// Slack reaction removal). WhatsApp handles this implicitly via setPresence("paused")
// in DeliverResponse, so it does not need this interface.
type TypingIndicatorRemover interface {
	RemoveTypingIndicator(ctx context.Context, dc models.DeliveryContext, meta models.ChannelMeta) error
}

// ChannelAdapter is the contract for a bidirectional channel.
// The gateway interacts with every channel exclusively through this interface.
type ChannelAdapter interface {
	// ChannelType returns the canonical channel identifier.
	ChannelType() models.ChannelType

	// Start initialises the adapter (e.g. connect to broker, register webhooks).
	Start(ctx context.Context) error

	// Stop gracefully shuts down the adapter.
	Stop(ctx context.Context) error

	// HealthCheck returns nil if the adapter is operational.
	HealthCheck(ctx context.Context) error

	// NormalizeInbound converts a channel-native payload into a NipperMessage.
	// Returning (nil, nil) signals that the message should be silently ignored
	// (e.g. self-messages, non-Message events, filtered subtypes).
	NormalizeInbound(ctx context.Context, raw []byte) (*models.NipperMessage, error)

	// DeliverResponse sends a fully-assembled response to the user's channel.
	// Used for non-streaming channels (WhatsApp, MQTT, RabbitMQ) or as the
	// final delivery on streaming channels.
	DeliverResponse(ctx context.Context, resp *models.NipperResponse) error

	// DeliverEvent forwards a single streaming event to the user's channel.
	// For non-streaming channels this is typically a no-op; the dispatcher
	// buffers deltas and calls DeliverResponse on the "done" event instead.
	DeliverEvent(ctx context.Context, event *models.NipperEvent) error
}
