package ratelimit

import (
	"testing"
	"time"
)

func TestAllow_UnderLimit(t *testing.T) {
	l := NewLimiter(5, time.Minute)
	for i := 0; i < 5; i++ {
		ok, _ := l.Allow("key1")
		if !ok {
			t.Fatalf("expected allow on attempt %d", i+1)
		}
	}
}

func TestAllow_ExceedsLimit(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		ok, _ := l.Allow("key1")
		if !ok {
			t.Fatalf("expected allow on attempt %d", i+1)
		}
	}
	ok, retryAfter := l.Allow("key1")
	if ok {
		t.Fatal("expected rejection after limit exceeded")
	}
	if retryAfter <= 0 {
		t.Fatal("expected positive retry-after duration")
	}
}

func TestAllow_IndependentKeys(t *testing.T) {
	l := NewLimiter(2, time.Minute)

	ok, _ := l.Allow("a")
	if !ok {
		t.Fatal("key a should be allowed")
	}
	ok, _ = l.Allow("a")
	if !ok {
		t.Fatal("key a should be allowed")
	}
	ok, _ = l.Allow("a")
	if ok {
		t.Fatal("key a should be rejected")
	}

	ok, _ = l.Allow("b")
	if !ok {
		t.Fatal("key b should be allowed (independent)")
	}
}

func TestAllow_WindowExpiry(t *testing.T) {
	l := NewLimiter(2, 100*time.Millisecond)

	now := time.Now()
	ok, _ := l.allowAt("x", now)
	if !ok {
		t.Fatal("expected allow")
	}
	ok, _ = l.allowAt("x", now.Add(10*time.Millisecond))
	if !ok {
		t.Fatal("expected allow")
	}
	ok, _ = l.allowAt("x", now.Add(20*time.Millisecond))
	if ok {
		t.Fatal("expected rejection")
	}

	// After window expires, should allow again.
	ok, _ = l.allowAt("x", now.Add(110*time.Millisecond))
	if !ok {
		t.Fatal("expected allow after window expiry")
	}
}

func TestAllow_RetryAfterAccuracy(t *testing.T) {
	l := NewLimiter(1, time.Second)

	now := time.Now()
	l.allowAt("k", now)

	_, retryAfter := l.allowAt("k", now.Add(200*time.Millisecond))
	expected := 800 * time.Millisecond
	tolerance := 10 * time.Millisecond
	if retryAfter < expected-tolerance || retryAfter > expected+tolerance {
		t.Fatalf("retry after = %v, want ~%v", retryAfter, expected)
	}
}

func TestReset(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	l.Allow("k")
	ok, _ := l.Allow("k")
	if ok {
		t.Fatal("expected rejection")
	}
	l.Reset("k")
	ok, _ = l.Allow("k")
	if !ok {
		t.Fatal("expected allow after reset")
	}
}

func TestCount(t *testing.T) {
	l := NewLimiter(10, time.Minute)
	if l.Count("k") != 0 {
		t.Fatal("expected 0 for unknown key")
	}
	l.Allow("k")
	l.Allow("k")
	l.Allow("k")
	if l.Count("k") != 3 {
		t.Fatalf("expected 3, got %d", l.Count("k"))
	}
}

func TestCleanup(t *testing.T) {
	l := NewLimiter(10, 100*time.Millisecond)

	now := time.Now()
	l.allowAt("a", now)
	l.allowAt("b", now)

	// After window, cleanup should remove both.
	l.cleanupAt(now.Add(200 * time.Millisecond))

	if l.countAt("a", now.Add(200*time.Millisecond)) != 0 {
		t.Fatal("expected a to be cleaned up")
	}
	if l.countAt("b", now.Add(200*time.Millisecond)) != 0 {
		t.Fatal("expected b to be cleaned up")
	}
}

func TestConcurrentAccess(t *testing.T) {
	l := NewLimiter(1000, time.Minute)
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				l.Allow("concurrent")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	count := l.Count("concurrent")
	if count != 1000 {
		t.Fatalf("expected 1000 events, got %d", count)
	}
}

func TestAllow_ZeroMax(t *testing.T) {
	l := NewLimiter(0, time.Minute)
	ok, _ := l.Allow("k")
	if ok {
		t.Fatal("expected rejection with max=0")
	}
}

func TestCleanup_PreservesActive(t *testing.T) {
	l := NewLimiter(10, time.Second)

	now := time.Now()
	l.allowAt("old", now.Add(-2*time.Second))
	l.allowAt("fresh", now)

	l.cleanupAt(now)

	if l.countAt("old", now) != 0 {
		t.Fatal("expected old to be cleaned up")
	}
	if l.countAt("fresh", now) != 1 {
		t.Fatal("expected fresh to be preserved")
	}
}
