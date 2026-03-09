package gateway

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/channels"
	"github.com/open-nipper/open-nipper/internal/models"
)

const (
	accumulatorTimeout = 5 * time.Minute
	deliveryMaxRetries = 3
	deliveryRetryDelay = 1 * time.Second
)

// accumulator buffers delta text for non-streaming channels until the
// "done" event arrives.
type accumulator struct {
	builder   strings.Builder
	createdAt time.Time
	userID    string
	event     *models.NipperEvent // last event seen (carries ResponseID etc.)
}

// Dispatcher consumes NipperEvents from the event consumer and routes them
// to the correct channel adapter based on the DeliveryContext in the Registry.
type Dispatcher struct {
	logger   *zap.Logger
	registry *Registry
	adapters map[models.ChannelType]channels.ChannelAdapter

	accMu        sync.Mutex
	accumulators map[string]*accumulator

	// typingMu guards typingSent to ensure we only fire once per session.
	typingMu   sync.Mutex
	typingSent map[string]bool

	// wsMu guards wsSubscribers for WebSocket fan-out.
	wsMu          sync.RWMutex
	wsSubscribers map[string][]chan *models.NipperEvent

	cleanupDone chan struct{}
}

// NewDispatcher creates a Dispatcher. adapters maps channel types to their
// concrete adapter. The registry is used to look up DeliveryContexts.
func NewDispatcher(logger *zap.Logger, registry *Registry, adapters map[models.ChannelType]channels.ChannelAdapter) *Dispatcher {
	d := &Dispatcher{
		logger:        logger,
		registry:      registry,
		adapters:      adapters,
		accumulators:  make(map[string]*accumulator),
		typingSent:    make(map[string]bool),
		wsSubscribers: make(map[string][]chan *models.NipperEvent),
		cleanupDone:   make(chan struct{}),
	}
	go d.cleanupLoop()
	return d
}

// HandleEvent is the callback registered on the EventConsumer. It routes each
// agent event to the correct channel adapter.
func (d *Dispatcher) HandleEvent(ctx context.Context, event *models.NipperEvent) error {
	if event == nil {
		return nil
	}

	// For done/error we Consume (pop) the front entry so each response matches
	// one queued message. For delta/other events we Lookup (peek) to route.
	var dc models.DeliveryContext
	var meta models.ChannelMeta
	var inboundParts []models.ContentPart
	var found bool
	if event.Type == models.EventTypeDone || event.Type == models.EventTypeError {
		dc, meta, inboundParts, found = d.registry.Consume(event.SessionKey)
	} else {
		dc, meta, inboundParts, found = d.registry.Lookup(event.SessionKey)
	}
	if !found {
		d.logger.Warn("no delivery context for session",
			zap.String("sessionKey", event.SessionKey),
			zap.String("eventType", string(event.Type)),
		)
		return nil
	}

	// Fan out to WebSocket subscribers.
	d.fanOutToWebSocket(event)

	// Broadcast mode: deliver to all notify channels on "done".
	// This check comes before adapter lookup because broadcast sources (e.g. cron)
	// may not have an outbound adapter of their own.
	if dc.ReplyMode == "broadcast" && event.Type == models.EventTypeDone {
		d.deliverBroadcast(ctx, event, dc)
		return nil
	}

	adapter, ok := d.adapters[dc.ChannelType]
	if !ok {
		d.logger.Warn("no adapter for channel type",
			zap.String("channelType", string(dc.ChannelType)),
		)
		return nil
	}

	// Trigger typing indicator on the first thinking/tool_start event per session.
	if event.Type == models.EventTypeThinking || event.Type == models.EventTypeToolStart {
		d.maybeSendTypingIndicator(ctx, adapter, event.SessionKey, dc, meta)
	}

	// Remove typing indicator and clean up tracking on terminal events.
	if event.Type == models.EventTypeDone || event.Type == models.EventTypeError {
		d.clearTypingSent(ctx, adapter, event.SessionKey, dc, meta)
	}

	switch {
	case dc.Capabilities.SupportsStreaming:
		return d.handleStreamingEvent(ctx, event, adapter, dc, meta, inboundParts)
	default:
		return d.handleBufferedEvent(ctx, event, adapter, dc, meta, inboundParts)
	}
}

