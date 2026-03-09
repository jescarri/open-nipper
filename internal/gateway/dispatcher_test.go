package gateway

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/channels"
	"github.com/jescarri/open-nipper/internal/models"
)

// --- dispatcher mock adapter ---

type dispatcherMockAdapter struct {
	ct        models.ChannelType
	mu        sync.Mutex
	responses []*models.NipperResponse
	events    []*models.NipperEvent
	delivErr  error
}

func (a *dispatcherMockAdapter) ChannelType() models.ChannelType           { return a.ct }
func (a *dispatcherMockAdapter) Start(context.Context) error                { return nil }
func (a *dispatcherMockAdapter) Stop(context.Context) error                 { return nil }
func (a *dispatcherMockAdapter) HealthCheck(context.Context) error          { return nil }
func (a *dispatcherMockAdapter) NormalizeInbound(context.Context, []byte) (*models.NipperMessage, error) {
	return nil, nil
}

func (a *dispatcherMockAdapter) DeliverResponse(_ context.Context, resp *models.NipperResponse) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.responses = append(a.responses, resp)
	return a.delivErr
}

func (a *dispatcherMockAdapter) DeliverEvent(_ context.Context, evt *models.NipperEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, evt)
	return a.delivErr
}

var _ channels.ChannelAdapter = (*dispatcherMockAdapter)(nil)

// retryCountAdapter tracks delivery attempts and fails the first N calls.
type retryCountAdapter struct {
	dispatcherMockAdapter
	failCount int
	attempts  int
}

func (a *retryCountAdapter) DeliverResponse(_ context.Context, resp *models.NipperResponse) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts++
	if a.attempts <= a.failCount {
		return fmt.Errorf("simulated failure #%d", a.attempts)
	}
	a.responses = append(a.responses, resp)
	return nil
}

var _ channels.ChannelAdapter = (*retryCountAdapter)(nil)

// --- helpers ---

func newDispatcher(adapters map[models.ChannelType]channels.ChannelAdapter) (*Dispatcher, *Registry) {
	reg := NewRegistry()
	d := NewDispatcher(zap.NewNop(), reg, adapters)
	return d, reg
}

func registerSession(reg *Registry, key string, ct models.ChannelType, streaming bool, inboundParts ...models.ContentPart) {
	reg.Register(key, models.DeliveryContext{
		ChannelType: ct,
		ChannelID:   "test-channel",
		Capabilities: models.ChannelCapabilities{
			SupportsStreaming: streaming,
		},
	}, nil, inboundParts)
}

// --- Tests ---

func TestDispatcher_BufferedDeltaThenDone(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ctx := context.Background()

	// Send deltas
	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
		Delta:      &models.EventDelta{Text: "Hello "},
	})
	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
		Delta:      &models.EventDelta{Text: "World"},
	})

	// Send done
	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
	})

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(adapter.responses))
	}
	if adapter.responses[0].Text != "Hello World" {
		t.Fatalf("expected 'Hello World', got %q", adapter.responses[0].Text)
	}
}

func TestDispatcher_StreamingForwardsEvents(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeSlack}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeSlack: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:slack:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeSlack, true)

	ctx := context.Background()

	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
		Delta:      &models.EventDelta{Text: "streaming..."},
	})

	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
	})

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.events) != 1 {
		t.Fatalf("expected 1 streamed event, got %d", len(adapter.events))
	}
	if len(adapter.responses) != 1 {
		t.Fatalf("expected 1 final response on done, got %d", len(adapter.responses))
	}
}

func TestDispatcher_ErrorEvent(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ctx := context.Background()

	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeError,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
		Error: &models.EventError{
			Code:    "agent_error",
			Message: "something went wrong",
		},
	})

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.responses) != 1 {
		t.Fatalf("expected 1 error response, got %d", len(adapter.responses))
	}
	if adapter.responses[0].Text != "something went wrong" {
		t.Fatalf("expected error text, got %q", adapter.responses[0].Text)
	}
}

