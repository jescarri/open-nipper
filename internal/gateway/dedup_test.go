package gateway

import (
	"testing"
	"time"
)

func TestDeduplicator_NoneStrategy(t *testing.T) {
	d := NewDeduplicator(30 * time.Second)
	defer d.Stop()

	if d.IsDuplicate("user-01", DeduplicationNone, "anything") {
		t.Fatal("DeduplicationNone should never return true")
	}
	if d.IsDuplicate("user-01", DeduplicationNone, "anything") {
		t.Fatal("DeduplicationNone should never return true on repeat")
	}
}

func TestDeduplicator_EmptyKey(t *testing.T) {
	d := NewDeduplicator(30 * time.Second)
	defer d.Stop()

	if d.IsDuplicate("user-01", DeduplicationByMessageID, "") {
		t.Fatal("empty key should never be a duplicate")
	}
}

func TestDeduplicator_MessageIDStrategy(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Stop()

	if d.IsDuplicate("user-01", DeduplicationByMessageID, "msg-aaa") {
		t.Fatal("first occurrence should not be a duplicate")
	}
	if !d.IsDuplicate("user-01", DeduplicationByMessageID, "msg-aaa") {
		t.Fatal("second occurrence should be a duplicate")
	}
}

func TestDeduplicator_PromptStrategy(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Stop()

	hash := PromptHash("hello world")

	if d.IsDuplicate("user-01", DeduplicationByPrompt, hash) {
		t.Fatal("first prompt hash should not be duplicate")
	}
	if !d.IsDuplicate("user-01", DeduplicationByPrompt, hash) {
		t.Fatal("second prompt hash should be duplicate")
	}
}

func TestDeduplicator_DifferentUsersAreIndependent(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Stop()

	if d.IsDuplicate("user-01", DeduplicationByMessageID, "msg-1") {
		t.Fatal("user-01 first should not be duplicate")
	}
	if d.IsDuplicate("user-02", DeduplicationByMessageID, "msg-1") {
		t.Fatal("user-02 same key should not be duplicate (different user)")
	}
}

func TestDeduplicator_ExpiresAfterWindow(t *testing.T) {
	d := NewDeduplicator(50 * time.Millisecond)
	defer d.Stop()

	if d.IsDuplicate("user-01", DeduplicationByMessageID, "msg-x") {
		t.Fatal("first should not be duplicate")
	}
	time.Sleep(100 * time.Millisecond)

	if d.IsDuplicate("user-01", DeduplicationByMessageID, "msg-x") {
		t.Fatal("should not be duplicate after window expired")
	}
}

func TestDeduplicator_Len(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	defer d.Stop()

	d.IsDuplicate("u1", DeduplicationByMessageID, "a")
	d.IsDuplicate("u1", DeduplicationByMessageID, "b")
	d.IsDuplicate("u2", DeduplicationByMessageID, "a")

	if d.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", d.Len())
	}
}

func TestPromptHash_Deterministic(t *testing.T) {
	a := PromptHash("deploy the service")
	b := PromptHash("deploy the service")
	if a != b {
		t.Fatalf("expected identical hashes, got %q and %q", a, b)
	}

	c := PromptHash("deploy the OTHER service")
	if a == c {
		t.Fatal("different inputs should produce different hashes")
	}
}

func TestDeduplicator_EvictExpired(t *testing.T) {
	d := NewDeduplicator(50 * time.Millisecond)
	defer d.Stop()

	d.IsDuplicate("u1", DeduplicationByMessageID, "a")
	d.IsDuplicate("u1", DeduplicationByMessageID, "b")
	if d.Len() != 2 {
		t.Fatalf("expected 2, got %d", d.Len())
	}

	time.Sleep(100 * time.Millisecond)
	d.evictExpired()

	if d.Len() != 0 {
		t.Fatalf("expected 0 after eviction, got %d", d.Len())
	}
}

func TestDeduplicator_StopIdempotent(t *testing.T) {
	d := NewDeduplicator(5 * time.Second)
	d.Stop()
	d.Stop() // should not panic
}
