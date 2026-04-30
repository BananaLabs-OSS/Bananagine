// Package eventbuffer maintains a ring buffer of recent container
// events so clients that can't hold a long-lived SSE connection
// (WASM cells, stateless functions) can poll for events since a
// cursor instead.
package eventbuffer

import (
	"sync"
	"time"
)

// Event is the serializable shape we store. It mirrors the fields the
// Docker provider emits, plus a Timestamp so clients can resume from
// where they left off.
type Event struct {
	Timestamp   int64  `json:"timestamp"`
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
	Action      string `json:"action"`
}

// Buffer is a size-bounded, time-bounded ring of events. Writes are
// cheap (append + drop oldest when over cap); reads after a cursor
// are O(N) over the current contents.
type Buffer struct {
	mu        sync.Mutex
	events    []Event
	maxSize   int
	maxAgeNs  int64
}

// New returns a Buffer that holds at most maxSize events and drops
// anything older than maxAge regardless of size.
func New(maxSize int, maxAge time.Duration) *Buffer {
	return &Buffer{
		events:   make([]Event, 0, maxSize),
		maxSize:  maxSize,
		maxAgeNs: maxAge.Nanoseconds(),
	}
}

// Append records a new event with the current wall-clock time.
func (b *Buffer) Append(containerID, name, action string) {
	now := time.Now().UnixNano()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, Event{
		Timestamp:   now,
		ContainerID: containerID,
		Name:        name,
		Action:      action,
	})
	b.pruneLocked(now)
}

// Since returns every event with Timestamp > sinceNanos, limited to
// the first `limit` matches (0 means no limit). Results are returned
// in append (chronological) order.
func (b *Buffer) Since(sinceNanos int64, limit int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now().UnixNano())
	out := make([]Event, 0, 16)
	for _, e := range b.events {
		if e.Timestamp > sinceNanos {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// pruneLocked trims events older than maxAge or exceeding maxSize.
// Called with mu held.
func (b *Buffer) pruneLocked(now int64) {
	cutoff := now - b.maxAgeNs
	// Find first index whose Timestamp > cutoff.
	i := 0
	for i < len(b.events) && b.events[i].Timestamp <= cutoff {
		i++
	}
	if i > 0 {
		b.events = b.events[i:]
	}
	if len(b.events) > b.maxSize {
		b.events = b.events[len(b.events)-b.maxSize:]
	}
}
