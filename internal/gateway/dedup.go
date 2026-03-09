package gateway

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

// DeduplicationStrategy determines how a message's dedup key is derived.
type DeduplicationStrategy string

const (
	DeduplicationByMessageID DeduplicationStrategy = "message-id"
	DeduplicationByPrompt    DeduplicationStrategy = "prompt"
	DeduplicationNone        DeduplicationStrategy = "none"
)

// dedupEntry tracks when a key was first seen.
type dedupEntry struct {
	expiresAt time.Time
}

// Deduplicator is an in-memory cache that prevents the same message from
// being processed twice within a configurable window.
type Deduplicator struct {
	mu      sync.Mutex
	entries map[string]*dedupEntry

	defaultWindow time.Duration
	cleanupTicker *time.Ticker
	done          chan struct{}
}

// NewDeduplicator creates a deduplicator with the given default window.
// It starts a background goroutine to evict expired entries every 30 seconds.
func NewDeduplicator(defaultWindow time.Duration) *Deduplicator {
	d := &Deduplicator{
		entries:       make(map[string]*dedupEntry),
		defaultWindow: defaultWindow,
		cleanupTicker: time.NewTicker(30 * time.Second),
		done:          make(chan struct{}),
	}
	go d.cleanupLoop()
	return d
}

// IsDuplicate returns true if the (userID, strategy, rawKey) combination has
// already been seen within the deduplication window. If it is new, it is
// registered and false is returned.
func (d *Deduplicator) IsDuplicate(userID string, strategy DeduplicationStrategy, rawKey string) bool {
	if strategy == DeduplicationNone || rawKey == "" {
		return false
	}

	key := d.buildKey(userID, strategy, rawKey)

	d.mu.Lock()
	defer d.mu.Unlock()

	if entry, ok := d.entries[key]; ok {
		if time.Now().Before(entry.expiresAt) {
			return true
		}
		delete(d.entries, key)
	}

	d.entries[key] = &dedupEntry{
		expiresAt: time.Now().Add(d.defaultWindow),
	}
	return false
}

// Len returns the number of tracked entries (including potentially expired ones
// not yet cleaned up).
func (d *Deduplicator) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

// Stop terminates the background cleanup goroutine.
func (d *Deduplicator) Stop() {
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	d.cleanupTicker.Stop()
}

// PromptHash returns a deterministic SHA-256 hex hash of the text, suitable
// for use as a dedup key with the "prompt" strategy.
func PromptHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)
}

func (d *Deduplicator) buildKey(userID string, strategy DeduplicationStrategy, rawKey string) string {
	return fmt.Sprintf("%s:%s:%s", userID, strategy, rawKey)
}

func (d *Deduplicator) cleanupLoop() {
	for {
		select {
		case <-d.done:
			return
		case <-d.cleanupTicker.C:
			d.evictExpired()
		}
	}
}

func (d *Deduplicator) evictExpired() {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, e := range d.entries {
		if now.After(e.expiresAt) {
			delete(d.entries, k)
		}
	}
}
