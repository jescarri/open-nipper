package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
)

// rawRouter routes requests by matching "METHOD rawPath" against the request URI.
// Unlike http.ServeMux, it does NOT decode percent-encoded characters before matching,
// so patterns like "/api/queues/%2Fnipper" work correctly.
type rawRouter struct {
	routes map[string]http.HandlerFunc
}

func newRawRouter() *rawRouter {
	return &rawRouter{routes: make(map[string]http.HandlerFunc)}
}

func (r *rawRouter) handle(method, rawPath string, handler http.HandlerFunc) {
	r.routes[method+" "+rawPath] = handler
}

func (r *rawRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rawPath := req.URL.RawPath
	if rawPath == "" {
		rawPath = req.URL.Path
	}
	if i := strings.Index(rawPath, "?"); i >= 0 {
		rawPath = rawPath[:i]
	}
	key := req.Method + " " + rawPath
	if h, ok := r.routes[key]; ok {
		h(w, req)
		return
	}
	http.NotFound(w, req)
}

// newTestManagementClient spins up an httptest server and returns a client pointed at it.
func newTestManagementClient(t *testing.T, router http.Handler) (*HTTPManagementClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfg := &config.RMQManagementConfig{
		URL:      srv.URL,
		Username: "admin",
		Password: "secret",
	}
	client := NewHTTPManagementClient(cfg, zap.NewNop())
	return client, srv
}

// --- CreateUser ---

func TestManagement_CreateUser_SendsPUTWithCredentials(t *testing.T) {
	type userPayload struct {
		Password string `json:"password"`
		Tags     string `json:"tags"`
	}

	var gotMethod, gotPath string
	var gotPayload userPayload
	var gotAuthUser, gotAuthPass string

	router := newRawRouter()
	router.handle(http.MethodPut, "/api/users/agent-alice", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuthUser, gotAuthPass, _ = r.BasicAuth()
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusCreated)
	})

	client, _ := newTestManagementClient(t, router)

	if err := client.CreateUser(context.Background(), "agent-alice", "s3cr3t"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method: got %q, want PUT", gotMethod)
	}
	if gotPath != "/api/users/agent-alice" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotPayload.Password != "s3cr3t" {
		t.Errorf("password: got %q", gotPayload.Password)
	}
	if gotAuthUser != "admin" || gotAuthPass != "secret" {
		t.Errorf("basic auth: got %q/%q", gotAuthUser, gotAuthPass)
	}
}

