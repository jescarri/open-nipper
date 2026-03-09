// Package whatsapp implements the WhatsApp channel adapter backed by the Wuzapi
// gateway (https://github.com/asternic/wuzapi).
package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// VerifyHMAC checks the Wuzapi HMAC-SHA256 signature against the request body.
// The expected header format is "sha256=<hex>"; the "sha256=" prefix is
// optional for tolerance.
func VerifyHMAC(body []byte, signature, key string) bool {
	if len(signature) == 0 || len(key) == 0 {
		return false
	}

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