// handleStreamingEvent forwards events directly to the adapter for channels
// that support real-time streaming (e.g. Slack, WebSocket).
func (d *Dispatcher) handleStreamingEvent(ctx context.Context, event *models.NipperEvent, adapter channels.ChannelAdapter, dc models.DeliveryContext, meta models.ChannelMeta, inboundParts []models.ContentPart) error {
	if event.Type == models.EventTypeDone {
		resp := d.assembleResponseFromEvent(event, dc, meta, inboundParts)
		d.deliverWithRetry(ctx, adapter, resp)
		return nil
	}

	if err := adapter.DeliverEvent(ctx, event); err != nil {
		d.logger.Warn("streaming event delivery failed",
			zap.Error(err),
			zap.String("eventType", string(event.Type)),
		)
	}
	return nil
}

// handleBufferedEvent accumulates delta text and delivers the complete
// response on the "done" event.
func (d *Dispatcher) handleBufferedEvent(ctx context.Context, event *models.NipperEvent, adapter channels.ChannelAdapter, dc models.DeliveryContext, meta models.ChannelMeta, inboundParts []models.ContentPart) error {
	switch event.Type {
	case models.EventTypeDelta:
		d.accMu.Lock()
		acc, ok := d.accumulators[event.SessionKey]
		if !ok {
			acc = &accumulator{
				createdAt: time.Now(),
				userID:    event.UserID,
			}
			d.accumulators[event.SessionKey] = acc
		}
		if event.Delta != nil {
			acc.builder.WriteString(event.Delta.Text)
		}
		acc.event = event
		d.accMu.Unlock()

	case models.EventTypeError:
		resp := d.assembleErrorResponse(event, dc, meta)
		d.deliverWithRetry(ctx, adapter, resp)
		d.clearAccumulator(event.SessionKey)

	case models.EventTypeDone:
		resp := d.assembleFinalResponse(event, dc, meta, inboundParts)
		d.deliverWithRetry(ctx, adapter, resp)
		d.clearAccumulator(event.SessionKey)
	}
	return nil
}

// assembleFinalResponse builds a NipperResponse from the accumulated text.
func (d *Dispatcher) assembleFinalResponse(event *models.NipperEvent, dc models.DeliveryContext, meta models.ChannelMeta, inboundParts []models.ContentPart) *models.NipperResponse {
	d.accMu.Lock()
	text := ""
	if acc, ok := d.accumulators[event.SessionKey]; ok {
		text = acc.builder.String()
	}
	d.accMu.Unlock()

	// If the agent sent a done event with text in Delta, prefer that.
	if text == "" && event.Delta != nil {
		text = event.Delta.Text
	}

	return &models.NipperResponse{
		ResponseID:      event.ResponseID,
		SessionKey:      event.SessionKey,
		UserID:          event.UserID,
		ChannelType:     dc.ChannelType,
		Text:            text,
		Parts:           outboundMediaParts(dc, inboundParts),
		DeliveryContext: dc,
		Meta:            meta,
		Timestamp:       time.Now().UTC(),
		ContextUsage:    event.ContextUsage,
	}
}

func (d *Dispatcher) assembleResponseFromEvent(event *models.NipperEvent, dc models.DeliveryContext, meta models.ChannelMeta, inboundParts []models.ContentPart) *models.NipperResponse {
	text := ""
	if event.Delta != nil {
		text = event.Delta.Text
	}
	return &models.NipperResponse{
		ResponseID:      event.ResponseID,
		SessionKey:      event.SessionKey,
		UserID:          event.UserID,
		ChannelType:     dc.ChannelType,
		Text:            text,
		Parts:           outboundMediaParts(dc, inboundParts),
		DeliveryContext: dc,
		Meta:            meta,
		Timestamp:       time.Now().UTC(),
		ContextUsage:    event.ContextUsage,
	}
}

// outboundMediaParts selects media parts to include in the final channel
// response. For WhatsApp we echo back the first inbound image-like attachment
// so users get text + image in a single response.
func outboundMediaParts(dc models.DeliveryContext, inboundParts []models.ContentPart) []models.ContentPart {
	if dc.ChannelType != models.ChannelTypeWhatsApp {
		return nil
	}
	for _, p := range inboundParts {
		if p.URL == "" {
			continue
		}
		if p.Type == "image" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.MimeType)), "image/") {
			return []models.ContentPart{
				{
					Type:     "image",
					URL:      p.URL,
					MimeType: p.MimeType,
				},
			}
		}
	}
	return nil
}

