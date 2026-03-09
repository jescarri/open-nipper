package session_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-nipper/open-nipper/pkg/session"
)

func TestFileLock_AcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	lock := session.NewFileLock(path)
	if err := lock.Lock(context.Background(), "alice"); err != nil {
		t.Fatalf("Lock() error: %v", err)
	}
	if !lock.IsHeld() {
		t.Fatal("expected IsHeld()=true after Lock()")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("lock file should exist after Lock()")
	}

	lock.Unlock()

	if lock.IsHeld() {
		t.Fatal("expected IsHeld()=false after Unlock()")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lock file should be removed after Unlock()")
	}
}

func TestFileLock_UnlockIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	lock := session.NewFileLock(path)
	if err := lock.Lock(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	lock.Unlock()
	lock.Unlock()
}

func TestFileLock_SecondAcquireBlocksUntilRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	lock1 := session.NewFileLock(path)
	if err := lock1.Lock(context.Background(), "alice"); err != nil {
		t.Fatalf("lock1.Lock() error: %v", err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		lock1.Unlock()
	}()

	start := time.Now()
	lock2 := session.NewFileLock(path)
	if err := lock2.Lock(context.Background(), "alice"); err != nil {
		t.Fatalf("lock2.Lock() error: %v", err)
	}
	lock2.Unlock()

	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected lock2 to wait at least 100ms, only waited %s", elapsed)
	}
}

func TestFileLock_StaleIsOverridden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	stale := `{"pid":99999,"acquired_at":"2000-01-01T00:00:00Z","user_id":"old"}`
	if err := os.WriteFile(path, []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}

	lock := session.NewFileLock(path)
	if err := lock.Lock(context.Background(), "alice"); err != nil {
		t.Fatalf("Lock() on stale file error: %v", err)
	}
	lock.Unlock()
}

func TestFileLock_MalformedLockIsOverriddenWhenOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	lock := session.NewFileLock(path)
	if err := lock.Lock(context.Background(), "alice"); err != nil {
		t.Fatalf("Lock() on old malformed file error: %v", err)
	}
	lock.Unlock()
}

func TestFileLock_ContextCancelledWhileWaiting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	holder := session.NewFileLock(path)
	if err := holder.Lock(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	defer holder.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	waiter := session.NewFileLock(path)
	err := waiter.Lock(ctx, "alice")
	if err == nil {
		waiter.Unlock()
		t.Fatal("expected error when context is cancelled")
	}
}

func TestFileLock_ConcurrentAcquireSerialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl.lock")

	const goroutines = 5
	var (
		inCritical atomic.Int32
		acquired   atomic.Int32
		wg         sync.WaitGroup
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock := session.NewFileLock(path)
			if err := lock.Lock(context.Background(), "user"); err != nil {
				t.Errorf("Lock() error: %v", err)
				return
			}
			if inCritical.Add(1) != 1 {
				t.Error("more than one goroutine in critical section simultaneously")
			}
			acquired.Add(1)
			time.Sleep(2 * time.Millisecond)
			inCritical.Add(-1)
			lock.Unlock()
		}()
	}
	wg.Wait()
	if n := acquired.Load(); n != goroutines {
		t.Errorf("expected %d lock acquisitions, got %d", goroutines, n)
	}
}

func TestCleanStaleLocks_RemovesStaleKeepsFresh(t *testing.T) {
	dir := t.TempDir()

	stalePath := filepath.Join(dir, "old.jsonl.lock")
	_ = os.WriteFile(stalePath, []byte(`{"pid":1,"acquired_at":"2000-01-01T00:00:00Z","user_id":"x"}`), 0600)

	freshTS := time.Now().UTC().Format(time.RFC3339Nano)
	freshPath := filepath.Join(dir, "new.jsonl.lock")
	_ = os.WriteFile(freshPath, []byte(`{"pid":2,"acquired_at":"`+freshTS+`","user_id":"y"}`), 0600)

	nonLock := filepath.Join(dir, "something.jsonl")
	_ = os.WriteFile(nonLock, []byte("data"), 0600)

	if err := session.CleanStaleLocks(dir); err != nil {
		t.Fatalf("CleanStaleLocks error: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected stale lock to be removed")
	}
	if _, err := os.Stat(freshPath); os.IsNotExist(err) {
		t.Error("expected fresh lock to remain")
	}
	if _, err := os.Stat(nonLock); os.IsNotExist(err) {
		t.Error("expected non-lock file to remain")
	}
}

func TestCleanStaleLocks_NonExistentDirIsNoop(t *testing.T) {
	if err := session.CleanStaleLocks("/nonexistent/path/that/does/not/exist"); err != nil {
		t.Fatalf("CleanStaleLocks on missing dir should return nil, got: %v", err)
	}
}