func TestManagement_CreateUser_ErrorOn4xx(t *testing.T) {
	router := newRawRouter()
	router.handle(http.MethodPut, "/api/users/bad-user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	})

	client, _ := newTestManagementClient(t, router)
	err := client.CreateUser(context.Background(), "bad-user", "pass")
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
}

// --- DeleteUser ---

func TestManagement_DeleteUser(t *testing.T) {
	var gotMethod, gotPath string

	router := newRawRouter()
	router.handle(http.MethodDelete, "/api/users/agent-bob", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestManagementClient(t, router)
	if err := client.DeleteUser(context.Background(), "agent-bob"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method: got %q, want DELETE", gotMethod)
	}
	if gotPath != "/api/users/agent-bob" {
		t.Errorf("path: got %q", gotPath)
	}
}

func TestManagement_DeleteUser_NotFound(t *testing.T) {
	router := newRawRouter()
	router.handle(http.MethodDelete, "/api/users/ghost", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client, _ := newTestManagementClient(t, router)
	err := client.DeleteUser(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

// --- SetVhostPermissions ---

func TestManagement_SetVhostPermissions(t *testing.T) {
	var gotPayload VhostPermissions

	router := newRawRouter()
	router.handle(http.MethodPut, "/api/permissions/%2Fnipper/agent-carol", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusCreated)
	})

	client, _ := newTestManagementClient(t, router)
	perms := VhostPermissions{
		Configure: "^nipper-agent-user-01$",
		Write:     "^nipper\\.events$",
		Read:      "^nipper-agent-user-01$",
	}
	if err := client.SetVhostPermissions(context.Background(), "/nipper", "agent-carol", perms); err != nil {
		t.Fatalf("SetVhostPermissions: %v", err)
	}

	if gotPayload.Configure != perms.Configure {
		t.Errorf("configure: got %q, want %q", gotPayload.Configure, perms.Configure)
	}
	if gotPayload.Write != perms.Write {
		t.Errorf("write: got %q, want %q", gotPayload.Write, perms.Write)
	}
	if gotPayload.Read != perms.Read {
		t.Errorf("read: got %q, want %q", gotPayload.Read, perms.Read)
	}
}

// --- GetQueueInfo ---

func TestManagement_GetQueueInfo(t *testing.T) {
	info := QueueInfo{
		Name:                   "nipper-agent-user-01",
		Messages:               3,
		MessagesReady:          2,
		MessagesUnacknowledged: 1,
		Consumers:              1,
	}

	router := newRawRouter()
	router.handle(http.MethodGet, "/api/queues/%2Fnipper/nipper-agent-user-01", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(info)
	})

	client, _ := newTestManagementClient(t, router)
	got, err := client.GetQueueInfo(context.Background(), "/nipper", "nipper-agent-user-01")
	if err != nil {
		t.Fatalf("GetQueueInfo: %v", err)
	}

	if got.Messages != info.Messages {
		t.Errorf("Messages: got %d, want %d", got.Messages, info.Messages)
	}
	if got.Consumers != info.Consumers {
		t.Errorf("Consumers: got %d, want %d", got.Consumers, info.Consumers)
	}
	if got.MessagesReady != info.MessagesReady {
		t.Errorf("MessagesReady: got %d, want %d", got.MessagesReady, info.MessagesReady)
	}
	if got.MessagesUnacknowledged != info.MessagesUnacknowledged {
		t.Errorf("MessagesUnacknowledged: got %d, want %d", got.MessagesUnacknowledged, info.MessagesUnacknowledged)
	}
}

func TestManagement_GetQueueInfo_NotFound(t *testing.T) {
	router := newRawRouter()
	router.handle(http.MethodGet, "/api/queues/%2Fnipper/missing-queue", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client, _ := newTestManagementClient(t, router)
	_, err := client.GetQueueInfo(context.Background(), "/nipper", "missing-queue")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

// --- ListQueues ---

func TestManagement_ListQueues(t *testing.T) {
	queues := []*QueueInfo{
		{Name: "nipper-agent-user-01", Consumers: 1},
		{Name: "nipper-agent-user-02", Consumers: 0},
	}

	router := newRawRouter()
	router.handle(http.MethodGet, "/api/queues/%2Fnipper", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(queues)
	})

	client, _ := newTestManagementClient(t, router)
	got, err := client.ListQueues(context.Background(), "/nipper")
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(got))
	}
	if got[0].Name != "nipper-agent-user-01" {
		t.Errorf("queue[0].Name: got %q", got[0].Name)
	}
}

func TestManagement_ListQueues_ServerError(t *testing.T) {
	router := newRawRouter()
	router.handle(http.MethodGet, "/api/queues/%2Fnipper", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client, _ := newTestManagementClient(t, router)
	_, err := client.ListQueues(context.Background(), "/nipper")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

// --- percentEncodeVHost tests ---

func TestPercentEncodeVHost_Root(t *testing.T) {
	got := percentEncodeVHost("/")
	if got != "%2F" {
		t.Errorf("got %q, want %%2F", got)
	}
}

func TestPercentEncodeVHost_WithLeadingSlash(t *testing.T) {
	got := percentEncodeVHost("/nipper")
	want := "%2Fnipper"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPercentEncodeVHost_NoLeadingSlash(t *testing.T) {
	got := percentEncodeVHost("nipper")
	if got != "nipper" {
		t.Errorf("got %q, want %q", got, "nipper")
	}
}

// --- interface compliance ---

func TestManagement_InterfaceCompliance(t *testing.T) {
	var _ ManagementClient = (*HTTPManagementClient)(nil)
}

// TestManagement_Unreachable verifies the client returns an error when the server is down.
func TestManagement_Unreachable(t *testing.T) {
	cfg := &config.RMQManagementConfig{
		URL:      "http://127.0.0.1:1", // nothing listening here
		Username: "admin",
		Password: "pass",
	}
	client := NewHTTPManagementClient(cfg, zap.NewNop())
	err := client.CreateUser(context.Background(), "user", "pass")
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}
