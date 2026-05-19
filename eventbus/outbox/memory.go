package outbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
)

// ErrOutboxFull is returned by [Memory.Stage] when [WithMaxSize] is set
// and the new batch would exceed the configured limit. Stage is
// all-or-nothing: when this error is returned, no records from the
// batch were appended.
var ErrOutboxFull = errors.New("outbox: memory store at WithMaxSize limit")

// DeadLetterRecorder is an outbox-adapter-internal extension implemented
// by stores that support poison-message termination. Defined in this
// package (not in core) because core's eventbus.OutboxStore has no DLQ
// primitive. [Relay] calls Terminate when a record exceeds its retry
// budget; [Memory] satisfies this interface.
type DeadLetterRecorder interface {
	Terminate(ctx context.Context, id string, reason string) error
}

// MemoryConfig configures a [Memory] outbox.
type MemoryConfig struct {
	// Codec encodes domain events into payload + headers at Stage time.
	// Required. The same codec instance (or one with an equivalent
	// registry) must drive the Relay that drains this store; mismatched
	// codecs will route records through fail() at relay time.
	Codec eventbus.Codec
}

// MemoryOption configures a [Memory] outbox.
type MemoryOption func(*Memory)

// WithClock overrides time.Now. Useful for deterministic tests.
func WithClock(now func() time.Time) MemoryOption {
	return func(m *Memory) { m.now = now }
}

// WithIDGenerator overrides the default monotonic generator. Stage
// calls the generator while holding m.mu, so non-atomic user generators
// are still safe under concurrent Stage. The default generator is
// atomic.Uint64-backed and produces "mem-outbox-<n>".
func WithIDGenerator(gen func() string) MemoryOption {
	return func(m *Memory) { m.idGen = gen }
}

// WithMaxSize caps the active store at limit records. Stage returns
// [ErrOutboxFull] when accepting the batch would exceed the limit; the
// store stays untouched. 0 (the default) means unbounded.
//
// Unlike eventbus/inbox where MaxSize triggers eviction, the Outbox
// store cannot evict unsent records — that would silently lose events.
// ErrOutboxFull surfaces back to the caller so they (and the
// surrounding transaction, if any) can react.
func WithMaxSize(limit int) MemoryOption {
	return func(m *Memory) { m.maxSize = limit }
}

// DeadLetterRecord describes a record that the Relay has terminated
// after exhausting its retry budget.
type DeadLetterRecord struct {
	// Record is a snapshot of the OutboxRecord at termination time.
	// Headers / Payload are defensive copies.
	Record eventbus.OutboxRecord
	// Reason is the error message that caused termination.
	Reason string
	// FailedAt is the wall-clock at which Terminate was called.
	FailedAt time.Time
	// Attempts is the terminal attempt count, set to
	// Record.Attempts + 1 at Terminate time — i.e. it counts the
	// attempt that caused termination, not just the prior survivable
	// count.
	Attempts int
}

// Memory is a goroutine-safe in-process Outbox + OutboxStore +
// DeadLetterRecorder. See package doc for the non-transactional
// limitations — this type is for tests, single-instance examples, and
// exercising Relay behaviour. NOT suitable for production
// multi-instance services.
type Memory struct {
	mu      sync.RWMutex
	records []eventbus.OutboxRecord
	dlq     map[string]DeadLetterRecord

	codec   eventbus.Codec
	now     func() time.Time
	idGen   func() string
	maxSize int
}

