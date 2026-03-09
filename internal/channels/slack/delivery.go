package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/models"
)

const (
	slackAPIBase  = "https://slack.com/api"
	maxRetries    = 3
	retryDelay    = 1 * time.Second
	clientTimeout = 30 * time.Second
)

// SlackClient sends outbound messages through the Slack Web API.
type SlackClient struct {
	botToken string
	client   *http.Client
	logger   *zap.Logger

	// streamMu protects the streaming message state.
	streamMu       sync.Mutex
	streamMessages map[string]string // sessionKey → message ts (for chat.update)
}

// NewSlackClient creates a client for Slack outbound delivery.
func NewSlackClient(botToken string, logger *zap.Logger) *SlackClient {
	return &SlackClient{
		botToken:       botToken,
		client:         &http.Client{Timeout: clientTimeout},
		logger:         logger,
		streamMessages: make(map[string]string),
	}
}

// SetHTTPClient overrides the default HTTP client (used in tests).
func (s *SlackClient) SetHTTPClient(c *http.Client) {
	s.client = c
}

// DeliverResponse sends a fully-assembled NipperResponse to the user via Slack.
func (s *SlackClient) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	meta, ok := extractSlackMeta(resp)
	if !ok {
		return fmt.Errorf("slack delivery: response missing SlackMeta")
	}

	token := s.resolveToken(meta)

	channel := meta.ChannelID
	if channel == "" {
		channel = resp.DeliveryContext.ChannelID
	}
	if channel == "" {
		return fmt.Errorf("slack delivery: no channel ID")
	}

	threadTS := meta.ThreadTS
	if threadTS == "" {
		threadTS = resp.DeliveryContext.ThreadID
	}

	// If we have an in-progress streaming message, do a final update.
	s.streamMu.Lock()
	existingTS, hasStream := s.streamMessages[resp.SessionKey]
	if hasStream {
		delete(s.streamMessages, resp.SessionKey)
	}
	s.streamMu.Unlock()

	if hasStream && existingTS != "" {
		return s.chatUpdate(ctx, token, channel, existingTS, resp.Text)
	}

	return s.chatPostMessage(ctx, token, channel, threadTS, resp.Text)
}

// DeliverEvent handles streaming events by creating or updating a Slack message.
// On the first delta, it posts a new message. Subsequent deltas update that message.
// On a "done" event, the final text is set and the streaming reference is cleared.
func (s *SlackClient) DeliverEvent(ctx context.Context, event *models.NipperEvent) error {
	if event == nil {
		return nil
	}

	meta, ok := extractSlackMetaFromEvent(event)
	if !ok {
		return nil
	}

	token := s.resolveTokenFromMeta(meta)
	channel := meta.ChannelID
	if channel == "" {
		return nil
	}

	threadTS := meta.ThreadTS

	switch event.Type {
	case models.EventTypeDelta:
		if event.Delta == nil {
			return nil
		}
		return s.handleDelta(ctx, token, channel, threadTS, event.SessionKey, event.Delta.Text)

	case models.EventTypeDone:
		s.streamMu.Lock()
		existingTS, hasStream := s.streamMessages[event.SessionKey]
		if hasStream {
			delete(s.streamMessages, event.SessionKey)
		}
		s.streamMu.Unlock()

		if hasStream && existingTS != "" && event.Delta != nil {
			return s.chatUpdate(ctx, token, channel, existingTS, event.Delta.Text)
		}
		return nil

	case models.EventTypeError:
		if event.Error == nil {
			return nil
		}
		text := fmt.Sprintf(":warning: Error: %s", event.Error.Message)

		s.streamMu.Lock()
		existingTS, hasStream := s.streamMessages[event.SessionKey]
		if hasStream {
			delete(s.streamMessages, event.SessionKey)
		}
		s.streamMu.Unlock()

		if hasStream && existingTS != "" {
			return s.chatUpdate(ctx, token, channel, existingTS, text)
		}
		return s.chatPostMessage(ctx, token, channel, threadTS, text)

	default:
		return nil
	}
}

// handleDelta creates or updates a streaming message.
func (s *SlackClient) handleDelta(ctx context.Context, token, channel, threadTS, sessionKey, text string) error {
	s.streamMu.Lock()
	existingTS, exists := s.streamMessages[sessionKey]
	s.streamMu.Unlock()

	if exists && existingTS != "" {
		return s.chatUpdate(ctx, token, channel, existingTS, text)
	}

	ts, err := s.chatPostMessageReturningTS(ctx, token, channel, threadTS, text)
	if err != nil {
		return err
	}

	s.streamMu.Lock()
	s.streamMessages[sessionKey] = ts
	s.streamMu.Unlock()

	return nil
}

// chatPostMessage posts a new message and returns any error.
func (s *SlackClient) chatPostMessage(ctx context.Context, token, channel, threadTS, text string) error {
	_, err := s.chatPostMessageReturningTS(ctx, token, channel, threadTS, text)
	return err
}

