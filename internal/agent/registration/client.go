package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"go.uber.org/zap"
)

const (
	registerPath      = "/agents/register"
	healthPath        = "/agents/health"
	agentType         = "eino-go"
	agentVersion      = "0.1.0"
	defaultTimeoutSec = 15
)

// Client calls the Gateway's POST /agents/register endpoint.
type Client struct {
	gatewayURL string
	authToken  string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewClient creates a registration client from the two required env vars.
func NewClient(gatewayURL, authToken string, logger *zap.Logger) *Client {
	return &Client{
		gatewayURL: gatewayURL,
		authToken:  authToken,
		httpClient: &http.Client{Timeout: time.Duration(defaultTimeoutSec) * time.Second},
		logger:     logger,
	}
}

// NewClientFromEnv reads NIPPER_GATEWAY_URL and NIPPER_AUTH_TOKEN from the environment.
func NewClientFromEnv(logger *zap.Logger) (*Client, error) {
	gatewayURL := os.Getenv("NIPPER_GATEWAY_URL")
	authToken := os.Getenv("NIPPER_AUTH_TOKEN")
	if gatewayURL == "" {
		return nil, fmt.Errorf("NIPPER_GATEWAY_URL environment variable is required")
	}
	if authToken == "" {
		return nil, fmt.Errorf("NIPPER_AUTH_TOKEN environment variable is required")
	}
	return NewClient(gatewayURL, authToken, logger), nil
}

// Register calls POST {gatewayURL}/agents/register and returns the registration result.
//
// Retry policy:
//   - 503 Service Unavailable: exponential backoff (1s → 30s)
//   - 429 Too Many Requests:   respect Retry-After header
//   - 401 Unauthorized:        fatal (invalid/revoked token)
//   - 403 Forbidden:           fatal (user disabled)
func (c *Client) Register(ctx context.Context) (*RegistrationResult, error) {
	hostname, _ := os.Hostname()
	body := map[string]string{
		"agent_type": agentType,
		"version":    agentVersion,
		"hostname":   hostname,
		"goarch":     runtime.GOARCH,
		"goos":       runtime.GOOS,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshalling registration body: %w", err)
	}

	initialDelay := time.Second
	maxDelay := 30 * time.Second
	delay := initialDelay

	for attempt := 1; ; attempt++ {
		result, retry, retryAfter, err := c.doRegister(ctx, bodyBytes)
		if err != nil {
			return nil, err // fatal error (401, 403, or context cancelled)
		}
		if result != nil {
			return result, nil
		}

		// Retryable — calculate wait.
		wait := delay
		if retryAfter > 0 {
			wait = retryAfter
		}
		if wait > maxDelay {
			wait = maxDelay
		}

		_ = retry
		c.logger.Warn("registration retrying",
			zap.Int("attempt", attempt),
			zap.Duration("wait", wait),
		)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}

		// Exponential backoff.
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// doRegister performs a single registration HTTP attempt.
// Returns (result, false, 0, nil) on success.
// Returns (nil, true, retryAfter, nil) on retryable errors (503, 429).
// Returns (nil, false, 0, err) on fatal errors (401, 403, network).
func (c *Client) doRegister(ctx context.Context, body []byte) (*RegistrationResult, bool, time.Duration, error) {
	url := c.gatewayURL + registerPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, 0, fmt.Errorf("building registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, true, 0, nil // treat network errors as retryable
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var regResp registrationResponse
		if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
			return nil, false, 0, fmt.Errorf("decoding registration response: %w", err)
		}
		if !regResp.OK || regResp.Result == nil {
			return nil, false, 0, fmt.Errorf("gateway returned ok=false: %s", regResp.Error)
		}
		return regResp.Result, false, 0, nil

	case http.StatusUnauthorized:
		return nil, false, 0, fmt.Errorf("registration failed: invalid or revoked token (401)")

	case http.StatusForbidden:
		return nil, false, 0, fmt.Errorf("registration failed: user disabled (403)")

	case http.StatusTooManyRequests:
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, true, retryAfter, nil

	case http.StatusServiceUnavailable:
		return nil, true, 0, nil

	default:
		c.logger.Warn("unexpected registration status",
			zap.Int("status", resp.StatusCode),
		)
		return nil, true, 0, nil
	}
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// SendHeartbeat POSTs status to the gateway's /agents/health endpoint.
// status should be "healthy", "degraded", or "unhealthy". Empty is treated as "healthy".
func (c *Client) SendHeartbeat(ctx context.Context, status string) error {
	if status == "" {
		status = "healthy"
	}
	body := map[string]string{"status": status}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling heartbeat body: %w", err)
	}
	url := c.gatewayURL + healthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("building heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat returned %d", resp.StatusCode)
	}
	return nil
}

// StartHeartbeat runs a goroutine that sends heartbeats every interval until ctx is done.
// If interval is 0 or negative, no goroutine is started.
func (c *Client) StartHeartbeat(ctx context.Context, interval time.Duration, status string) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// Send one immediately
		if err := c.SendHeartbeat(ctx, status); err != nil {
			c.logger.Debug("heartbeat failed", zap.Error(err))
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.SendHeartbeat(ctx, status); err != nil {
					c.logger.Debug("heartbeat failed", zap.Error(err))
				}
			}
		}
	}()
}
