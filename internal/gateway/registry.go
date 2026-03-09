package gateway

import (
	"sync"
	"time"

	"github.com/jescarri/open-nipper/internal/models"
)

// DeliveryEntry holds a DeliveryContext for an active session, along with
// the time it was registered.  The dispatcher uses this to route agent
// response events back to the correct channel adapter.
type DeliveryEntry struct {
	Context      models.DeliveryContext
	Meta         models.ChannelMeta // channel-specific metadata needed for reply (e.g. WhatsAppMeta)
	InboundParts []models.ContentPart
	CreatedAt    time.Time
}

// Registry is a thread-safe, in-memory map of sessionKey → FIFO queue of DeliveryEntry.
//
// When multiple messages for the same session are queued, each Register appends
// an entry. The dispatcher Consumes (pops) the front on each "done" or "error"
// event, so the first response matches the first queued message.
type Registry struct {
	mu      sync.RWMutex
	entries map[string][]*DeliveryEntry
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string][]*DeliveryEntry)}
}

// Register appends a DeliveryContext to the queue for sessionKey.
// Each inbound message adds one entry; Consume removes one per done/error event.
func (r *Registry) Register(sessionKey string, dc models.DeliveryContext, meta models.ChannelMeta, inboundParts []models.ContentPart) {
	r.mu.Lock()
	defer r.mu.Unlock()
	partsCopy := append([]models.ContentPart(nil), inboundParts...)
	entry := &DeliveryEntry{
		Context:      dc,
		Meta:         meta,
		InboundParts: partsCopy,
		CreatedAt:    time.Now(),
	}
	r.entries[sessionKey] = append(r.entries[sessionKey], entry)
}

// Lookup returns the front entry for sessionKey without removing it (peek).
// Used for routing delta events when the entry is not yet consumed.
func (r *Registry) Lookup(sessionKey string) (models.DeliveryContext, models.ChannelMeta, []models.ContentPart, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q := r.entries[sessionKey]
	if len(q) == 0 {
		return models.DeliveryContext{}, nil, nil, false
	}
	e := q[0]
	return e.Context, e.Meta, append([]models.ContentPart(nil), e.InboundParts...), true
}

// Consume pops and returns the front entry for sessionKey.
// Used on "done" and "error" events so each response consumes exactly one queued message.
func (r *Registry) Consume(sessionKey string) (models.DeliveryContext, models.ChannelMeta, []models.ContentPart, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.entries[sessionKey]
	if len(q) == 0 {
		return models.DeliveryContext{}, nil, nil, false
	}
	e := q[0]
	r.entries[sessionKey] = q[1:]
	if len(r.entries[sessionKey]) == 0 {
		delete(r.entries, sessionKey)
	}
	return e.Context, e.Meta, append([]models.ContentPart(nil), e.InboundParts...), true
}

// Len returns the total number of entries across all session queues.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, q := range r.entries {
		n += len(q)
	}
	return n
}

// EvictOlderThan removes entries whose CreatedAt is before cutoff from each queue.
// Returns the number of evicted entries.
func (r *Registry) EvictOlderThan(cutoff time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	evicted := 0
	for k, q := range r.entries {
		keep := q[:0]
		for _, e := range q {
			if e.CreatedAt.Before(cutoff) {
				evicted++
			} else {
				keep = append(keep, e)
			}
		}
		if len(keep) == 0 {
			delete(r.entries, k)
		} else {
			r.entries[k] = keep
		}
	}
	return evicted
}
