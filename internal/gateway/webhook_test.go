package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	channelpkg "github.com/open-nipper/open-nipper/internal/channels"
	"github.com/open-nipper/open-nipper/internal/models"
)

func TestVerifyWhatsAppHMAC_Valid(t *testing.T) {
	key := "test-hmac-key"
	body := []byte(`{"type":"Message","data":"hello"}`)

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyWhatsAppHMAC(body, sig, key) {
		t.Fatal("valid HMAC should pass")
	}
}

func TestVerifyWhatsAppHMAC_Invalid(t *testing.T) {
	body := []byte(`{"type":"Message"}`)
	if verifyWhatsAppHMAC(body, "sha256=deadbeef00", "secret") {
		t.Fatal("invalid HMAC should fail")
	}
}

func TestVerifyWhatsAppHMAC_EmptySignature(t *testing.T) {
	if verifyWhatsAppHMAC([]byte("body"), "", "key") {
		t.Fatal("empty signature should fail")
	}
}

func TestVerifyWhatsAppHMAC_EmptyKey(t *testing.T) {
	if verifyWhatsAppHMAC([]byte("body"), "sha256=abc", "") {
		t.Fatal("empty key should fail")
	}
}

func TestVerifyWhatsAppHMAC_MalformedHex(t *testing.T) {
	if verifyWhatsAppHMAC([]byte("body"), "sha256=not_hex!", "key") {
		t.Fatal("malformed hex should fail")
	}
}

func TestVerifyWhatsAppHMAC_NoPrefixStillWorks(t *testing.T) {
	key := "my-secret"
	body := []byte(`payload`)

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !verifyWhatsAppHMAC(body, sig, key) {
		t.Fatal("signature without prefix should still work")
	}
}

func TestVerifySlackSignature_Valid(t *testing.T) {
	secret := "test-slack-secret"
	body := []byte(`{"type":"event_callback"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())

	baseString := fmt.Sprintf("v0:%s:%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", ts)
	headers.Set("X-Slack-Signature", sig)

	if !verifySlackSignature(body, headers, secret) {
		t.Fatal("valid Slack signature should pass")
	}
}

func TestVerifySlackSignature_ReplayAttack(t *testing.T) {
	secret := "secret"
	body := []byte(`data`)
	ts := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())

	baseString := fmt.Sprintf("v0:%s:%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", ts)
	headers.Set("X-Slack-Signature", sig)

	if verifySlackSignature(body, headers, secret) {
		t.Fatal("old timestamp (>5min) should be rejected")
	}
}

func TestVerifySlackSignature_MissingHeaders(t *testing.T) {
	headers := http.Header{}
	if verifySlackSignature([]byte("data"), headers, "secret") {
		t.Fatal("missing headers should fail")
	}

	headers.Set("X-Slack-Request-Timestamp", "12345")
	if verifySlackSignature([]byte("data"), headers, "secret") {
		t.Fatal("missing signature header should fail")
	}
}

func TestVerifySlackSignature_BadTimestamp(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Slack-Request-Timestamp", "not-a-number")
	headers.Set("X-Slack-Signature", "v0=abc")
	if verifySlackSignature([]byte("data"), headers, "secret") {
		t.Fatal("non-numeric timestamp should fail")
	}
}

// --- Integration tests using httptest ---

func newTestServer(adapters map[string]*mockAdapter, guardAllowed bool) *Server {
	adapterMap := make(map[models.ChannelType]channelpkg.ChannelAdapter)
	for k, v := range adapters {
		adapterMap[models.ChannelType(k)] = v
	}

	pub := &mockPublisher{}
	guard := &mockGuard{allowed: guardAllowed}
	repo := &mockRepo{resolvedUserID: "user-01"}
	logger := zap.NewNop()
	dedup := NewDeduplicator(30 * time.Second)

	msgRouter := NewRouter(RouterDeps{
		Logger:    logger,
		Repo:      repo,
		Guard:     guard,
		Resolver:  NewResolver(logger, "test-model"),
		Registry:  NewRegistry(),
		Publisher: pub,
		Dedup:     dedup,
		Config:    nil,
	})

	return NewServer(ServerDeps{
		Logger:     logger,
		Config:     nil,
		MsgRouter:  msgRouter,
		Dispatcher: nil,
		Adapters:   adapterMap,
	})
}

func TestServer_HealthEndpoint(t *testing.T) {
	s := newTestServer(nil, true)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("expected ok:true in body, got %s", w.Body.String())
	}
}

func TestServer_WhatsAppWebhookReturns200OnMessage(t *testing.T) {
	msg := newWhatsAppMsg()
	waAdapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: msg}
	s := newTestServer(map[string]*mockAdapter{"whatsapp": waAdapter}, true)

	body := `{"type":"Message","Info":{"ID":"wa-123"}}`
	req := httptest.NewRequest("POST", "/webhook/whatsapp", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestServer_WhatsAppWebhookIgnoresNonMessage(t *testing.T) {
	waAdapter := &mockAdapter{ct: models.ChannelTypeWhatsApp, msg: nil}
	s := newTestServer(map[string]*mockAdapter{"whatsapp": waAdapter}, true)

	body := `{"type":"ReadReceipt","data":{}}`
	req := httptest.NewRequest("POST", "/webhook/whatsapp", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestServer_WhatsAppWebhookBadJSON(t *testing.T) {
	waAdapter := &mockAdapter{ct: models.ChannelTypeWhatsApp}
	s := newTestServer(map[string]*mockAdapter{"whatsapp": waAdapter}, true)

	body := `not valid json`
	req := httptest.NewRequest("POST", "/webhook/whatsapp", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even for bad JSON, got %d", w.Code)
	}
}

func TestServer_SlackURLVerification(t *testing.T) {
	slackAdapter := &mockAdapter{ct: models.ChannelTypeSlack}
	s := newTestServer(map[string]*mockAdapter{"slack": slackAdapter}, true)

	body := `{"type":"url_verification","challenge":"test-challenge-abc"}`
	req := httptest.NewRequest("POST", "/webhook/slack", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "test-challenge-abc") {
		t.Fatalf("expected challenge in response, got %s", w.Body.String())
	}
}

func TestServer_SlackWebhookMessageEvent(t *testing.T) {
	msg := newWhatsAppMsg()
	msg.ChannelType = models.ChannelTypeSlack
	slackAdapter := &mockAdapter{ct: models.ChannelTypeSlack, msg: msg}
	s := newTestServer(map[string]*mockAdapter{"slack": slackAdapter}, true)

	body := `{"type":"event_callback","event":{"type":"message","text":"hello","user":"U123","channel":"C456"}}`
	req := httptest.NewRequest("POST", "/webhook/slack", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestServer_SlackWebhookIgnoresNonMessage(t *testing.T) {
	slackAdapter := &mockAdapter{ct: models.ChannelTypeSlack}
	s := newTestServer(map[string]*mockAdapter{"slack": slackAdapter}, true)

	body := `{"type":"event_callback","event":{"type":"reaction_added"}}`
	req := httptest.NewRequest("POST", "/webhook/slack", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestServer_404OnUnknownRoute(t *testing.T) {
	s := newTestServer(nil, true)
	req := httptest.NewRequest("GET", "/unknown", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
