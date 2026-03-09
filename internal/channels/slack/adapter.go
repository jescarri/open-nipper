package slack

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// Adapter implements the channels.ChannelAdapter interface for Slack
// messages received via the Events API and sent via the Web API.
type Adapter struct {
	cfg    config.SlackConfig
	client *SlackClient
	normCfg NormalizerConfig
	logger *zap.Logger
}

// AdapterDeps bundles the dependencies for constructing a Slack Adapter.
type AdapterDeps struct {
	Config config.SlackConfig
	Logger *zap.Logger
}

// NewAdapter creates a Slack adapter backed by the Web API.
func NewAdapter(deps AdapterDeps) *Adapter {
	client := NewSlackClient(deps.Config.BotToken, deps.Logger)
	return &Adapter{
		cfg:    deps.Config,
		client: client,
		normCfg: NormalizerConfig{
			BotToken: deps.Config.BotToken,
			AppID:    deps.Config.AppToken,
		},
		logger: deps.Logger,
	}
}

// ChannelType returns ChannelTypeSlack.
func (a *Adapter) ChannelType() models.ChannelType {
	return models.ChannelTypeSlack
}

// Start is a no-op for the Slack Events API adapter — Slack pushes events
// to our webhook endpoint, so there is nothing to connect or subscribe to.
func (a *Adapter) Start(_ context.Context) error {
	a.logger.Info("slack adapter started",
		zap.String("webhookPath", a.cfg.WebhookPath),
	)
	return nil
}

// Stop is a no-op for the HTTP-based Slack adapter.
func (a *Adapter) Stop(_ context.Context) error {
	a.logger.Info("slack adapter stopped")
	return nil
}

// HealthCheck verifies Slack API connectivity by calling auth.test.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if a.cfg.BotToken == "" {
		return fmt.Errorf("slack: bot_token not configured")
	}
	return a.client.AuthTest(ctx)
}

// NormalizeInbound converts a raw Slack Events API payload into a
// NipperMessage. Returns (nil, nil) for events that should be silently
// ignored (bot messages, non-message events, filtered subtypes).
func (a *Adapter) NormalizeInbound(ctx context.Context, raw []byte) (*models.NipperMessage, error) {
	return NormalizeInbound(raw, a.normCfg)
}

// NotifyInbound sends immediate feedback (emoji reactions) on accepted inbound messages.
func (a *Adapter) NotifyInbound(ctx context.Context, msg *models.NipperMessage) {
	if msg == nil {
		return
	}
	slackMeta, ok := msg.Meta.(models.SlackMeta)
	if !ok {
		return
	}
	token := a.client.resolveToken(slackMeta)
	channel := slackMeta.ChannelID
	if channel == "" {
		return
	}
	messageTS := slackMeta.MessageTS
	if messageTS == "" {
		return
	}
	for _, emoji := range []string{"fire", "ok_hand", "star"} {
		if err := a.client.AddReaction(ctx, token, channel, messageTS, emoji); err != nil {
			a.logger.Warn("failed to add inbound reaction",
				zap.String("emoji", emoji),
				zap.String("channel", channel),
				zap.Error(err),
			)
		}
	}
}

// DeliverResponse sends a fully-assembled NipperResponse to the user via Slack.
func (a *Adapter) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}
	return a.client.DeliverResponse(ctx, resp)
}

// DeliverEvent forwards a streaming event to Slack. Slack supports streaming
// by creating a message on the first delta and updating it as more deltas
// arrive. The done event performs the final update.
func (a *Adapter) DeliverEvent(ctx context.Context, event *models.NipperEvent) error {
	if event == nil {
		return nil
	}
	return a.client.DeliverEvent(ctx, event)
}

// SendTypingIndicator adds a :thinking_face: reaction to the user's message
// so they get immediate visual feedback while the agent is processing.
func (a *Adapter) SendTypingIndicator(ctx context.Context, dc models.DeliveryContext, meta models.ChannelMeta) error {
	slackMeta, ok := meta.(models.SlackMeta)
	if !ok {
		return nil
	}
	token := a.client.resolveToken(slackMeta)
	channel := slackMeta.ChannelID
	if channel == "" {
		channel = dc.ChannelID
	}
	messageTS := slackMeta.MessageTS
	if messageTS == "" {
		return nil
	}
	return a.client.AddReaction(ctx, token, channel, messageTS, "thinking_face")
}

// RemoveTypingIndicator removes the :thinking_face: reaction when processing is done.
func (a *Adapter) RemoveTypingIndicator(ctx context.Context, dc models.DeliveryContext, meta models.ChannelMeta) error {
	slackMeta, ok := meta.(models.SlackMeta)
	if !ok {
		return nil
	}
	token := a.client.resolveToken(slackMeta)
	channel := slackMeta.ChannelID
	if channel == "" {
		channel = dc.ChannelID
	}
	messageTS := slackMeta.MessageTS
	if messageTS == "" {
		return nil
	}
	return a.client.RemoveReaction(ctx, token, channel, messageTS, "thinking_face")
}

// SlackClientRef returns the underlying SlackClient (used in tests).
func (a *Adapter) SlackClientRef() *SlackClient {
	return a.client
}

// Validate checks that the adapter has a valid configuration.
func Validate(cfg config.SlackConfig) error {
	if cfg.BotToken == "" {
		return fmt.Errorf("slack: bot_token is required")
	}
	if cfg.SigningSecret == "" {
		return fmt.Errorf("slack: signing_secret is required")
	}
	return nil
}
