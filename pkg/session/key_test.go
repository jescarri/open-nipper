package session_test

import (
	"testing"

	"github.com/open-nipper/open-nipper/pkg/session"
)

func TestParseSessionKey_Valid(t *testing.T) {
	key := "user:alice:channel:whatsapp:session:ses-001"
	uid, ct, sid, err := session.ParseSessionKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != "alice" || ct != "whatsapp" || sid != "ses-001" {
		t.Errorf("got uid=%q ct=%q sid=%q", uid, ct, sid)
	}
}

func TestParseSessionKey_SessionIDWithHyphens(t *testing.T) {
	key := "user:usr-01:channel:mqtt:session:wa-1555010001-s-whatsapp-net"
	uid, ct, sid, err := session.ParseSessionKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != "usr-01" || ct != "mqtt" || sid != "wa-1555010001-s-whatsapp-net" {
		t.Errorf("got uid=%q ct=%q sid=%q", uid, ct, sid)
	}
}

func TestParseSessionKey_InvalidMissingPrefix(t *testing.T) {
	_, _, _, err := session.ParseSessionKey("alice:channel:slack:session:x")
	if err == nil {
		t.Fatal("expected error for missing 'user:' prefix")
	}
}

func TestParseSessionKey_InvalidMissingChannel(t *testing.T) {
	_, _, _, err := session.ParseSessionKey("user:alice:session:x")
	if err == nil {
		t.Fatal("expected error for missing ':channel:'")
	}
}

func TestParseSessionKey_InvalidMissingSession(t *testing.T) {
	_, _, _, err := session.ParseSessionKey("user:alice:channel:whatsapp")
	if err == nil {
		t.Fatal("expected error for missing ':session:'")
	}
}

func TestBuildAndParse_Roundtrip(t *testing.T) {
	key := session.BuildSessionKey("u-01", "whatsapp", "ses-xyz")
	uid, ct, sid, err := session.ParseSessionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if uid != "u-01" || ct != "whatsapp" || sid != "ses-xyz" {
		t.Errorf("roundtrip mismatch: uid=%q ct=%q sid=%q", uid, ct, sid)
	}
}

func TestSanitizeSessionID_SpecialChars(t *testing.T) {
	got := session.SanitizeSessionID("1555010001@s.whatsapp.net")
	if got == "" {
		t.Fatal("expected non-empty result")
	}
	for _, c := range got {
		if c != '-' && c != '_' && !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			t.Errorf("invalid character %q in sanitized ID %q", string(c), got)
		}
	}
}

func TestSanitizeSessionID_EmptyReturnsDefault(t *testing.T) {
	got := session.SanitizeSessionID("")
	if got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}
}

func TestSanitizeSessionID_AllSpecialReturnsDefault(t *testing.T) {
	got := session.SanitizeSessionID("@.!#")
	if got != "default" {
		t.Errorf("expected 'default' for all-special input, got %q", got)
	}
}
