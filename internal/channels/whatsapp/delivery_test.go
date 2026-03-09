package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

type requestLog struct {
	Method string
	Path   string
	Body   map[string]any
	Token  string
}

func newTestWuzapi(t *testing.T) (*WuzapiClient, *httptest.Server, *[]requestLog) {
	t.Helper()
	var mu sync.Mutex
	var logs []requestLog

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		json.Unmarshal(body, &parsed)

		mu.Lock()
		logs = append(logs, requestLog{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   parsed,
			Token:  r.Header.Get("Token"),
		})
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	client := NewWuzapiClient(srv.URL, "test-token", config.DeliveryOptions{
		MarkAsRead:    true,
		ShowTyping:    true,
		QuoteOriginal: true,
	}, config.S3DefaultConfig{}, zap.NewNop())
	client.SetHTTPClient(srv.Client())

	return client, srv, &logs
}

func testResponse() *models.NipperResponse {
	return &models.NipperResponse{
		ResponseID: "resp-001",
		SessionKey: "user:u1:channel:whatsapp:session:s1",
		UserID:     "u1",
		Text:       "Hello from the agent",
		Meta: models.WhatsAppMeta{
			ChatJID:   "1555010001@s.whatsapp.net",
			SenderJID: "1555010001@s.whatsapp.net",
			MessageID: "wa-origin-001",
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType: models.ChannelTypeWhatsApp,
			ChannelID:   "1555010001@s.whatsapp.net",
		},
	}
}

func TestDelivery_SendsTextWithTypingAndMarkRead(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	err := client.DeliverResponse(context.Background(), testResponse())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*logs) < 4 {
		t.Fatalf("expected at least 4 requests (typing, text, markread, paused), got %d", len(*logs))
	}

	// Verify sequence: composing → text → markread → paused
	if (*logs)[0].Path != "/chat/presence" {
		t.Fatalf("first request should be presence, got %s", (*logs)[0].Path)
	}
	if (*logs)[0].Body["State"] != "composing" {
		t.Fatalf("expected composing, got %v", (*logs)[0].Body["State"])
	}

	if (*logs)[1].Path != "/chat/send/text" {
		t.Fatalf("second request should be send/text, got %s", (*logs)[1].Path)
	}
	if (*logs)[1].Body["Body"] != "Hello from the agent" {
		t.Fatalf("unexpected body: %v", (*logs)[1].Body["Body"])
	}

	if (*logs)[2].Path != "/chat/markread" {
		t.Fatalf("third request should be markread, got %s", (*logs)[2].Path)
	}

	if (*logs)[3].Path != "/chat/presence" {
		t.Fatalf("fourth request should be presence, got %s", (*logs)[3].Path)
	}
	if (*logs)[3].Body["State"] != "paused" {
		t.Fatalf("expected paused, got %v", (*logs)[3].Body["State"])
	}
}

func TestDelivery_QuotesOriginalMessage(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	client.DeliverResponse(context.Background(), testResponse())

	textReq := (*logs)[1] // send/text
	ci, ok := textReq.Body["ContextInfo"]
	if !ok {
		t.Fatal("expected ContextInfo for quoting")
	}
	ciMap, ok := ci.(map[string]any)
	if !ok {
		t.Fatal("ContextInfo should be a map")
	}
	if ciMap["StanzaId"] != "wa-origin-001" {
		t.Fatalf("unexpected StanzaId: %v", ciMap["StanzaId"])
	}
}

func TestDelivery_SendsAuthToken(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	client.DeliverResponse(context.Background(), testResponse())

	for _, l := range *logs {
		if l.Token != "test-token" {
			t.Fatalf("expected Token header 'test-token', got %q for path %s", l.Token, l.Path)
		}
	}
}

func TestDelivery_NoTypingWhenDisabled(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()
	client.delivery.ShowTyping = false

	client.DeliverResponse(context.Background(), testResponse())

	for _, l := range *logs {
		if l.Path == "/chat/presence" {
			t.Fatal("should not send presence when typing is disabled")
		}
	}
}

func TestDelivery_NoMarkReadWhenDisabled(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()
	client.delivery.MarkAsRead = false

	client.DeliverResponse(context.Background(), testResponse())

	for _, l := range *logs {
		if l.Path == "/chat/markread" {
			t.Fatal("should not markread when disabled")
		}
	}
}

