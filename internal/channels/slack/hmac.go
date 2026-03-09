// Package slack implements the Slack channel adapter using the Slack Events API
// and Web API for bidirectional messaging with streaming support.
package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// VerifySignature validates the Slack request signature per
// https://api.slack.com/authentication/verifying-requests-from-slack.
//
// It rejects requests older than 5 minutes to prevent replay attacks.
func VerifySignature(body []byte, headers http.Header, signingSecret string) bool {
	timestamp := headers.Get("X-Slack-Request-Timestamp")
	signature := headers.Get("X-Slack-Signature")

	if timestamp == "" || signature == "" || signingSecret == "" {
		return false
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	if abs64(time.Now().Unix()-ts) > 300 {
		return false
	}

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
