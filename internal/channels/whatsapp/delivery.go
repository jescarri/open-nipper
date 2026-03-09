package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/formatting"
	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/internal/s3fetch"
)

const (
	maxRetries    = 3
	retryDelay    = 1 * time.Second
	clientTimeout = 30 * time.Second
)

// WuzapiClient sends outbound messages through the Wuzapi REST API.
type WuzapiClient struct {
	baseURL    string
	token      string
	delivery   config.DeliveryOptions
	s3Fetcher  *s3fetch.Fetcher
	client     *http.Client
	logger     *zap.Logger
}

// NewWuzapiClient creates a client for Wuzapi outbound delivery.
func NewWuzapiClient(baseURL, token string, delivery config.DeliveryOptions, s3Cfg config.S3DefaultConfig, logger *zap.Logger) *WuzapiClient {
	return &WuzapiClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		delivery:  delivery,
		s3Fetcher: s3fetch.NewFetcher(s3Cfg, s3fetch.WithMaxBytes(maxOutboundImageBytes)),
		client:    &http.Client{Timeout: clientTimeout},
		logger:    logger,
	}
}

// SetHTTPClient overrides the default HTTP client (used in tests).
func (w *WuzapiClient) SetHTTPClient(c *http.Client) {
	w.client = c
}

// DeliverResponse sends a fully-assembled NipperResponse to the user via Wuzapi.
// The sequence is: typing indicator → send message → mark as read → clear typing.
func (w *WuzapiClient) DeliverResponse(ctx context.Context, resp *models.NipperResponse) error {
	meta, ok := extractWAMeta(resp)
	if !ok {
		return fmt.Errorf("whatsapp delivery: response missing WhatsAppMeta")
	}

	targetJID := resolveTargetJID(meta)
	phone := phoneFromJID(targetJID)
	if phone == "" {
		return fmt.Errorf("whatsapp delivery: could not extract phone from target JID %q", targetJID)
	}
	w.logger.Debug("resolved whatsapp outbound target",
		zap.String("responseId", resp.ResponseID),
		zap.String("sessionKey", resp.SessionKey),
		zap.String("chatJID", meta.ChatJID),
		zap.String("senderJID", meta.SenderJID),
		zap.String("targetJID", targetJID),
		zap.String("phone", phone),
	)

	if w.delivery.ShowTyping {
		_ = w.setPresence(ctx, phone, "composing")
	}

	var deliveryErr error
	if resp.Text != "" {
		deliveryErr = w.sendText(ctx, phone, resp.Text, meta)
	}
	for _, part := range resp.Parts {
		switch part.Type {
		case "image":
			if part.Caption != "" {
				part.Caption = formatting.WhatsApp(part.Caption)
			}
			if err := w.sendImage(ctx, phone, part); err != nil && deliveryErr == nil {
				deliveryErr = err
			}
		case "document":
			if part.Caption != "" {
				part.Caption = formatting.WhatsApp(part.Caption)
			}
			// Some WhatsApp clients upload photos as "document" with image MIME.
			// Send these back as image for proper inline rendering.
			if isImageMIME(part.MimeType) {
				if err := w.sendImage(ctx, phone, part); err != nil && deliveryErr == nil {
					deliveryErr = err
				}
			} else {
				if err := w.sendDocument(ctx, phone, part); err != nil && deliveryErr == nil {
					deliveryErr = err
				}
			}
		}
	}

	if w.delivery.MarkAsRead && meta.MessageID != "" {
		_ = w.markRead(ctx, meta.MessageID, targetJID)
	}

	if w.delivery.ShowTyping {
		_ = w.setPresence(ctx, phone, "paused")
	}

	return deliveryErr
}

// SendInboundFeedback sends immediate user-visible feedback for accepted inbound
// messages (mark-read + typing), which is useful when queue mode uses debounce.
func (w *WuzapiClient) SendInboundFeedback(ctx context.Context, meta models.WhatsAppMeta) error {
	targetJID := resolveTargetJID(meta)
	phone := phoneFromJID(targetJID)
	if phone == "" {
		return fmt.Errorf("whatsapp delivery: could not extract phone from target JID %q", targetJID)
	}

	if w.delivery.MarkAsRead && meta.MessageID != "" {
		_ = w.markRead(ctx, meta.MessageID, targetJID)
	}
	if meta.MessageID != "" {
		emojis := []string{"🔥", "👌", "⭐"}
		emoji := emojis[time.Now().UnixNano()%int64(len(emojis))]
		_ = w.sendReaction(ctx, phone, meta.MessageID, emoji)
	}
	if w.delivery.ShowTyping {
		_ = w.setPresence(ctx, phone, "composing")
	}

	w.logger.Debug("sent whatsapp inbound feedback",
		zap.String("messageId", meta.MessageID),
		zap.String("chatJID", meta.ChatJID),
		zap.String("senderJID", meta.SenderJID),
		zap.String("targetJID", targetJID),
		zap.String("phone", phone),
	)
	return nil
}

// sendText sends a text message, optionally quoting the original.
// LinkPreview is enabled when the body contains http(s) URLs (per Wuzapi API).
func (w *WuzapiClient) sendText(ctx context.Context, phone, text string, meta models.WhatsAppMeta) error {
	payload := map[string]any{
		"Phone": phone,
		"Body":  text,
	}
	if w.delivery.QuoteOriginal && meta.MessageID != "" {
		payload["ContextInfo"] = map[string]string{
			"StanzaId":    meta.MessageID,
			"Participant": meta.SenderJID,
		}
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		payload["LinkPreview"] = true
	}
	return w.postWithRetry(ctx, "/chat/send/text", payload)
}

