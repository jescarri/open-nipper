package whatsapp

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

// Adapter implements the channels.ChannelAdapter interface for WhatsApp
// messages delivered via the Wuzapi gateway.
type Adapter struct {
	cfg        config.WhatsAppConfig
	wuzapi     *WuzapiClient
	normCfg    NormalizerConfig
	logger     *zap.Logger
	gatewayURL string
}

// AdapterDeps bundles the dependencies for constructing a WhatsApp Adapter.
type AdapterDeps struct {
	Config     config.WhatsAppConfig
	S3Config   config.S3DefaultConfig
	Logger     *zap.Logger
	GatewayURL string // e.g. "http://127.0.0.1:18789"
}

// NewAdapter creates a WhatsApp adapter backed by Wuzapi.
func NewAdapter(deps AdapterDeps) *Adapter {
	wuzapi := NewWuzapiClient(
		deps.Config.WuzapiBaseURL,
		deps.Config.WuzapiToken,
		deps.Config.Delivery,
		deps.S3Config,
		deps.Logger,
	)
	return &Adapter{
		cfg:    deps.Config,
		wuzapi: wuzapi,
		normCfg: NormalizerConfig{
			WuzapiBaseURL:      deps.Config.WuzapiBaseURL,
			WuzapiInstanceName: deps.Config.WuzapiInstanceName,
		},
		logger:     deps.Logger,
		gatewayURL: deps.GatewayURL,
	}
}

// ChannelType returns ChannelTypeWhatsApp.
func (a *Adapter) ChannelType() models.ChannelType {
	return models.ChannelTypeWhatsApp
}

// Start configures Wuzapi HMAC signing (if a key is set), then registers the
// webhook URL and verifies connectivity.
func (a *Adapter) Start(ctx context.Context) error {
	if a.cfg.WuzapiHMACKey != "" {
		if err := a.wuzapi.ConfigureHMAC(ctx, a.cfg.WuzapiHMACKey); err != nil {
			a.logger.Warn("failed to configure Wuzapi HMAC key; webhook signatures may fail",
				zap.Error(err),
			)
		} else {
			a.logger.Info("wuzapi HMAC key configured")
		}
	}

	webhookPath := a.cfg.WebhookPath
	if webhookPath == "" {
		webhookPath = "/webhook/whatsapp"
	}
	webhookURL := a.gatewayURL + webhookPath

	events := a.cfg.Events
	if len(events) == 0 {
		events = []string{"Message", "ReadReceipt", "ChatPresence", "Connected", "Disconnected"}
	}

	if err := a.wuzapi.RegisterWebhook(ctx, webhookURL, events); err != nil {
		a.logger.Warn("failed to register Wuzapi webhook; gateway will still start",
			zap.Error(err),
			zap.String("webhookURL", webhookURL),
		)
		return nil
	}

	a.logger.Info("whatsapp adapter started",
		zap.String("webhookURL", webhookURL),
		zap.Strings("events", events),
	)
	return nil
}

// Stop is a no-op for the Wuzapi-backed adapter (HTTP webhooks don't require
// an active connection to tear down).
func (a *Adapter) Stop(_ context.Context) error {
	a.logger.Info("whatsapp adapter stopped")
	return nil
}

// HealthCheck verifies that Wuzapi is reachable.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	return a.wuzapi.HealthCheck(ctx)
}

// NormalizeInbound converts a raw Wuzapi webhook JSON payload into a
// NipperMessage. Returns (nil, nil) for events that should be silently
// ignored (self-messages, non-Message events).
func (a *Adapter) NormalizeInbound(ctx context.Context, raw []byte) (*models.NipperMessage, error) {
	return NormalizeInbound(raw, a.normCfg)
}

// NotifyInbound sends immediate feedback to the chat for accepted inbound
// messages (read + typing), useful for debounced queue modes.
func (a *Adapter) NotifyInbound(ctx context.Context, msg *models.NipperMessage) {
	if msg == nil {
		return
	}
	meta, ok := msg.Meta.(models.WhatsAppMeta)
	if !ok {
		return
	}
	if err := a.wuzapi.SendInboundFeedback(ctx, meta); err != nil {
		a.logger.Warn("failed to send inbound WhatsApp feedback",
			zap.String("sessionKey", msg.SessionKey),
			zap.String("messageId", msg.OriginMessageID),
			zap.Error(err),
		)
	}
}

// DeliverResponse sends a fully-assembled NipperResponse to the user via Wuzapi.
func (a *Adapter) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	if resp == nil {
		return nil
	}
	return a.wuzapi.DeliverResponse(ctx, resp)
}

// DeliverEvent is a no-op for WhatsApp because the channel does not support
// streaming. The dispatcher buffers delta events and calls DeliverResponse
// on the "done" event.
func (a *Adapter) DeliverEvent(_ context.Context, _ *models.NipperEvent) error {
	return nil
}

// SendTypingIndicator sets the WhatsApp "composing" presence so the user
// sees a typing bubble as soon as the agent starts thinking.
func (a *Adapter) SendTypingIndicator(ctx context.Context, dc models.DeliveryContext, meta models.ChannelMeta) error {
	if !a.cfg.Delivery.ShowTyping {
		return nil
	}
	waMeta, ok := meta.(models.WhatsAppMeta)
	if !ok {
		return nil
	}
	targetJID := resolveTargetJID(waMeta)
	phone := phoneFromJID(targetJID)
	if phone == "" {
		return nil
	}
	return a.wuzapi.setPresence(ctx, phone, "composing")
}

// WuzapiClient returns the underlying Wuzapi client (used in tests).
func (a *Adapter) WuzapiClientRef() *WuzapiClient {
	return a.wuzapi
}

// Validate checks that the adapter has a valid configuration.
func Validate(cfg config.WhatsAppConfig) error {
	if cfg.WuzapiBaseURL == "" {
		return fmt.Errorf("whatsapp: wuzapi_base_url is required")
	}
	if cfg.WuzapiToken == "" {
		return fmt.Errorf("whatsapp: wuzapi_token is required")
	}
	return nil
}
