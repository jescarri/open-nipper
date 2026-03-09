// Package ratelimit provides an in-memory per-key sliding window rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces per-key rate limits using a sliding window counter.
// Each key maintains a list of event timestamps; Allow() prunes entries
// outside the window and checks whether the count is below the maximum.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	max     int
	window  time.Duration
}

type window struct {
	events []time.Time
}

// NewLimiter creates a rate limiter that allows at most max events per
// key within the specified window duration.
func NewLimiter(max int, windowDuration time.Duration) *Limiter {
	return &Limiter{
		windows: make(map[string]*window),
		max:     max,
		window:  windowDuration,
	}
}

// Allow checks whether the given key is within its rate limit. If allowed,
// it records the event and returns true. If the key has exhausted its
// allowance for the current window, it returns false along with the
// duration until the oldest entry expires (retry-after hint).
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	return l.allowAt(key, time.Now())
}

// allowAt is the internal implementation that accepts a clock value for testing.
func (l *Limiter) allowAt(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok {
		w = &window{}
		l.windows[key] = w
	}

	cutoff := now.Add(-l.window)
	w.prune(cutoff)

	if len(w.events) >= l.max {
		retryAfter := l.window
		if len(w.events) > 0 {
			retryAfter = w.events[0].Add(l.window).Sub(now)
		}
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, retryAfter
	}

	w.events = append(w.events, now)
	return true, 0
}

// Reset removes all tracked events for a key.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.windows, key)
}

// Count returns the number of events currently in the window for a key.
func (l *Limiter) Count(key string) int {
	return l.countAt(key, time.Now())
}

func (l *Limiter) countAt(key string, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok {
		return 0
	}

	cutoff := now.Add(-l.window)
	w.prune(cutoff)
	return len(w.events)
}

// Cleanup removes all keys that have no events within the current window.
// Call periodically from a background goroutine to prevent unbounded memory growth.
func (l *Limiter) Cleanup() {
	l.cleanupAt(time.Now())
}

func (l *Limiter) cleanupAt(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-l.window)
	for key, w := range l.windows {
		w.prune(cutoff)
		if len(w.events) == 0 {
			delete(l.windows, key)
		}
	}
}

// prune removes entries older than the cutoff. Must be called under lock.
func (w *window) prune(cutoff time.Time) {
	i := 0
	for i < len(w.events) && w.events[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		w.events = w.events[i:]
	}
}