// sendImage sends an image message with an optional caption.
func (w *WuzapiClient) sendImage(ctx context.Context, phone string, part models.ContentPart) error {
	imagePayload, err := w.resolveImagePayload(ctx, part)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"Phone": phone,
	}
	if imagePayload != "" {
		payload["Image"] = imagePayload
	}
	if part.Caption != "" {
		payload["Caption"] = part.Caption
	}
	return w.postWithRetry(ctx, "/chat/send/image", payload)
}

const maxOutboundImageBytes = 20 * 1024 * 1024 // 20MB max media payload

func (w *WuzapiClient) resolveImagePayload(ctx context.Context, part models.ContentPart) (string, error) {
	if part.URL == "" {
		return "", nil
	}

	if !strings.HasPrefix(part.URL, "s3://") && !w.s3Fetcher.IsS3EndpointURL(part.URL) {
		return part.URL, nil
	}

	data, err := w.s3Fetcher.Fetch(ctx, part.URL)
	if err != nil {
		return "", fmt.Errorf("whatsapp delivery: fetch image bytes from S3 URL %q: %w", part.URL, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("whatsapp delivery: empty image bytes from %q", part.URL)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// sendDocument sends a document message.
func (w *WuzapiClient) sendDocument(ctx context.Context, phone string, part models.ContentPart) error {
	payload := map[string]any{
		"Phone": phone,
	}
	if part.URL != "" {
		payload["Document"] = part.URL
	}
	if part.Caption != "" {
		payload["Caption"] = part.Caption
	}
	return w.postWithRetry(ctx, "/chat/send/document", payload)
}

// sendReaction adds an emoji reaction to a message.
func (w *WuzapiClient) sendReaction(ctx context.Context, phone, messageID, emoji string) error {
	return w.post(ctx, "/chat/react", map[string]any{
		"Phone": phone,
		"Body":  emoji,
		"Id":    messageID,
	})
}

// setPresence sets the typing indicator state.
func (w *WuzapiClient) setPresence(ctx context.Context, phone, state string) error {
	return w.post(ctx, "/chat/presence", map[string]any{
		"Phone": phone,
		"State": state,
	})
}

// markRead marks a message as read.
func (w *WuzapiClient) markRead(ctx context.Context, messageID, targetJID string) error {
	phone := phoneFromJID(targetJID)
	return w.post(ctx, "/chat/markread", map[string]any{
		"Id":        []string{messageID},
		"ChatPhone": phone,
	})
}

// RegisterWebhook configures Wuzapi to POST events to the specified URL.
func (w *WuzapiClient) RegisterWebhook(ctx context.Context, webhookURL string, events []string) error {
	return w.post(ctx, "/webhook", map[string]any{
		"webhookURL": webhookURL,
		"events":     events,
	})
}

// ConfigureHMAC sets the HMAC signing key on Wuzapi so that webhooks are
// signed with the same key the gateway uses for verification.
func (w *WuzapiClient) ConfigureHMAC(ctx context.Context, hmacKey string) error {
	return w.post(ctx, "/session/hmac/config", map[string]string{
		"hmac_key": hmacKey,
	})
}

// HealthCheck pings Wuzapi to verify connectivity.
func (w *WuzapiClient) HealthCheck(ctx context.Context) error {
	url := w.baseURL + "/session/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Token", w.token)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("wuzapi health check: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("wuzapi health check: status %d", resp.StatusCode)
	}
	return nil
}

func (w *WuzapiClient) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := w.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", w.token)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("POST %s: status %d", path, resp.StatusCode)
	}
	return nil
}

func (w *WuzapiClient) postWithRetry(ctx context.Context, path string, payload any) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := w.post(ctx, path, payload); err != nil {
			lastErr = err
			w.logger.Warn("wuzapi request failed, retrying",
				zap.String("path", path),
				zap.Int("attempt", attempt+1),
				zap.Error(err),
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("wuzapi POST %s failed after %d attempts: %w", path, maxRetries, lastErr)
}

func extractWAMeta(resp *models.NipperResponse) (models.WhatsAppMeta, bool) {
	if resp.Meta != nil {
		if m, ok := resp.Meta.(models.WhatsAppMeta); ok {
			return m, true
		}
	}
	return models.WhatsAppMeta{}, false
}

// phoneFromJID extracts the phone number portion from a WhatsApp JID.
// "5491155553935@s.whatsapp.net" → "5491155553935"
// "120362123456@g.us" → "120362123456"
func phoneFromJID(jid string) string {
	at := strings.Index(jid, "@")
	if at <= 0 {
		return jid
	}
	return jid[:at]
}

// resolveTargetJID chooses the destination JID for outbound delivery.
// For direct chats where ChatJID is @lid, it falls back to SenderJID.
func resolveTargetJID(meta models.WhatsAppMeta) string {
	chat := normalizeJID(meta.ChatJID)
	sender := normalizeJID(meta.SenderJID)

	if strings.HasSuffix(chat, "@g.us") {
		return chat
	}
	if strings.HasSuffix(chat, "@s.whatsapp.net") {
		return chat
	}
	if sender != "" {
		return sender
	}
	return chat
}

func isImageMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/")
}