func TestDispatcher_NoDeliveryContext(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, _ := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	err := d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: "nonexistent-key",
		UserID:     "u1",
	})
	if err != nil {
		t.Fatal("missing DC should not error, just warn")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.responses) != 0 {
		t.Fatal("no response should be delivered without DC")
	}
}

func TestDispatcher_NilEvent(t *testing.T) {
	d, _ := newDispatcher(nil)
	defer d.Stop()

	err := d.HandleEvent(context.Background(), nil)
	if err != nil {
		t.Fatal("nil event should be a no-op")
	}
}

func TestDispatcher_BroadcastDelivery(t *testing.T) {
	waAdapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	slackAdapter := &dispatcherMockAdapter{ct: models.ChannelTypeSlack}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: waAdapter,
		models.ChannelTypeSlack:    slackAdapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:cron:session:daily"
	reg.Register(sessionKey, models.DeliveryContext{
		ChannelType:    models.ChannelTypeCron,
		ReplyMode:      "broadcast",
		NotifyChannels: []string{"whatsapp:1555010001@s.whatsapp.net", "slack:C0789GHI"},
	}, nil, nil)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
	})

	waAdapter.mu.Lock()
	slackAdapter.mu.Lock()
	defer waAdapter.mu.Unlock()
	defer slackAdapter.mu.Unlock()

	if len(waAdapter.responses) != 1 {
		t.Fatalf("expected 1 WA broadcast, got %d", len(waAdapter.responses))
	}
	if len(slackAdapter.responses) != 1 {
		t.Fatalf("expected 1 Slack broadcast, got %d", len(slackAdapter.responses))
	}
}

func TestDispatcher_WebSocketFanOut(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ch := make(chan *models.NipperEvent, 10)
	d.SubscribeWebSocket(sessionKey, ch)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
		Delta:      &models.EventDelta{Text: "ws-text"},
	})

	select {
	case evt := <-ch:
		if evt.Delta == nil || evt.Delta.Text != "ws-text" {
			t.Fatal("ws subscriber should get the delta")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ws event")
	}

	d.UnsubscribeWebSocket(sessionKey, ch)
}

func TestDispatcher_DoneRemovesRegistry(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
	})

	if _, _, _, ok := reg.Lookup(sessionKey); ok {
		t.Fatal("registry entry should be removed after done event")
	}
}

func TestDispatcher_QueuedMessagesEachGetDelivery(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:queued"
	// Simulate 3 messages queued for same session - each Register adds one entry.
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ctx := context.Background()

	// Send 3 done events (agent processed 3 messages)
	for i := 0; i < 3; i++ {
		_ = d.HandleEvent(ctx, &models.NipperEvent{
			Type:       models.EventTypeDone,
			SessionKey: sessionKey,
			ResponseID: fmt.Sprintf("resp-%d", i+1),
			UserID:     "u1",
			Delta:      &models.EventDelta{Text: fmt.Sprintf("response %d", i+1)},
		})
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.responses) != 3 {
		t.Fatalf("expected 3 responses for 3 queued messages, got %d", len(adapter.responses))
	}
	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("response %d", i+1)
		if adapter.responses[i].Text != expected {
			t.Errorf("response %d: expected %q, got %q", i, expected, adapter.responses[i].Text)
		}
	}
}

func TestDispatcher_WhatsAppDoneIncludesInboundImagePart(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:image-echo"
	registerSession(
		reg,
		sessionKey,
		models.ChannelTypeWhatsApp,
		false,
		models.ContentPart{
			Type:     "document",
			URL:      "https://s3.example.com/cyclist.jpg",
			MimeType: "image/jpeg",
		},
	)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-image",
		UserID:     "u1",
		Delta:      &models.EventDelta{Text: "analysis"},
	})

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(adapter.responses))
	}
	if len(adapter.responses[0].Parts) != 1 {
		t.Fatalf("expected 1 image part, got %d", len(adapter.responses[0].Parts))
	}
	if adapter.responses[0].Parts[0].Type != "image" {
		t.Fatalf("expected part type image, got %q", adapter.responses[0].Parts[0].Type)
	}
	if adapter.responses[0].Parts[0].URL != "https://s3.example.com/cyclist.jpg" {
		t.Fatalf("unexpected part URL: %q", adapter.responses[0].Parts[0].URL)
	}
}

