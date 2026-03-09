package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/config"
)

const managementClientTimeout = 10 * time.Second

// VhostPermissions encodes RabbitMQ per-vhost permission patterns.
type VhostPermissions struct {
	Configure string `json:"configure"`
	Write     string `json:"write"`
	Read      string `json:"read"`
}

// QueueInfo holds queue statistics returned by the Management API.
type QueueInfo struct {
	Name                    string `json:"name"`
	Messages                int    `json:"messages"`
	MessagesReady           int    `json:"messages_ready"`
	MessagesUnacknowledged  int    `json:"messages_unacknowledged"`
	Consumers               int    `json:"consumers"`
}

// ManagementClient communicates with the RabbitMQ HTTP Management API.
// It is used during agent provisioning, health monitoring, and queue introspection.
type ManagementClient interface {
	// CreateUser creates or updates a RabbitMQ user with the given password.
	CreateUser(ctx context.Context, username, password string) error

	// DeleteUser deletes a RabbitMQ user.
	DeleteUser(ctx context.Context, username string) error

	// SetVhostPermissions grants permissions for username on vhost.
	SetVhostPermissions(ctx context.Context, vhost, username string, perms VhostPermissions) error

	// GetQueueInfo returns queue statistics for one queue.
	GetQueueInfo(ctx context.Context, vhost, queueName string) (*QueueInfo, error)

	// ListQueues returns all queues in a vhost.
	ListQueues(ctx context.Context, vhost string) ([]*QueueInfo, error)
}

// HTTPManagementClient implements ManagementClient against the RabbitMQ HTTP API.
type HTTPManagementClient struct {
	baseURL  string
	username string
	password string
	client   *http.Client
	logger   *zap.Logger
}

type connectionInfo struct {
	Name string `json:"name"`
	User string `json:"user"`
}

// NewHTTPManagementClient constructs an HTTPManagementClient from configuration.
func NewHTTPManagementClient(cfg *config.RMQManagementConfig, logger *zap.Logger) *HTTPManagementClient {
	return &HTTPManagementClient{
		baseURL:  cfg.URL,
		username: cfg.Username,
		password: cfg.Password,
		client:   &http.Client{Timeout: managementClientTimeout},
		logger:   logger,
	}
}

// CreateUser creates or replaces a RabbitMQ user.
// PUT /api/users/{username}
func (m *HTTPManagementClient) CreateUser(ctx context.Context, username, password string) error {
	body := struct {
		Password string `json:"password"`
		Tags     string `json:"tags"`
	}{
		Password: password,
		Tags:     "",
	}
	path := fmt.Sprintf("/api/users/%s", username)
	return m.do(ctx, http.MethodPut, path, body, nil)
}

// DeleteUser removes a RabbitMQ user.
// DELETE /api/users/{username}
func (m *HTTPManagementClient) DeleteUser(ctx context.Context, username string) error {
	path := fmt.Sprintf("/api/users/%s", username)
	return m.do(ctx, http.MethodDelete, path, nil, nil)
}

// SetVhostPermissions grants permissions for username on the given vhost.
// PUT /api/permissions/{vhost}/{username}
func (m *HTTPManagementClient) SetVhostPermissions(ctx context.Context, vhost, username string, perms VhostPermissions) error {
	path := fmt.Sprintf("/api/permissions/%s/%s", percentEncodeVHost(vhost), username)
	return m.do(ctx, http.MethodPut, path, perms, nil)
}

// GetQueueInfo returns queue statistics for a single queue.
// GET /api/queues/{vhost}/{queueName}
func (m *HTTPManagementClient) GetQueueInfo(ctx context.Context, vhost, queueName string) (*QueueInfo, error) {
	path := fmt.Sprintf("/api/queues/%s/%s", percentEncodeVHost(vhost), queueName)
	var info QueueInfo
	if err := m.do(ctx, http.MethodGet, path, nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ListQueues returns all queues in the given vhost.
// GET /api/queues/{vhost}
func (m *HTTPManagementClient) ListQueues(ctx context.Context, vhost string) ([]*QueueInfo, error) {
	path := fmt.Sprintf("/api/queues/%s", percentEncodeVHost(vhost))
	var queues []*QueueInfo
	if err := m.do(ctx, http.MethodGet, path, nil, &queues); err != nil {
		return nil, err
	}
	return queues, nil
}

// CloseUserConnections closes all active RabbitMQ connections owned by username.
// This is useful during re-registration to evict stale agent consumers that
// remain connected after credential rotation.
func (m *HTTPManagementClient) CloseUserConnections(ctx context.Context, username string) error {
	var conns []*connectionInfo
	if err := m.do(ctx, http.MethodGet, "/api/connections", nil, &conns); err != nil {
		return err
	}
	for _, c := range conns {
		if c == nil || c.User != username || c.Name == "" {
			continue
		}
		path := "/api/connections/" + url.PathEscape(c.Name)
		if err := m.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// do performs an HTTP request against the management API.
// If reqBody is non-nil, it is JSON-encoded as the request body.
// If respBody is non-nil, the response body is JSON-decoded into it.
func (m *HTTPManagementClient) do(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	url := m.baseURL + path

	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshalling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request %s %s: %w", method, url, err)
	}
	req.SetBasicAuth(m.username, m.password)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("management API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("management API %s %s: status %d: %s", method, path, resp.StatusCode, string(raw))
	}

	if respBody != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
			return fmt.Errorf("decoding management API response: %w", err)
		}
	}

	return nil
}

// percentEncodeVHost encodes a vhost string for use in URL path segments.
// "/" becomes "%2F", others are left as-is since they are valid URL path chars.
func percentEncodeVHost(vhost string) string {
	if vhost == "/" {
		return "%2F"
	}
	// Strip leading slash before encoding to match RabbitMQ's convention.
	if len(vhost) > 0 && vhost[0] == '/' {
		return "%2F" + vhost[1:]
	}
	return vhost
}
