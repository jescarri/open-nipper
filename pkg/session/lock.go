package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	staleLockThreshold = 30 * time.Minute
	lockMaxWait        = 10 * time.Second
	lockRetryBase      = 100 * time.Millisecond
	lockRetryMax       = 800 * time.Millisecond
)

type lockContent struct {
	PID        int    `json:"pid"`
	AcquiredAt string `json:"acquired_at"`
	UserID     string `json:"user_id"`
}

// FileLock is an advisory, file-based mutex for a single session transcript.
//
// Acquisition uses the hard-link technique to avoid the TOCTOU race that exists
// between O_EXCL file creation and writing the lock content:
//  1. Write lock content to a randomly-named temp file (content is ready before anyone sees it).
//  2. os.Link(tmpFile, lockPath) atomically claims ownership or fails with EEXIST.
//  3. On success, defer removes the temp file; the lock file (hard-linked inode) remains.
//  4. On release, os.Remove(lockPath) drops the last link to the inode.
type FileLock struct {
	path   string
	locked bool
}

// NewFileLock returns a FileLock for the given lock-file path.
func NewFileLock(path string) *FileLock {
	return &FileLock{path: path}
}

// Lock acquires the lock. It blocks with exponential backoff for up to lockMaxWait.
// Stale locks (> 30 minutes old) are silently removed and the acquisition retried.
// The context is checked on every retry iteration.
func (l *FileLock) Lock(ctx context.Context, userID string) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("file lock: generate temp name: %w", err)
	}
	tmpPath := fmt.Sprintf("%s.%x.tmp", l.path, rnd)

	data, _ := json.Marshal(lockContent{
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		UserID:     userID,
	})
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("file lock prepare temp %q: %w", tmpPath, err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	deadline := time.Now().Add(lockMaxWait)
	delay := lockRetryBase

	for {
		if err := os.Link(tmpPath, l.path); err == nil {
			l.locked = true
			return nil
		} else if !os.IsExist(err) {
			return fmt.Errorf("file lock link %q: %w", l.path, err)
		}

		if lockFileIsStale(l.path) {
			_ = os.Remove(l.path)
			continue
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("could not acquire lock on %q after %s", l.path, lockMaxWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("lock acquire cancelled: %w", ctx.Err())
		default:
		}

		time.Sleep(delay)
		delay = delay * 2
		if delay > lockRetryMax {
			delay = lockRetryMax
		}
	}
}

// Unlock releases the lock by removing the lock file.
// It is idempotent and safe to call even if Lock was never called.
func (l *FileLock) Unlock() {
	_ = os.Remove(l.path)
	l.locked = false
}

// IsHeld returns true if this FileLock instance successfully acquired the lock
// and has not yet released it.
func (l *FileLock) IsHeld() bool {
	return l.locked
}

func lockFileIsStale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}

	var lc lockContent
	if err := json.Unmarshal(data, &lc); err != nil {
		return time.Since(info.ModTime()) > 5*time.Minute
	}

	t, err := time.Parse(time.RFC3339Nano, lc.AcquiredAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339, lc.AcquiredAt)
		if err != nil {
			return time.Since(info.ModTime()) > 5*time.Minute
		}
	}
	return time.Since(t) > staleLockThreshold
}

// CleanStaleLocks scans dir for stale *.jsonl.lock files and removes them.
// It is safe to call concurrently and is intended to run periodically in a
// background goroutine.
func CleanStaleLocks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clean stale locks: read dir %q: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl.lock") {
			continue
		}
		lockPath := filepath.Join(dir, e.Name())
		if lockFileIsStale(lockPath) {
			_ = os.Remove(lockPath)
		}
	}
	return nil
}