func TestDelivery_NoQuoteWhenDisabled(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()
	client.delivery.QuoteOriginal = false

	client.DeliverResponse(context.Background(), testResponse())

	textReq := findRequest(*logs, "/chat/send/text")
	if textReq == nil {
		t.Fatal("expected text request")
	}
	if _, ok := textReq.Body["ContextInfo"]; ok {
		t.Fatal("should not include ContextInfo when quoting is disabled")
	}
}

func TestDelivery_MissingMeta(t *testing.T) {
	client, srv, _ := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Meta = nil
	err := client.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error when meta is missing")
	}
}

func TestDelivery_LinkPreviewEnabledWhenBodyHasURL(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Text = "Check this map: https://www.google.com/maps?q=18.94,-103.89"

	client.DeliverResponse(context.Background(), resp)

	textReq := findRequest(*logs, "/chat/send/text")
	if textReq == nil {
		t.Fatal("expected text send request")
	}
	if textReq.Body["LinkPreview"] != true {
		t.Errorf("expected LinkPreview=true when body contains URL, got %v", textReq.Body["LinkPreview"])
	}
}

func TestDelivery_ImagePart(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Text = ""
	resp.Parts = []models.ContentPart{
		{
			Type:    "image",
			URL:     "https://s3.example.com/image.jpg",
			Caption: "Screenshot",
		},
	}

	client.DeliverResponse(context.Background(), resp)

	imgReq := findRequest(*logs, "/chat/send/image")
	if imgReq == nil {
		t.Fatal("expected image send request")
	}
	if imgReq.Body["Image"] != "https://s3.example.com/image.jpg" {
		t.Fatalf("unexpected Image URL: %v", imgReq.Body["Image"])
	}
	if imgReq.Body["Caption"] != "Screenshot" {
		t.Fatalf("unexpected Caption: %v", imgReq.Body["Caption"])
	}
}

func TestDelivery_DocumentPart(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Text = ""
	resp.Parts = []models.ContentPart{
		{
			Type:    "document",
			URL:     "https://s3.example.com/report.pdf",
			Caption: "Report",
		},
	}

	client.DeliverResponse(context.Background(), resp)

	docReq := findRequest(*logs, "/chat/send/document")
	if docReq == nil {
		t.Fatal("expected document send request")
	}
}

func TestDelivery_DocumentWithImageMIMERoutesToImage(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Text = ""
	resp.Parts = []models.ContentPart{
		{
			Type:     "document",
			MimeType: "image/jpeg",
			URL:      "https://s3.example.com/cyclist.jpg",
			Caption:  "**Unsafe lane** at this point",
		},
	}

	client.DeliverResponse(context.Background(), resp)

	imgReq := findRequest(*logs, "/chat/send/image")
	if imgReq == nil {
		t.Fatal("expected image send request for image/* document")
	}
	if imgReq.Body["Image"] != "https://s3.example.com/cyclist.jpg" {
		t.Fatalf("unexpected Image URL: %v", imgReq.Body["Image"])
	}
	if imgReq.Body["Caption"] != "*Unsafe lane* at this point" {
		t.Fatalf("unexpected Caption: %v", imgReq.Body["Caption"])
	}

	docReq := findRequest(*logs, "/chat/send/document")
	if docReq != nil {
		t.Fatal("did not expect document send request for image/* document")
	}
}

