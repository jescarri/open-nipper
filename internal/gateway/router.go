package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/channels"
	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/datastore"
	"github.com/open-nipper/open-nipper/internal/models"
	"github.com/open-nipper/open-nipper/internal/queue"
	"github.com/open-nipper/open-nipper/internal/telemetry"
)

// AllowlistChecker is the subset of the allowlist.Guard the router requires.
type AllowlistChecker interface {
	Check(ctx context.Context, userID, channelType, channelIdentity string) (bool, error)
}

// Router is the central message pipeline that processes every inbound message.
// It orchestrates: normalisation → user resolution → allowlist → session key
// resolution → deduplication → queue mode → publish.
type Router struct {
	logger    *zap.Logger
	repo      datastore.Repository
	guard     AllowlistChecker
	resolver  *Resolver
	registry  *Registry
	publisher queue.Publisher
	dedup     *Deduplicator
	cfg       *config.Config
	metrics   *telemetry.Metrics

	// collectBuffers holds per-(userId,channelType) message batches for collect mode.
	collectMu      sync.Mutex
	collectBuffers map[string]*collectBuffer
}

type collectBuffer struct {
	msgs     []*models.NipperMessage
	timer    *time.Timer
	cap      int
	mode     models.QueueMode
	priority int
}

// inboundNotifier is an optional adapter extension that can emit immediate
// user feedback right after inbound normalization.
type inboundNotifier interface {
	NotifyInbound(ctx context.Context, msg *models.NipperMessage)
}

// RouterDeps bundles the dependencies for creating a Router.
type RouterDeps struct {
	Logger    *zap.Logger
	Repo      datastore.Repository
	Guard     AllowlistChecker
	Resolver  *Resolver
	Registry  *Registry
	Publisher queue.Publisher
	Dedup     *Deduplicator
	Config    *config.Config
	Metrics   *telemetry.Metrics
}

// NewRouter creates a Router with the given dependencies.
func NewRouter(deps RouterDeps) *Router {
	return &Router{
		logger:         deps.Logger,
		repo:           deps.Repo,
		guard:          deps.Guard,
		resolver:       deps.Resolver,
		registry:       deps.Registry,
		publisher:      deps.Publisher,
		dedup:          deps.Dedup,
		cfg:            deps.Config,
		metrics:        deps.Metrics,
		collectBuffers: make(map[string]*collectBuffer),
	}
}

// HandleMessage is the main pipeline entry point for every inbound message
// arriving via webhook (WhatsApp, Slack). It normalizes raw bytes using the
// adapter and then runs the standard pipeline.
func (r *Router) HandleMessage(ctx context.Context, raw []byte, adapter channels.ChannelAdapter) error {
	msg, err := adapter.NormalizeInbound(ctx, raw)
	if err != nil {
		return fmt.Errorf("normalise: %w", err)
	}
	if msg == nil {
		return nil // adapter signalled "ignore this message"
	}
	notify := func() {}
	if notifier, ok := adapter.(inboundNotifier); ok {
		notify = func() { notifier.NotifyInbound(ctx, msg) }
	}
	return r.handleNormalizedMessage(ctx, msg, notify)
}

// HandleNormalizedMessage runs a pre-normalized NipperMessage through the
// pipeline (user resolution → allowlist → session key → dedup → publish).
// This is the entry point for push-based adapters (cron, MQTT, RabbitMQ
// channel) that normalize messages internally.
func (r *Router) HandleNormalizedMessage(ctx context.Context, msg *models.NipperMessage) error {
	return r.handleNormalizedMessage(ctx, msg, nil)
}