// NewMemory constructs an in-memory outbox. Returns an error when
// MemoryConfig.Codec is nil.
func NewMemory(cfg MemoryConfig, opts ...MemoryOption) (*Memory, error) {
	if cfg.Codec == nil {
		return nil, errors.New("outbox memory: Codec is required")
	}
	m := &Memory{
		codec: cfg.Codec,
		now:   time.Now,
		dlq:   make(map[string]DeadLetterRecord),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.idGen == nil {
		var counter atomic.Uint64
		m.idGen = func() string {
			return "mem-outbox-" + strconv.FormatUint(counter.Add(1), 10)
		}
	}
	return m, nil
}

// Stage encodes each event and appends one OutboxRecord per event to
// the active store. Marshal failures abort the batch (all-or-nothing);
// if WithMaxSize would be exceeded, returns ErrOutboxFull. Empty
// events slice is a no-op.
//
// The canonical EventName / AggregateID / AggregateType fields come
// from the domain.DomainEvent interface methods, NOT from codec
// metadata, so a buggy codec cannot silently drop those columns.
func (m *Memory) Stage(ctx context.Context, topic string, events ...domain.DomainEvent) error {
	if len(events) == 0 {
		return nil
	}

	type staged struct {
		eventID       string
		eventName     string
		aggregateID   string
		aggregateType string
		payload       []byte
		headers       map[string]string
	}

	now := m.now()
	pending := make([]staged, 0, len(events))
	for _, ev := range events {
		msg, err := m.codec.Marshal(ctx, ev)
		if err != nil {
			return fmt.Errorf("outbox memory: marshal %s: %w", ev.EventID(), err)
		}
		headers := make(map[string]string, len(msg.Metadata))
		for k, v := range msg.Metadata {
			headers[k] = v
		}
		pending = append(pending, staged{
			eventID:       ev.EventID(),
			eventName:     ev.EventName(),
			aggregateID:   ev.AggregateID(),
			aggregateType: ev.AggregateType(),
			payload:       msg.Payload,
			headers:       headers,
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.maxSize > 0 && len(m.records)+len(pending) > m.maxSize {
		return ErrOutboxFull
	}
	for _, p := range pending {
		m.records = append(m.records, eventbus.OutboxRecord{
			ID:            m.idGen(),
			EventID:       p.eventID,
			Topic:         topic,
			EventName:     p.eventName,
			AggregateID:   p.aggregateID,
			AggregateType: p.aggregateType,
			Payload:       p.payload,
			Headers:       p.headers,
			CreatedAt:     now,
			AvailableAt:   now,
			Attempts:      0,
		})
	}
	return nil
}

// Fetch returns up to limit records that are due for delivery
// (AvailableAt <= now()), sorted by AvailableAt then ID. Returned
// records carry fresh Headers maps and Payload byte slices — callers
// may mutate them without corrupting the stored record.
func (m *Memory) Fetch(_ context.Context, limit int) ([]eventbus.OutboxRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.now()
	due := make([]eventbus.OutboxRecord, 0, len(m.records))
	for _, rec := range m.records {
		if !rec.AvailableAt.After(now) {
			due = append(due, rec)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].AvailableAt.Equal(due[j].AvailableAt) {
			return due[i].ID < due[j].ID
		}
		return due[i].AvailableAt.Before(due[j].AvailableAt)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]eventbus.OutboxRecord, 0, len(due))
	for _, rec := range due {
		out = append(out, cloneRecord(rec))
	}
	return out, nil
}

// MarkSent removes the record with the given id from the active store.
// Unknown ids are silently ignored — Relay never has a reason to mark
// a record it didn't observe via Fetch.
func (m *Memory) MarkSent(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, rec := range m.records {
		if rec.ID == id {
			m.records = append(m.records[:i], m.records[i+1:]...)
			return nil
		}
	}
	return nil
}

// MarkFailed increments Attempts and defers the record's next attempt
// to nextAttemptAt. The reason argument is log-only in this memory
// adapter (no durable field). Unknown ids are silently ignored.
func (m *Memory) MarkFailed(_ context.Context, id string, _ string, nextAttemptAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.records {
		if m.records[i].ID == id {
			m.records[i].Attempts++
			m.records[i].AvailableAt = nextAttemptAt
			return nil
		}
	}
	return nil
}

// Terminate moves the record with the given id from the active store
// to the dead-letter quarantine. The stored DeadLetterRecord.Attempts
// field is set to Record.Attempts + 1 — i.e. it counts the attempt
// that caused termination, not just the prior survivable attempts.
// Unknown ids are silently ignored.
//
// Terminate is part of the local [DeadLetterRecorder] interface, NOT
// of core's eventbus.OutboxStore — core has no DLQ primitive.
func (m *Memory) Terminate(_ context.Context, id string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, rec := range m.records {
		if rec.ID == id {
			m.records = append(m.records[:i], m.records[i+1:]...)
			m.dlq[id] = DeadLetterRecord{
				Record:   cloneRecord(rec),
				Reason:   reason,
				FailedAt: m.now(),
				Attempts: rec.Attempts + 1,
			}
			return nil
		}
	}
	return nil
}

// DeadLettered returns a snapshot of the dead-letter quarantine. The
// top-level map is fresh; each DeadLetterRecord.Record carries
// defensive copies of Headers and Payload.
func (m *Memory) DeadLettered() map[string]DeadLetterRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]DeadLetterRecord, len(m.dlq))
	for k, v := range m.dlq {
		out[k] = DeadLetterRecord{
			Record:   cloneRecord(v.Record),
			Reason:   v.Reason,
			FailedAt: v.FailedAt,
			Attempts: v.Attempts,
		}
	}
	return out
}

// Size returns the number of records currently in the active store
// (DLQ entries are not counted).
func (m *Memory) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.records)
}

func cloneRecord(rec eventbus.OutboxRecord) eventbus.OutboxRecord {
	out := rec
	if rec.Payload != nil {
		out.Payload = slices.Clone(rec.Payload)
	}
	if rec.Headers != nil {
		out.Headers = make(map[string]string, len(rec.Headers))
		for k, v := range rec.Headers {
			out.Headers[k] = v
		}
	}
	return out
}

var (
	_ eventbus.Outbox      = (*Memory)(nil)
	_ eventbus.OutboxStore = (*Memory)(nil)
	_ DeadLetterRecorder   = (*Memory)(nil)
)
