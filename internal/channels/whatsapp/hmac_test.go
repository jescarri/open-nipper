package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func computeHMAC(body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMAC_ValidWithPrefix(t *testing.T) {
	key := "test-hmac-key"
	body := []byte(`{"type":"Message","data":"hello"}`)
	sig := "sha256=" + computeHMAC(body, key)

	if !VerifyHMAC(body, sig, key) {
		t.Fatal("valid HMAC with sha256= prefix should pass")
	}
}

func TestVerifyHMAC_ValidWithoutPrefix(t *testing.T) {
	key := "my-secret"
	body := []byte(`payload`)
	sig := computeHMAC(body, key)

	if !VerifyHMAC(body, sig, key) {
		t.Fatal("valid HMAC without prefix should also pass")
	}
}

func TestVerifyHMAC_Invalid(t *testing.T) {
	body := []byte(`{"type":"Message"}`)
	if VerifyHMAC(body, "sha256=deadbeef00112233445566778899aabbccddeeff00112233445566778899aabb", "secret") {
		t.Fatal("wrong HMAC should fail")
	}
}

func TestVerifyHMAC_EmptySignature(t *testing.T) {
	if VerifyHMAC([]byte("body"), "", "key") {
		t.Fatal("empty signature should fail")
	}
}

func TestVerifyHMAC_EmptyKey(t *testing.T) {
	if VerifyHMAC([]byte("body"), "sha256=abc", "") {
		t.Fatal("empty key should fail")
	}
}

func TestVerifyHMAC_MalformedHex(t *testing.T) {
	if VerifyHMAC([]byte("body"), "sha256=not_valid_hex!", "key") {
		t.Fatal("malformed hex should fail")
	}
}

func TestVerifyHMAC_WrongBody(t *testing.T) {
	key := "secret"
	body := []byte("original")
	sig := "sha256=" + computeHMAC(body, key)

	if VerifyHMAC([]byte("tampered"), sig, key) {
		t.Fatal("tampered body should fail HMAC check")
	}
}

func TestVerifyHMAC_DifferentKey(t *testing.T) {
	body := []byte("data")
	sig := "sha256=" + computeHMAC(body, "key1")

	if VerifyHMAC(body, sig, "key2") {
		t.Fatal("different key should fail HMAC check")
	}
}