func (d *Dispatcher) assembleErrorResponse(event *models.NipperEvent, dc models.DeliveryContext, meta models.ChannelMeta) *models.NipperResponse {
	text := "An error occurred while processing your message."
	if event.Error != nil && event.Error.Message != "" {
		text = event.Error.Message
	}
	return &models.NipperResponse{
		ResponseID:      uuid.New().String(),
		SessionKey:      event.SessionKey,
		UserID:          event.UserID,
		ChannelType:     dc.ChannelType,
		Text:            text,
		DeliveryContext: dc,
		Meta:            meta,
		Timestamp:       time.Now().UTC(),
	}
}

func (d *Dispatcher) clearAccumulator(sessionKey string) {
	d.accMu.Lock()
	delete(d.accumulators, sessionKey)
	d.accMu.Unlock()
}

// maybeSendTypingIndicator fires the typing indicator exactly once per session.
func (d *Dispatcher) maybeSendTypingIndicator(ctx context.Context, adapter channels.ChannelAdapter, sessionKey string, dc models.DeliveryContext, meta models.ChannelMeta) {
	d.typingMu.Lock()
	if d.typingSent[sessionKey] {
		d.typingMu.Unlock()
		return
	}
	d.typingSent[sessionKey] = true
	d.typingMu.Unlock()

	ti, ok := adapter.(channels.TypingIndicator)
	if !ok {
		return
	}
	if err := ti.SendTypingIndicator(ctx, dc, meta); err != nil {
		d.logger.Warn("failed to send typing indicator",
			zap.String("sessionKey", sessionKey),
			zap.String("channelType", string(dc.ChannelType)),
			zap.Error(err),
		)
	}
}

// clearTypingSent removes the typing-sent flag and, if the adapter supports it,
// removes the typing indicator (e.g. Slack reaction removal).
func (d *Dispatcher) clearTypingSent(ctx context.Context, adapter channels.ChannelAdapter, sessionKey string, dc models.DeliveryContext, meta models.ChannelMeta) {
	d.typingMu.Lock()
	wasSent := d.typingSent[sessionKey]
	delete(d.typingSent, sessionKey)
	d.typingMu.Unlock()

	if !wasSent {
		return
	}

	tr, ok := adapter.(channels.TypingIndicatorRemover)
	if !ok {
		return
	}
	if err := tr.RemoveTypingIndicator(ctx, dc, meta); err != nil {
		d.logger.Warn("failed to remove typing indicator",
			zap.String("sessionKey", sessionKey),
			zap.String("channelType", string(dc.ChannelType)),
			zap.Error(err),
		)
	}
}

// deliverBroadcast sends the done response to all notify channels.
// It assembles the response body from the accumulator (same session) and sets
// channel-specific Meta so each adapter (e.g. WhatsApp, Slack) can deliver.
func (d *Dispatcher) deliverBroadcast(ctx context.Context, event *models.NipperEvent, dc models.DeliveryContext) {
	// Build response text from accumulator (cron session accumulated deltas).
	text := ""
	d.accMu.Lock()
	if acc, ok := d.accumulators[event.SessionKey]; ok {
		text = acc.builder.String()
	}
	d.accMu.Unlock()
	if text == "" && event.Delta != nil {
		text = event.Delta.Text
	}

	for _, target := range dc.NotifyChannels {
		parts := strings.SplitN(target, ":", 2)
		if len(parts) != 2 {
			d.logger.Warn("invalid notifyChannels format (expected channelType:channelId, e.g. whatsapp:5491155553935@s.whatsapp.net)",
				zap.String("target", target))
			continue
		}
		ct := models.ChannelType(parts[0])
		channelID := parts[1]
		adapter, ok := d.adapters[ct]
		if !ok {
			d.logger.Warn("broadcast target channel not found", zap.String("channelType", string(ct)))
			continue
		}

		resp := &models.NipperResponse{
			ResponseID:  event.ResponseID,
			SessionKey:  event.SessionKey,
			UserID:      event.UserID,
			ChannelType: ct,
			Text:        text,
			DeliveryContext: models.DeliveryContext{
				ChannelType: ct,
				ChannelID:   channelID,
			},
			Timestamp:    time.Now().UTC(),
			ContextUsage: event.ContextUsage,
		}

		// Set channel-specific Meta required by each adapter for delivery.
		switch ct {
		case models.ChannelTypeWhatsApp:
			resp.Meta = models.WhatsAppMeta{ChatJID: channelID, SenderJID: channelID}
		case models.ChannelTypeSlack:
			resp.Meta = models.SlackMeta{ChannelID: channelID}
		}

		d.deliverWithRetry(ctx, adapter, resp)
	}

	// Clear accumulator for this session after broadcast (same as handleBufferedEvent).
	d.clearAccumulator(event.SessionKey)
}