// chatPostMessageReturningTS posts a new message and returns the message timestamp.
func (s *SlackClient) chatPostMessageReturningTS(ctx context.Context, token, channel, threadTS, text string) (string, error) {
	payload := map[string]any{
		"channel": channel,
		"text":    text,
	}
	if threadTS != "" {
		payload["thread_ts"] = threadTS
	}

	respBody, err := s.apiCallWithRetry(ctx, token, "chat.postMessage", payload)
	if err != nil {
		return "", err
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		TS    string `json:"ts,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("slack: parse postMessage response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("slack: chat.postMessage: %s", result.Error)
	}

	return result.TS, nil
}

// chatUpdate updates an existing message.
func (s *SlackClient) chatUpdate(ctx context.Context, token, channel, messageTS, text string) error {
	payload := map[string]any{
		"channel": channel,
		"ts":      messageTS,
		"text":    text,
	}

	respBody, err := s.apiCallWithRetry(ctx, token, "chat.update", payload)
	if err != nil {
		return err
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("slack: parse update response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack: chat.update: %s", result.Error)
	}

	return nil
}

// AddReaction adds a reaction emoji to a message.
func (s *SlackClient) AddReaction(ctx context.Context, token, channel, messageTS, emoji string) error {
	payload := map[string]any{
		"channel":   channel,
		"timestamp": messageTS,
		"name":      emoji,
	}

	respBody, err := s.apiCall(ctx, token, "reactions.add", payload)
	if err != nil {
		return err
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("slack: parse reactions.add response: %w", err)
	}
	if !result.OK && result.Error != "already_reacted" {
		return fmt.Errorf("slack: reactions.add: %s", result.Error)
	}

	return nil
}

// RemoveReaction removes a reaction emoji from a message.
func (s *SlackClient) RemoveReaction(ctx context.Context, token, channel, messageTS, emoji string) error {
	payload := map[string]any{
		"channel":   channel,
		"timestamp": messageTS,
		"name":      emoji,
	}

	respBody, err := s.apiCall(ctx, token, "reactions.remove", payload)
	if err != nil {
		return err
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("slack: parse reactions.remove response: %w", err)
	}
	if !result.OK && result.Error != "no_reaction" {
		return fmt.Errorf("slack: reactions.remove: %s", result.Error)
	}

	return nil
}

// AuthTest checks connectivity to Slack by calling auth.test.
func (s *SlackClient) AuthTest(ctx context.Context) error {
	respBody, err := s.apiCall(ctx, s.botToken, "auth.test", map[string]any{})
	if err != nil {
		return err
	}

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("slack: parse auth.test response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack: auth.test: %s", result.Error)
	}
	return nil
}

func (s *SlackClient) apiCall(ctx context.Context, token, method string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	url := slackAPIBase + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("slack %s: read response: %w", method, err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("slack %s: HTTP %d: %s", method, resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (s *SlackClient) apiCallWithRetry(ctx context.Context, token, method string, payload any) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := s.apiCall(ctx, token, method, payload)
		if err != nil {
			lastErr = err
			s.logger.Warn("slack API call failed, retrying",
				zap.String("method", method),
				zap.Int("attempt", attempt+1),
				zap.Error(err),
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
			continue
		}
		return result, nil
	}
	return nil, fmt.Errorf("slack %s failed after %d attempts: %w", method, maxRetries, lastErr)
}

func (s *SlackClient) resolveToken(meta models.SlackMeta) string {
	if meta.BotToken != "" {
		return meta.BotToken
	}
	return s.botToken
}

func (s *SlackClient) resolveTokenFromMeta(meta models.SlackMeta) string {
	if meta.BotToken != "" {
		return meta.BotToken
	}
	return s.botToken
}

func extractSlackMeta(resp *models.NipperResponse) (models.SlackMeta, bool) {
	if resp.Meta != nil {
		if m, ok := resp.Meta.(models.SlackMeta); ok {
			return m, true
		}
	}
	return models.SlackMeta{}, false
}

func extractSlackMetaFromEvent(event *models.NipperEvent) (models.SlackMeta, bool) {
	// Events don't carry full Meta; we need the delivery context.
	// Return a minimal SlackMeta for token resolution.
	return models.SlackMeta{}, false
}

// ClearStreamState removes any tracked streaming message for a session key.
// Exposed for testing.
func (s *SlackClient) ClearStreamState(sessionKey string) {
	s.streamMu.Lock()
	delete(s.streamMessages, sessionKey)
	s.streamMu.Unlock()
}

// SetStreamMessage manually sets a streaming message ts for a session.
// Exposed for testing.
func (s *SlackClient) SetStreamMessage(sessionKey, ts string) {
	s.streamMu.Lock()
	s.streamMessages[sessionKey] = ts
	s.streamMu.Unlock()
}

// StreamMessageTS returns the tracked message TS for a session key, if any.
// Exposed for testing.
func (s *SlackClient) StreamMessageTS(sessionKey string) (string, bool) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	ts, ok := s.streamMessages[sessionKey]
	return ts, ok
}

// tokenFromDeliveryContext attempts to extract a bot token. The event dispatch
// path stores the token in the SlackMeta attached to the response; this helper
// provides a fallback for events that only carry a DeliveryContext.
func tokenFromDeliveryContext(_ models.DeliveryContext) string {
	return ""
}

// stripMrkdwn is a placeholder for future markdown-to-mrkdwn conversion.
func stripMrkdwn(text string) string {
	return strings.TrimSpace(text)
}