func TestDelivery_UsesSenderForLIDChat(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	resp := testResponse()
	resp.Meta = models.WhatsAppMeta{
		ChatJID:   "1555010004@lid",
		SenderJID: "1555010003@s.whatsapp.net",
		MessageID: "wa-origin-lid-001",
	}

	if err := client.DeliverResponse(context.Background(), resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	textReq := findRequest(*logs, "/chat/send/text")
	if textReq == nil {
		t.Fatal("expected text request")
	}
	if textReq.Body["Phone"] != "1555010003" {
		t.Fatalf("expected Phone to use SenderJID, got %v", textReq.Body["Phone"])
	}
}

func TestDelivery_SendInboundFeedback_UsesSenderForLIDChat(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	meta := models.WhatsAppMeta{
		ChatJID:   "1555010004@lid",
		SenderJID: "1555010003@s.whatsapp.net",
		MessageID: "wa-origin-lid-002",
	}

	if err := client.SendInboundFeedback(context.Background(), meta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	presenceReq := findRequest(*logs, "/chat/presence")
	if presenceReq == nil {
		t.Fatal("expected presence request")
	}
	if presenceReq.Body["Phone"] != "1555010003" {
		t.Fatalf("expected typing Phone to use SenderJID, got %v", presenceReq.Body["Phone"])
	}

	readReq := findRequest(*logs, "/chat/markread")
	if readReq == nil {
		t.Fatal("expected markread request")
	}
	if readReq.Body["ChatPhone"] != "1555010003" {
		t.Fatalf("expected ChatPhone to use SenderJID, got %v", readReq.Body["ChatPhone"])
	}
}

func TestDelivery_RetryOnServerError(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		a := attempts
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/chat/send/text") && a <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewWuzapiClient(srv.URL, "token", config.DeliveryOptions{}, config.S3DefaultConfig{}, zap.NewNop())
	client.SetHTTPClient(srv.Client())

	resp := testResponse()
	err := client.DeliverResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
}

func TestDelivery_RegisterWebhook(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	err := client.RegisterWebhook(context.Background(), "http://gateway:18789/webhook/whatsapp", []string{"Message", "ReadReceipt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*logs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*logs))
	}
	if (*logs)[0].Path != "/webhook" {
		t.Fatalf("expected /webhook, got %s", (*logs)[0].Path)
	}
	if (*logs)[0].Body["webhookURL"] != "http://gateway:18789/webhook/whatsapp" {
		t.Fatalf("unexpected webhookURL: %v", (*logs)[0].Body["webhookURL"])
	}
}

func TestDelivery_ConfigureHMAC(t *testing.T) {
	client, srv, logs := newTestWuzapi(t)
	defer srv.Close()

	err := client.ConfigureHMAC(context.Background(), "a]32-char-minimum-hmac-key-12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*logs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*logs))
	}
	if (*logs)[0].Path != "/session/hmac/config" {
		t.Fatalf("expected /session/hmac/config, got %s", (*logs)[0].Path)
	}
	if (*logs)[0].Body["hmac_key"] != "a]32-char-minimum-hmac-key-12345" {
		t.Fatalf("unexpected hmac_key: %v", (*logs)[0].Body["hmac_key"])
	}
}

func TestDelivery_HealthCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/status" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewWuzapiClient(srv.URL, "token", config.DeliveryOptions{}, config.S3DefaultConfig{}, zap.NewNop())
	client.SetHTTPClient(srv.Client())

	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected healthy: %v", err)
	}
}

func TestDelivery_HealthCheck_Fail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewWuzapiClient(srv.URL, "token", config.DeliveryOptions{}, config.S3DefaultConfig{}, zap.NewNop())
	client.SetHTTPClient(srv.Client())

	if err := client.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPhoneFromJID(t *testing.T) {
	tests := []struct {
		jid, want string
	}{
		{"1555010001@s.whatsapp.net", "1555010001"},
		{"12025550100@g.us", "12025550100"},
		{"nojid", "nojid"},
		{"", ""},
	}
	for _, tt := range tests {
		got := phoneFromJID(tt.jid)
		if got != tt.want {
			t.Errorf("phoneFromJID(%q) = %q, want %q", tt.jid, got, tt.want)
		}
	}
}

func TestResolveImagePayload_UsesURLForNonS3(t *testing.T) {
	client := NewWuzapiClient(
		"http://wuzapi.local",
		"token",
		config.DeliveryOptions{},
		config.S3DefaultConfig{},
		zap.NewNop(),
	)

	got, err := client.resolveImagePayload(context.Background(), models.ContentPart{
		Type:     "image",
		MimeType: "image/jpeg",
		URL:      "https://example.com/public.jpg",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://example.com/public.jpg" {
		t.Fatalf("expected original URL, got %q", got)
	}
}

func TestResolveImagePayload_S3URLWithoutCredentialsReturnsError(t *testing.T) {
	client := NewWuzapiClient(
		"http://wuzapi.local",
		"token",
		config.DeliveryOptions{},
		config.S3DefaultConfig{},
		zap.NewNop(),
	)

	_, err := client.resolveImagePayload(context.Background(), models.ContentPart{
		Type:     "image",
		MimeType: "image/jpeg",
		URL:      "s3://bucket/private.jpg",
	})
	if err == nil {
		t.Fatal("expected error for s3 image URL without S3 credentials")
	}
}

func findRequest(logs []requestLog, path string) *requestLog {
	for i := range logs {
		if logs[i].Path == path {
			return &logs[i]
		}
	}
	return nil
}