// SubscribeWebSocket registers a channel to receive events for a session key.
func (d *Dispatcher) SubscribeWebSocket(sessionKey string, ch chan *models.NipperEvent) {
	d.wsMu.Lock()
	defer d.wsMu.Unlock()
	d.wsSubscribers[sessionKey] = append(d.wsSubscribers[sessionKey], ch)
}

// UnsubscribeWebSocket removes a subscriber channel.
func (d *Dispatcher) UnsubscribeWebSocket(sessionKey string, ch chan *models.NipperEvent) {
	d.wsMu.Lock()
	defer d.wsMu.Unlock()
	subs := d.wsSubscribers[sessionKey]
	for i, s := range subs {
		if s == ch {
			d.wsSubscribers[sessionKey] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(d.wsSubscribers[sessionKey]) == 0 {
		delete(d.wsSubscribers, sessionKey)
	}
}

func (d *Dispatcher) fanOutToWebSocket(event *models.NipperEvent) {
	d.wsMu.RLock()
	subs := d.wsSubscribers[event.SessionKey]
	d.wsMu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// Subscriber slow — drop event rather than blocking.
		}
	}
}

// deliverWithRetry attempts to deliver a response up to deliveryMaxRetries times
// with deliveryRetryDelay between attempts. It does not return an error to the
// caller — failures are logged but the event is not nacked.
func (d *Dispatcher) deliverWithRetry(ctx context.Context, adapter channels.ChannelAdapter, resp *models.NipperResponse) {
	var lastErr error
	for attempt := 1; attempt <= deliveryMaxRetries; attempt++ {
		if err := adapter.DeliverResponse(ctx, resp); err != nil {
			lastErr = err
			d.logger.Warn("delivery attempt failed",
				zap.Int("attempt", attempt),
				zap.Int("maxRetries", deliveryMaxRetries),
				zap.String("sessionKey", resp.SessionKey),
				zap.String("channelType", string(resp.ChannelType)),
				zap.Error(err),
			)
			if attempt < deliveryMaxRetries {
				select {
				case <-ctx.Done():
					d.logger.Error("delivery cancelled during retry",
						zap.String("sessionKey", resp.SessionKey),
						zap.Error(ctx.Err()),
					)
					return
				case <-time.After(deliveryRetryDelay):
				}
			}
			continue
		}
		return
	}
	d.logger.Error("delivery failed after all retries",
		zap.Int("retries", deliveryMaxRetries),
		zap.String("sessionKey", resp.SessionKey),
		zap.String("channelType", string(resp.ChannelType)),
		zap.Error(lastErr),
	)
}

// cleanupLoop evicts stale accumulators and expired entries.
func (d *Dispatcher) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.cleanupDone:
			return
		case <-ticker.C:
			d.evictStaleAccumulators()
		}
	}
}

func (d *Dispatcher) evictStaleAccumulators() {
	cutoff := time.Now().Add(-accumulatorTimeout)
	d.accMu.Lock()
	for k, acc := range d.accumulators {
		if acc.createdAt.Before(cutoff) {
			delete(d.accumulators, k)
		}
	}
	d.accMu.Unlock()

	// Also evict stale typing-sent entries that were never cleared (e.g. agent crash).
	d.typingMu.Lock()
	defer d.typingMu.Unlock()
	// typingSent has no timestamps, but if the accumulator was evicted the
	// session is stale — clear its typing flag too.
	for k := range d.typingSent {
		d.accMu.Lock()
		_, hasAcc := d.accumulators[k]
		d.accMu.Unlock()
		if !hasAcc {
			delete(d.typingSent, k)
		}
	}
}

// Stop shuts down background goroutines.
func (d *Dispatcher) Stop() {
	select {
	case <-d.cleanupDone:
	default:
		close(d.cleanupDone)
	}
}
