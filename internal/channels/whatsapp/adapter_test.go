package whatsapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	channelpkg "github.com/open-nipper/open-nipper/internal/channels"
	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
)

// Compile-time check: Adapter implements ChannelAdapter.
var _ channelpkg.ChannelAdapter = (*Adapter)(nil)

func newTestAdapter(t *testing.T, srvURL string) *Adapter {
	t.Helper()
	return NewAdapter(AdapterDeps{
		Config: config.WhatsAppConfig{
			WuzapiBaseURL:      srvURL,
			WuzapiToken:        "test-token",
			WuzapiInstanceName: "nipper-wa",
			WebhookPath:        "/webhook/whatsapp",
			Events:             []string{"Message"},
			Delivery: config.DeliveryOptions{
				MarkAsRead:    true,
				ShowTyping:    true,
				QuoteOriginal: true,
			},
		},
		Logger:     zap.NewNop(),
		GatewayURL: "http://127.0.0.1:18789",
	})
}

func TestAdapter_ChannelType(t *testing.T) {
	a := newTestAdapter(t, "http://fake")
	if a.ChannelType() != models.ChannelTypeWhatsApp {
		t.Fatalf("expected whatsapp, got %s", a.ChannelType())
	}
}

func TestAdapter_InterfaceCompliance(t *testing.T) {
	var _ channelpkg.ChannelAdapter = &Adapter{}
}

func TestAdapter_Start_RegistersWebhook(t *testing.T) {
	var webhookRegistered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhook" {
			webhookRegistered = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	a.wuzapi.SetHTTPClient(srv.Client())

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !webhookRegistered {
		t.Fatal("expected webhook registration call")
	}
}

func TestAdapter_Start_WuzapiUnreachable_WarnsButContinues(t *testing.T) {
	a := newTestAdapter(t, "http://127.0.0.1:1") // unreachable

	err := a.Start(context.Background())
	if err != nil {
		t.Fatalf("start should not error even if Wuzapi is unreachable: %v", err)
	}
}

func TestAdapter_Stop_IsNoOp(t *testing.T) {
	a := newTestAdapter(t, "http://fake")
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("stop should be a no-op: %v", err)
	}
}

func TestAdapter_HealthCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	a.wuzapi.SetHTTPClient(srv.Client())

	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected healthy: %v", err)
	}
}

func TestAdapter_HealthCheck_Degraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := newTestAdapter(t, srv.URL)
	a.wuzapi.SetHTTPClient(srv.Client())

	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected error for unhealthy Wuzapi")
	}
}

func TestAdapter_NormalizeInbound_TextMessage(t *testing.T) {
	a := newTestAdapter(t, "http://fake")

	raw := []byte(`{
		"type":"Message",
		"Info":{
			"ID":"wa-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Test"
		},
		"Message":{"Conversation":"hello adapter"},
		"userID":"1","instanceName":"nipper-wa"
	}`)

	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content.Text != "hello adapter" {
		t.Fatalf("unexpected text: %q", msg.Content.Text)
	}
}

func TestAdapter_NormalizeInbound_SelfMessageFiltered(t *testing.T) {
	a := newTestAdapter(t, "http://fake")

	raw := []byte(`{
		"type":"Message",
		"Info":{
			"ID":"wa-self",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"c@s.whatsapp.net","IsFromMe":true,"IsGroup":false}
		},
		"Message":{"Conversation":"echo"},
		"userID":"1","instanceName":"nipper-wa"
	}`)

	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("self-messages should be filtered")
	}
}

func TestAdapter_NormalizeInbound_NonMessageFiltered(t *testing.T) {
	a := newTestAdapter(t, "http://fake")

	raw := []byte(`{"type":"ReadReceipt","Info":{"ID":"wa-rr"},"Message":{},"userID":"1"}`)
	msg, err := a.NormalizeInbound(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("non-Message events should be filtered")
	}
}

func TestAdapter_DeliverEvent_NoOp(t *testing.T) {
	a := newTestAdapter(t, "http://fake")

	err := a.DeliverEvent(context.Background(), &models.NipperEvent{
		Type:       models.EventTypeDelta,
		SessionKey: "sk",
		Delta:      &models.EventDelta{Text: "streaming..."},
	})
	if err != nil {
		t.Fatalf("DeliverEvent should be a no-op for WhatsApp: %v", err)
	}
}

func TestAdapter_DeliverResponse_NilSafe(t *testing.T) {
	a := newTestAdapter(t, "http://fake")
	if err := a.DeliverResponse(context.Background(), nil); err != nil {
		t.Fatalf("nil response should be safe: %v", err)
	}
}

func TestValidate_MissingBaseURL(t *testing.T) {
	err := Validate(config.WhatsAppConfig{WuzapiToken: "tok"})
	if err == nil {
		t.Fatal("expected error for missing base URL")
	}
}

func TestValidate_MissingToken(t *testing.T) {
	err := Validate(config.WhatsAppConfig{WuzapiBaseURL: "http://localhost:8080"})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestValidate_Valid(t *testing.T) {
	err := Validate(config.WhatsAppConfig{
		WuzapiBaseURL: "http://localhost:8080",
		WuzapiToken:   "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
