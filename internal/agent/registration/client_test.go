package registration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/agent/registration"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestRegister_Success(t *testing.T) {
	want := &registration.RegistrationResult{
		AgentID:  "agt-test",
		UserID:   "user-01",
		UserName: "Test User",
		RabbitMQ: registration.RMQConfig{
			URL:      "amqp://localhost:5672",
			Username: "agent-user-01",
			Password: "secret",
			VHost:    "/nipper",
			Queues: registration.QueuesConfig{
				Agent:   "nipper-agent-user-01",
				Control: "nipper-control-user-01",
			},
			Exchanges: registration.ExchangesConfig{
				Sessions: "nipper.sessions",
				Events:   "nipper.events",
				Control:  "nipper.control",
				Logs:     "nipper.logs",
			},
			RoutingKeys: registration.RoutingKeysConfig{
				EventsPublish: "nipper.events.user-01.{sessionId}",
				LogsPublish:   "nipper.logs.user-01.{eventType}",
			},
		},
		User: registration.UserInfo{
			ID:           "user-01",
			Name:         "Test User",
			DefaultModel: "gpt-4o",
			Preferences:  map[string]any{},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": want,
		})
	}))
	defer srv.Close()

	client := registration.NewClient(srv.URL, "test-token", testLogger())
	got, err := client.Register(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AgentID != want.AgentID {
		t.Errorf("agent_id: got %q, want %q", got.AgentID, want.AgentID)
	}
	if got.RabbitMQ.Queues.Agent != want.RabbitMQ.Queues.Agent {
		t.Errorf("queue.agent: got %q, want %q", got.RabbitMQ.Queues.Agent, want.RabbitMQ.Queues.Agent)
	}
}

func TestRegister_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := registration.NewClient(srv.URL, "bad-token", testLogger())
	_, err := client.Register(context.Background())
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestRegister_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := registration.NewClient(srv.URL, "token", testLogger())
	_, err := client.Register(context.Background())
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
}

func TestRegister_RetriesOn503(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": &registration.RegistrationResult{
				AgentID: "agt-ok",
				UserID:  "user-01",
				RabbitMQ: registration.RMQConfig{
					URL: "amqp://localhost:5672",
				},
			},
		})
	}))
	defer srv.Close()

	// Use a context with a short deadline so the test doesn't hang forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := registration.NewClient(srv.URL, "token", testLogger())
	got, err := client.Register(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AgentID != "agt-ok" {
		t.Errorf("expected agent_id agt-ok, got %s", got.AgentID)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestRegister_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := registration.NewClient(srv.URL, "token", testLogger())
	_, err := client.Register(ctx)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
}
