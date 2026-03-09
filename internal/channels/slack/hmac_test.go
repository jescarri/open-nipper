package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func makeSlackHeaders(body []byte, secret string, tsOffset int64) http.Header {
	ts := time.Now().Unix() + tsOffset
	baseString := fmt.Sprintf("v0:%d:%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(baseString))
	sig := fmt.Sprintf("v0=%s", hex.EncodeToString(mac.Sum(nil)))

	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	h.Set("X-Slack-Signature", sig)
	return h
}

func TestVerifySignature_Valid(t *testing.T) {
	body := []byte(`{"type":"event_callback","event":{"type":"message"}}`)
	secret := "test-signing-secret"
	headers := makeSlackHeaders(body, secret, 0)

	if !VerifySignature(body, headers, secret) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestVerifySignature_InvalidSecret(t *testing.T) {
	body := []byte(`{"type":"event_callback"}`)
	headers := makeSlackHeaders(body, "correct-secret", 0)

	if VerifySignature(body, headers, "wrong-secret") {
		t.Fatal("expected wrong secret to fail")
	}
}

func TestVerifySignature_ReplayAttack(t *testing.T) {
	body := []byte(`{"type":"event_callback"}`)
	secret := "test-secret"
	headers := makeSlackHeaders(body, secret, -600) // 10 minutes old

	if VerifySignature(body, headers, secret) {
		t.Fatal("expected replay attack (>5min) to fail")
	}
}

func TestVerifySignature_MissingTimestamp(t *testing.T) {
	h := http.Header{}
	h.Set("X-Slack-Signature", "v0=abc123")
	if VerifySignature([]byte("body"), h, "secret") {
		t.Fatal("expected missing timestamp to fail")
	}
}

func TestVerifySignature_MissingSignature(t *testing.T) {
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	if VerifySignature([]byte("body"), h, "secret") {
		t.Fatal("expected missing signature to fail")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	body := []byte(`test`)
	headers := makeSlackHeaders(body, "secret", 0)
	if VerifySignature(body, headers, "") {
		t.Fatal("expected empty secret to fail")
	}
}

func TestVerifySignature_BadTimestamp(t *testing.T) {
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", "not-a-number")
	h.Set("X-Slack-Signature", "v0=abc")
	if VerifySignature([]byte("body"), h, "secret") {
		t.Fatal("expected non-numeric timestamp to fail")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	body := []byte(`original body`)
	secret := "test-secret"
	headers := makeSlackHeaders(body, secret, 0)

	if VerifySignature([]byte(`tampered body`), headers, secret) {
		t.Fatal("expected tampered body to fail")
	}
}
