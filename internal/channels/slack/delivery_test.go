package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/models"
)

func newTestLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func slackResponse(ok bool, ts string, errMsg string) []byte {
	r := map[string]any{"ok": ok}
	if ts != "" {
		r["ts"] = ts
	}
	if errMsg != "" {
		r["error"] = errMsg
	}
	b, _ := json.Marshal(r)
	return b
}

func TestDeliverResponse_PostMessage(t *testing.T) {
	var mu sync.Mutex
	var capturedMethod string
	var capturedPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		capturedMethod = parts[len(parts)-1]

		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		capturedPayload = payload

		_, _ = w.Write(slackResponse(true, "1234.5678", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.SetHTTPClient(srv.Client())

	// Override the API base by wrapping the client
	origAPICall := client.apiCall
	_ = origAPICall
	// For testing, we replace the base URL by adjusting the httptest server
	// We need to override slackAPIBase... instead, let's create a custom transport
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	resp := &models.NipperResponse{
		SessionKey: "test-session",
		Text:       "Hello from Nipper!",
		Meta: models.SlackMeta{
			ChannelID: "C123",
			ThreadTS:  "111.222",
			BotToken:  "xoxb-test",
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType: models.ChannelTypeSlack,
			ChannelID:   "C123",
		},
	}

	err := client.DeliverResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if capturedMethod != "chat.postMessage" {
		t.Errorf("method = %q, want %q", capturedMethod, "chat.postMessage")
	}
	if capturedPayload["channel"] != "C123" {
		t.Errorf("channel = %v, want %q", capturedPayload["channel"], "C123")
	}
	if capturedPayload["text"] != "Hello from Nipper!" {
		t.Errorf("text = %v, want %q", capturedPayload["text"], "Hello from Nipper!")
	}
	if capturedPayload["thread_ts"] != "111.222" {
		t.Errorf("thread_ts = %v, want %q", capturedPayload["thread_ts"], "111.222")
	}
}

func TestDeliverResponse_UpdateExistingStreamMessage(t *testing.T) {
	var mu sync.Mutex
	var capturedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		capturedMethod = parts[len(parts)-1]
		_, _ = w.Write(slackResponse(true, "1234.5678", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	client.SetStreamMessage("sess-1", "existing.ts.123")

	resp := &models.NipperResponse{
		SessionKey: "sess-1",
		Text:       "Final response",
		Meta: models.SlackMeta{
			ChannelID: "C123",
			BotToken:  "xoxb-test",
		},
		DeliveryContext: models.DeliveryContext{
			ChannelType: models.ChannelTypeSlack,
			ChannelID:   "C123",
		},
	}

	err := client.DeliverResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if capturedMethod != "chat.update" {
		t.Errorf("method = %q, want %q", capturedMethod, "chat.update")
	}

	if _, exists := client.StreamMessageTS("sess-1"); exists {
		t.Error("stream message should be cleared after DeliverResponse")
	}
}

func TestDeliverResponse_MissingMeta(t *testing.T) {
	client := NewSlackClient("xoxb-test", newTestLogger())
	resp := &models.NipperResponse{
		Text: "hello",
	}
	err := client.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for missing SlackMeta")
	}
}

func TestDeliverResponse_MissingChannel(t *testing.T) {
	client := NewSlackClient("xoxb-test", newTestLogger())
	resp := &models.NipperResponse{
		Text: "hello",
		Meta: models.SlackMeta{},
		DeliveryContext: models.DeliveryContext{
			ChannelType: models.ChannelTypeSlack,
		},
	}
	err := client.DeliverResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for missing channel ID")
	}
}

func TestDeliverEvent_Delta_CreatesNewMessage(t *testing.T) {
	var mu sync.Mutex
	var capturedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		capturedMethod = parts[len(parts)-1]
		_, _ = w.Write(slackResponse(true, "new.msg.ts", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	// DeliverEvent needs SlackMeta in the event. Since events don't carry
	// full meta in our model, we test the handleDelta path directly.
	err := client.handleDelta(context.Background(), "xoxb-test", "C001", "thread.ts", "sess-delta", "Hello...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	if capturedMethod != "chat.postMessage" {
		t.Errorf("method = %q, want %q", capturedMethod, "chat.postMessage")
	}
	mu.Unlock()

	ts, ok := client.StreamMessageTS("sess-delta")
	if !ok {
		t.Fatal("expected stream message to be tracked")
	}
	if ts != "new.msg.ts" {
		t.Errorf("tracked ts = %q, want %q", ts, "new.msg.ts")
	}
}

func TestDeliverEvent_Delta_UpdatesExisting(t *testing.T) {
	var mu sync.Mutex
	var capturedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		capturedMethod = parts[len(parts)-1]
		_, _ = w.Write(slackResponse(true, "", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	client.SetStreamMessage("sess-update", "existing.ts.456")

	err := client.handleDelta(context.Background(), "xoxb-test", "C001", "", "sess-update", "Updated text...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	if capturedMethod != "chat.update" {
		t.Errorf("method = %q, want %q", capturedMethod, "chat.update")
	}
	mu.Unlock()
}

func TestDeliverEvent_NilEvent(t *testing.T) {
	client := NewSlackClient("xoxb-test", newTestLogger())
	err := client.DeliverEvent(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthTest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(slackResponse(true, "", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	err := client.AuthTest(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthTest_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(slackResponse(false, "", "invalid_auth"))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-bad", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	err := client.AuthTest(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid auth")
	}
}

func TestAddReaction(t *testing.T) {
	var capturedPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		_, _ = w.Write(slackResponse(true, "", ""))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	err := client.AddReaction(context.Background(), "xoxb-test", "C001", "111.222", "eyes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPayload["name"] != "eyes" {
		t.Errorf("emoji = %v, want %q", capturedPayload["name"], "eyes")
	}
}

func TestAddReaction_AlreadyReacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(slackResponse(false, "", "already_reacted"))
	}))
	defer srv.Close()

	client := NewSlackClient("xoxb-test", newTestLogger())
	client.client = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
		Timeout:   5 * time.Second,
	}

	err := client.AddReaction(context.Background(), "xoxb-test", "C001", "111.222", "eyes")
	if err != nil {
		t.Fatalf("already_reacted should not be treated as error: %v", err)
	}
}

func TestClearStreamState(t *testing.T) {
	client := NewSlackClient("xoxb-test", newTestLogger())
	client.SetStreamMessage("sess-1", "ts.123")

	client.ClearStreamState("sess-1")

	if _, ok := client.StreamMessageTS("sess-1"); ok {
		t.Error("expected stream state to be cleared")
	}
}

// rewriteTransport rewrites Slack API requests to the test server.
type rewriteTransport struct {
	base string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.base, "http://")
	return http.DefaultTransport.RoundTrip(req)
}
