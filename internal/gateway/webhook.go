package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/jescarri/open-nipper/internal/models"
)

const maxWebhookBody = 10 * 1024 * 1024 // 10 MB

// handleWebhookWhatsApp handles POST /webhook/whatsapp from Wuzapi.
func (s *Server) handleWebhookWhatsApp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		s.logger.Error("failed to read whatsapp webhook body", zap.Error(err))
		w.WriteHeader(http.StatusOK) // always 200 to Wuzapi
		return
	}
	defer r.Body.Close()

	// HMAC verification (if configured)
	hmacKey := ""
	if s.cfg != nil {
		hmacKey = s.cfg.Channels.WhatsApp.Config.WuzapiHMACKey
	}
	if hmacKey != "" {
		sig := r.Header.Get("X-Hmac-Signature")
		if !verifyWhatsAppHMAC(body, sig, hmacKey) {
			s.logger.Warn("whatsapp webhook HMAC verification failed",
				zap.String("remoteAddr", r.RemoteAddr),
			)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	// Quick-parse the event type to decide whether to route to the pipeline.
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		s.logger.Warn("whatsapp webhook: invalid JSON", zap.Error(err))
		w.WriteHeader(http.StatusOK)
		return
	}

	if envelope.Type != "Message" {
		s.logger.Debug("whatsapp webhook: non-Message event",
			zap.String("eventType", envelope.Type),
		)
		w.WriteHeader(http.StatusOK)
		return
	}

	adapter, ok := s.adapters[models.ChannelTypeWhatsApp]
	if !ok {
		s.logger.Warn("whatsapp adapter not registered")
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := s.msgRouter.HandleMessage(r.Context(), body, adapter); err != nil {
		s.logger.Error("whatsapp message pipeline error", zap.Error(err))
	}

	w.WriteHeader(http.StatusOK)
}

// handleWebhookSlack handles POST /webhook/slack from Slack Events API.
func (s *Server) handleWebhookSlack(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		s.logger.Error("failed to read slack webhook body", zap.Error(err))
		w.WriteHeader(http.StatusOK)
		return
	}
	defer r.Body.Close()

	// Slack signature verification (if configured)
	signingSecret := ""
	if s.cfg != nil {
		signingSecret = s.cfg.Channels.Slack.Config.SigningSecret
	}
	if signingSecret != "" {
		if !verifySlackSignature(body, r.Header, signingSecret) {
			s.logger.Warn("slack webhook signature verification failed",
				zap.String("remoteAddr", r.RemoteAddr),
			)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	// Check for url_verification challenge
	var envelope struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Event     *struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		s.logger.Warn("slack webhook: invalid JSON", zap.Error(err))
		w.WriteHeader(http.StatusOK)
		return
	}

	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return
	}

	// Only process event_callback with message events.
	if envelope.Type != "event_callback" || envelope.Event == nil || envelope.Event.Type != "message" {
		s.logger.Debug("slack webhook: non-message event",
			zap.String("type", envelope.Type),
		)
		w.WriteHeader(http.StatusOK)
		return
	}

	adapter, ok := s.adapters[models.ChannelTypeSlack]
	if !ok {
		s.logger.Warn("slack adapter not registered")
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := s.msgRouter.HandleMessage(r.Context(), body, adapter); err != nil {
		s.logger.Error("slack message pipeline error", zap.Error(err))
	}

	w.WriteHeader(http.StatusOK)
}

// verifyWhatsAppHMAC checks the Wuzapi HMAC-SHA256 signature.
// Expected header format: "sha256=<hex>"
func verifyWhatsAppHMAC(body []byte, signature, key string) bool {
	if signature == "" || key == "" {
		return false
	}

	// Strip prefix
	hexSig := signature
	if strings.HasPrefix(signature, "sha256=") {
		hexSig = signature[7:]
	}

	sigBytes, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	expected := mac.Sum(nil)

	return subtle.ConstantTimeCompare(sigBytes, expected) == 1
}

// verifySlackSignature validates the Slack request signature.
// See https://api.slack.com/authentication/verifying-requests-from-slack
func verifySlackSignature(body []byte, headers http.Header, signingSecret string) bool {
	timestamp := headers.Get("X-Slack-Request-Timestamp")
	signature := headers.Get("X-Slack-Signature")

	if timestamp == "" || signature == "" {
		return false
	}

	// Reject requests older than 5 minutes to prevent replay attacks.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if abs64(time.Now().Unix()-ts) > 300 {
		return false
	}

	// Compute expected signature: v0=HMAC-SHA256("v0:" + timestamp + ":" + body, signingSecret)
	baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(baseString))
	expected := fmt.Sprintf("v0=%s", hex.EncodeToString(mac.Sum(nil)))

	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