func (r *Router) handleNormalizedMessage(ctx context.Context, msg *models.NipperMessage, onAccepted func()) error {
	if msg == nil {
		return nil
	}

	if msg.MessageID == "" {
		msg.MessageID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	// Step 2: resolve user identity → userId
	if msg.UserID == "" {
		userID, resolveErr := r.resolveUser(ctx, msg)
		if resolveErr != nil {
			return fmt.Errorf("resolveUser: %w", resolveErr)
		}
		msg.UserID = userID
	}

	// Step 3: allowlist guard (skip for cron — it is a first-class internal
	// channel and user validity is already checked at job registration time).
	if msg.ChannelType != models.ChannelTypeCron {
		allowed, err := r.guard.Check(ctx, msg.UserID, string(msg.ChannelType), msg.ChannelIdentity)
		if err != nil {
			return fmt.Errorf("allowlist: %w", err)
		}
		if !allowed {
			telemetry.RecordMessageRejected(ctx, r.metrics, string(msg.ChannelType), "allowlist", msg.UserID)
			r.logger.Debug("message rejected by allowlist",
				zap.String("channelType", string(msg.ChannelType)),
				zap.String("userId", msg.UserID),
				zap.String("channelIdentity", msg.ChannelIdentity),
			)
			return nil // silent discard, not an error
		}
	}

	// Step 4: resolve session key
	sessionKey, err := r.resolver.Resolve(ctx, msg)
	if err != nil {
		return fmt.Errorf("resolver: %w", err)
	}
	msg.SessionKey = sessionKey

	// Step 5: register DeliveryContext + channel Meta + inbound parts.
	// The dispatcher uses inbound parts to optionally include media in the final
	// outbound response (e.g. echo inbound image attachments on WhatsApp).
	r.registry.Register(msg.SessionKey, msg.DeliveryContext, msg.Meta, msg.Content.Parts)

	// Step 6: deduplication
	dedupKey := r.deriveDedupKey(msg)
	strategy := r.dedupStrategy(msg)
	if r.dedup.IsDuplicate(msg.UserID, strategy, dedupKey) {
		r.logger.Debug("duplicate message suppressed",
			zap.String("userId", msg.UserID),
			zap.String("strategy", string(strategy)),
			zap.String("sessionKey", msg.SessionKey),
			zap.String("channelIdentity", msg.ChannelIdentity),
		)
		return nil
	}
	if onAccepted != nil {
		onAccepted()
	}

	telemetry.RecordMessageReceived(ctx, r.metrics, string(msg.ChannelType), msg.UserID)

	// Step 7: determine queue mode and build QueueItem
	mode, priority := r.queueModeFor(msg.ChannelType)

	// Step 8: collect mode buffering
	if mode == models.QueueModeCollect {
		r.enqueueCollect(msg, mode, priority)
		return nil
	}

	// Step 9: build and publish
	item := r.buildQueueItem(msg, mode, priority)

	if r.publisher == nil {
		return fmt.Errorf("publish: no RabbitMQ publisher configured")
	}
	if err := r.publisher.PublishMessage(ctx, item); err != nil {
		telemetry.RecordPublishError(ctx, r.metrics)
		return fmt.Errorf("publish: %w", err)
	}
	telemetry.RecordMessagePublished(ctx, r.metrics, string(msg.ChannelType), string(mode), msg.UserID)

	// Step 10: if interrupt mode, also send a control signal
	if mode == models.QueueModeInterrupt {
		ctrl := &models.ControlMessage{
			Type:       models.ControlMessageInterrupt,
			UserID:     msg.UserID,
			SessionKey: msg.SessionKey,
			Timestamp:  time.Now().UTC(),
		}
		if pubErr := r.publisher.PublishControl(ctx, msg.UserID, ctrl); pubErr != nil {
			r.logger.Warn("failed to publish interrupt control message",
				zap.Error(pubErr),
				zap.String("userId", msg.UserID),
			)
		}
	}

	r.logger.Info("message routed to queue",
		zap.String("userId", msg.UserID),
		zap.String("sessionKey", msg.SessionKey),
		zap.String("channelType", string(msg.ChannelType)),
		zap.String("queueMode", string(mode)),
		zap.String("messageId", msg.MessageID),
	)

	return nil
}

// resolveUser maps the channel-native identity to an Open-Nipper user ID.
func (r *Router) resolveUser(ctx context.Context, msg *models.NipperMessage) (string, error) {
	if msg.ChannelIdentity == "" {
		return "", nil // no identity to resolve
	}
	channel := string(msg.ChannelType)
	candidates := []string{msg.ChannelIdentity}
	if msg.ChannelType == models.ChannelTypeWhatsApp {
		if meta, ok := msg.Meta.(models.WhatsAppMeta); ok {
			// Some Wuzapi deliveries alternate between @lid and @s.whatsapp.net IDs.
			// Try all known identifiers before rejecting unknown identity.
			candidates = append(candidates, meta.SenderJID, meta.ChatJID)
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	uniq := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		uniq = append(uniq, c)
	}

	for _, identity := range uniq {
		userID, err := r.repo.ResolveIdentity(ctx, channel, identity)
		if err != nil {
			continue // unknown identity — try next candidate
		}
		if identity != msg.ChannelIdentity {
			r.logger.Debug("resolved user via whatsapp identity fallback",
				zap.String("channelIdentity", msg.ChannelIdentity),
				zap.String("matchedIdentity", identity),
				zap.String("userId", userID),
			)
			// Learn alias to avoid future misses when only one representation arrives.
			if addErr := r.repo.AddIdentity(ctx, userID, channel, msg.ChannelIdentity); addErr != nil {
				r.logger.Debug("failed to persist identity alias",
					zap.String("userId", userID),
					zap.String("channelType", channel),
					zap.String("channelIdentity", msg.ChannelIdentity),
					zap.Error(addErr),
				)
			}
		}
		return userID, nil
	}
	return "", nil
}

// queueModeFor returns the QueueMode and priority for a given channel.
func (r *Router) queueModeFor(ct models.ChannelType) (models.QueueMode, int) {
	if r.cfg != nil && r.cfg.Queue.PerChannel != nil {
		if chCfg, ok := r.cfg.Queue.PerChannel[string(ct)]; ok {
			mode := models.QueueMode(chCfg.Mode)
			return mode, chCfg.Priority
		}
	}
	defaultMode := models.QueueModeQueue
	if r.cfg != nil && r.cfg.Queue.DefaultMode != "" {
		defaultMode = models.QueueMode(r.cfg.Queue.DefaultMode)
	}
	return defaultMode, 0
}

// dedupStrategy picks the dedup strategy for the message. Defaults to message-id.
func (r *Router) dedupStrategy(msg *models.NipperMessage) DeduplicationStrategy {
	if msg.OriginMessageID != "" {
		return DeduplicationByMessageID
	}
	if msg.Content.Text != "" {
		return DeduplicationByPrompt
	}
	return DeduplicationNone
}

// deriveDedupKey extracts the raw key for the chosen strategy.
func (r *Router) deriveDedupKey(msg *models.NipperMessage) string {
	if msg.OriginMessageID != "" {
		return msg.OriginMessageID
	}
	if msg.Content.Text != "" {
		return PromptHash(msg.Content.Text)
	}
	return ""
}

func (r *Router) buildQueueItem(msg *models.NipperMessage, mode models.QueueMode, priority int) *models.QueueItem {
	return &models.QueueItem{
		ID:         uuid.New().String(),
		Message:    msg,
		Mode:       mode,
		Priority:   priority,
		EnqueuedAt: time.Now().UTC(),
	}
}

// enqueueCollect adds the message to a per-user,per-channel collect buffer.
// When the debounce timer fires or the cap is reached the buffer is flushed.
func (r *Router) enqueueCollect(msg *models.NipperMessage, mode models.QueueMode, priority int) {
	bufKey := fmt.Sprintf("%s:%s", msg.UserID, msg.ChannelType)

	r.collectMu.Lock()
	defer r.collectMu.Unlock()

	buf, ok := r.collectBuffers[bufKey]
	if !ok {
		capVal := 5 // default
		debounce := 2 * time.Second
		if r.cfg != nil {
			if chCfg, exists := r.cfg.Queue.PerChannel[string(msg.ChannelType)]; exists {
				if chCfg.CollectCap > 0 {
					capVal = chCfg.CollectCap
				}
				if chCfg.DebounceMS > 0 {
					debounce = time.Duration(chCfg.DebounceMS) * time.Millisecond
				}
			}
		}

		buf = &collectBuffer{
			msgs:     nil,
			cap:      capVal,
			mode:     mode,
			priority: priority,
		}
		buf.timer = time.AfterFunc(debounce, func() {
			r.flushCollectBuffer(bufKey)
		})
		r.collectBuffers[bufKey] = buf
	}

	buf.msgs = append(buf.msgs, msg)
	r.logger.Debug("collect buffer enqueued",
		zap.String("bufKey", bufKey),
		zap.String("sessionKey", msg.SessionKey),
		zap.Int("bufferLen", len(buf.msgs)),
		zap.Int("collectCap", buf.cap),
	)

	// If cap reached, flush immediately.
	if len(buf.msgs) >= buf.cap {
		buf.timer.Stop()
		go r.flushCollectBuffer(bufKey)
	}
}

// flushCollectBuffer publishes the collected messages as a single QueueItem.
func (r *Router) flushCollectBuffer(bufKey string) {
	r.collectMu.Lock()
	buf, ok := r.collectBuffers[bufKey]
	if !ok || len(buf.msgs) == 0 {
		r.collectMu.Unlock()
		return
	}
	msgs := buf.msgs
	buf.msgs = nil
	delete(r.collectBuffers, bufKey)
	r.collectMu.Unlock()

	primary := msgs[len(msgs)-1]

	item := &models.QueueItem{
		ID:                uuid.New().String(),
		Message:           primary,
		Mode:              buf.mode,
		Priority:          buf.priority,
		EnqueuedAt:        time.Now().UTC(),
		CollectedMessages: msgs,
	}

	if r.publisher == nil {
		r.logger.Error("cannot publish collected messages: no RabbitMQ publisher configured",
			zap.String("bufKey", bufKey),
			zap.Int("count", len(msgs)),
		)
		return
	}
	// Collect flush is decoupled from webhook request lifetime.
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.publisher.PublishMessage(flushCtx, item); err != nil {
		telemetry.RecordPublishError(flushCtx, r.metrics)
		r.logger.Error("failed to publish collected messages",
			zap.Error(err),
			zap.String("bufKey", bufKey),
			zap.Int("count", len(msgs)),
		)
		return
	}
	telemetry.RecordMessagePublished(flushCtx, r.metrics, string(primary.ChannelType), string(buf.mode), primary.UserID)
	r.logger.Debug("collect buffer flushed",
		zap.String("bufKey", bufKey),
		zap.String("sessionKey", primary.SessionKey),
		zap.Int("count", len(msgs)),
		zap.String("queueMode", string(buf.mode)),
	)
}