func TestDispatcher_AccumulatorClearedOnDone(t *testing.T) {
	adapter := &dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp}
	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: adapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:s1"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ctx := context.Background()
	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: sessionKey,
		Delta:      &models.EventDelta{Text: "data"},
		UserID:     "u1",
	})

	d.accMu.Lock()
	if _, ok := d.accumulators[sessionKey]; !ok {
		d.accMu.Unlock()
		t.Fatal("accumulator should exist after delta")
	}
	d.accMu.Unlock()

	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-1",
		UserID:     "u1",
	})

	d.accMu.Lock()
	if _, ok := d.accumulators[sessionKey]; ok {
		d.accMu.Unlock()
		t.Fatal("accumulator should be cleared after done")
	}
	d.accMu.Unlock()
}

func TestDispatcher_StopIdempotent(t *testing.T) {
	d, _ := newDispatcher(nil)
	d.Stop()
	d.Stop() // should not panic
}

func TestDispatcher_DeliveryRetryOnError(t *testing.T) {
	retryAdapter := &retryCountAdapter{
		dispatcherMockAdapter: dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp},
		failCount:             2,
	}

	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: retryAdapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:retry"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-retry",
		UserID:     "u1",
	})

	retryAdapter.mu.Lock()
	defer retryAdapter.mu.Unlock()
	if retryAdapter.attempts != 3 {
		t.Fatalf("expected 3 attempts (2 fail + 1 success), got %d", retryAdapter.attempts)
	}
	if len(retryAdapter.responses) != 1 {
		t.Fatalf("expected 1 successful delivery, got %d", len(retryAdapter.responses))
	}
}

func TestDispatcher_DeliveryRetryAllFail(t *testing.T) {
	retryAdapter := &retryCountAdapter{
		dispatcherMockAdapter: dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp},
		failCount:             10,
	}

	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: retryAdapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:allfail"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	_ = d.HandleEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-fail",
		UserID:     "u1",
	})

	retryAdapter.mu.Lock()
	defer retryAdapter.mu.Unlock()
	if retryAdapter.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", retryAdapter.attempts)
	}
	if len(retryAdapter.responses) != 0 {
		t.Fatalf("expected 0 successful deliveries, got %d", len(retryAdapter.responses))
	}
}

func TestDispatcher_DeliveryRetryContextCancelled(t *testing.T) {
	retryAdapter := &retryCountAdapter{
		dispatcherMockAdapter: dispatcherMockAdapter{ct: models.ChannelTypeWhatsApp},
		failCount:             10,
	}

	d, reg := newDispatcher(map[models.ChannelType]channels.ChannelAdapter{
		models.ChannelTypeWhatsApp: retryAdapter,
	})
	defer d.Stop()

	sessionKey := "user:u1:channel:whatsapp:session:cancel"
	registerSession(reg, sessionKey, models.ChannelTypeWhatsApp, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = d.HandleEvent(ctx, &models.NipperEvent{
		Type:       models.EventTypeDone,
		SessionKey: sessionKey,
		ResponseID: "resp-cancel",
		UserID:     "u1",
	})

	retryAdapter.mu.Lock()
	defer retryAdapter.mu.Unlock()
	// First attempt fails, then context is cancelled so no further retries
	if retryAdapter.attempts > 2 {
		t.Fatalf("expected at most 2 attempts with cancelled context, got %d", retryAdapter.attempts)
	}
}
