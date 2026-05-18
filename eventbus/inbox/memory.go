// Package inbox provides in-process implementations of the
// github.com/slam0504/go-ddd-core/eventbus.Inbox interface, suitable for
// tests, single-instance services, and examples.
//
// The Memory inbox originally shipped in go-ddd-core v0.2.0 and was
// relocated here as part of the v0.4.0 deprecation cycle. The Inbox
// interface itself remains in go-ddd-core/eventbus.
//
// Out-of-process implementations (SQL, Redis, ...) belong in sibling
// packages under go-ddd-adapters/eventbus/inbox/: the driver and dialect
// choice (database/sql vs sqlx vs gorm; PostgreSQL vs MySQL) is an
// infrastructure decision the core library should not impose, and
// grouping each tech in its own sub-package keeps transitive
// dependencies isolated.
package inbox

import (
	"context"
	"sync"
	"time"

	"github.com/slam0504/go-ddd-core/eventbus"
)

// Memory is a goroutine-safe in-memory Inbox. It records every seen
// (consumer, eventID) pair; suitable for tests, single-instance examples,
// and short-lived processes. Not suitable for production multi-instance
// services because the dedup window does not survive a restart and is not
// shared across replicas.
type Memory struct {
	mu      sync.RWMutex
	seenAt  map[eventbus.InboxKey]time.Time
	now     func() time.Time
	maxSize int
}

// MemoryOption configures a Memory inbox.
type MemoryOption func(*Memory)

// WithClock overrides time.Now, useful for deterministic tests.
func WithClock(now func() time.Time) MemoryOption {
	return func(m *Memory) { m.now = now }
}

// WithMaxSize bounds the dedup map by evicting the oldest half whenever the
// map reaches limit. Setting limit to 0 disables eviction (unbounded growth).
// The eviction strategy is intentionally simple — production deployments
// should use a TTL-based store (see WithTTL) or an out-of-process inbox
// adapter.
func WithMaxSize(limit int) MemoryOption {
	return func(m *Memory) { m.maxSize = limit }
}

// NewMemory constructs an in-memory inbox.
func NewMemory(opts ...MemoryOption) *Memory {
	m := &Memory{
		seenAt: make(map[eventbus.InboxKey]time.Time),
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Seen reports whether key has been recorded.
func (m *Memory) Seen(_ context.Context, key eventbus.InboxKey) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.seenAt[key]
	return ok, nil
}

// Record marks key as processed. Calling Record on an already-seen key is a
// no-op (the original timestamp is preserved) so handlers may safely retry
// without skewing the eviction order.
func (m *Memory) Record(_ context.Context, key eventbus.InboxKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seenAt[key]; ok {
		return nil
	}
	m.seenAt[key] = m.now()
	if m.maxSize > 0 && len(m.seenAt) > m.maxSize {
		m.evictOldestHalf()
	}
	return nil
}

// Size returns the current number of recorded keys. Exported primarily for
// tests and operational metrics.
func (m *Memory) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.seenAt)
}

// evictOldestHalf removes the oldest half of entries; called under lock.
// O(n log n) in n=len(seenAt); acceptable since eviction is rare relative to
// Seen/Record traffic.
func (m *Memory) evictOldestHalf() {
	type kv struct {
		key eventbus.InboxKey
		at  time.Time
	}
	all := make([]kv, 0, len(m.seenAt))
	for k, v := range m.seenAt {
		all = append(all, kv{key: k, at: v})
	}
	// Partial selection: find the median timestamp, then drop everything
	// older. Using a full sort to keep code straightforward.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j-1].at.After(all[j].at); j-- {
			all[j-1], all[j] = all[j], all[j-1]
		}
	}
	cutoff := len(all) / 2
	for i := 0; i < cutoff; i++ {
		delete(m.seenAt, all[i].key)
	}
}

var _ eventbus.Inbox = (*Memory)(nil)
