package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
)

// silentLog discards log output so failing-case logs don't drown tests.
func silentLog() logger.Logger {
	return slogger.New(slogger.Config{Writer: io.Discard, Level: slog.LevelError + 1})
}

// --- fakes ---------------------------------------------------------

type publishCall struct {
	ctx   context.Context
	topic string
	event domain.DomainEvent
}

// fakePublisher captures Publish calls and can be programmed to fail
// or panic. Goroutine-safe.
type fakePublisher struct {
	mu        sync.Mutex
	calls     []publishCall
	err       error
	panicMsg  string
	hookEvent func(ctx context.Context, ev domain.DomainEvent)
}

func (p *fakePublisher) Close() error { return nil }

func (p *fakePublisher) Publish(ctx context.Context, topic string, events ...domain.DomainEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ev := range events {
		if p.hookEvent != nil {
			p.hookEvent(ctx, ev)
		}
		p.calls = append(p.calls, publishCall{ctx: ctx, topic: topic, event: ev})
	}
	if p.panicMsg != "" {
		panic(p.panicMsg)
	}
	return p.err
}

// dlqLessStore implements eventbus.OutboxStore but NOT DeadLetterRecorder.
type dlqLessStore struct{}

func (dlqLessStore) Fetch(context.Context, int) ([]eventbus.OutboxRecord, error)   { return nil, nil }
func (dlqLessStore) MarkSent(context.Context, string) error                        { return nil }
func (dlqLessStore) MarkFailed(context.Context, string, string, time.Time) error   { return nil }

// scriptedStore returns a fixed list of records on Fetch and captures
// MarkSent / MarkFailed / Terminate calls.
type scriptedStore struct {
	mu sync.Mutex

	pending []eventbus.OutboxRecord

	markSent   []string
	markFailed []failedCall
	terminated []termCall
}

type failedCall struct {
	id     string
	reason string
	next   time.Time
}

type termCall struct {
	id     string
	reason string
}

func (s *scriptedStore) Fetch(_ context.Context, limit int) ([]eventbus.OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil, nil
	}
	n := len(s.pending)
	if limit > 0 && limit < n {
		n = limit
	}
	out := append([]eventbus.OutboxRecord{}, s.pending[:n]...)
	s.pending = s.pending[n:]
	return out, nil
}

func (s *scriptedStore) MarkSent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markSent = append(s.markSent, id)
	return nil
}

func (s *scriptedStore) MarkFailed(_ context.Context, id, reason string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markFailed = append(s.markFailed, failedCall{id, reason, next})
	return nil
}

func (s *scriptedStore) Terminate(_ context.Context, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminated = append(s.terminated, termCall{id, reason})
	return nil
}

func (s *scriptedStore) Counts() (sent, failed, term int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.markSent), len(s.markFailed), len(s.terminated)
}

// panicOnUnmarshalCodec satisfies eventbus.Codec by delegating Marshal
// to a real kafka codec and panicking on Unmarshal — used to exercise
// the Relay's recover-from-Unmarshal-panic path.
type panicOnUnmarshalCodec struct {
	inner *kafka.JSONCodec
}

func newPanicOnUnmarshalCodec() *panicOnUnmarshalCodec {
	c := &panicOnUnmarshalCodec{inner: kafka.NewJSONCodec()}
	c.inner.Register("test.event.v1", func() domain.DomainEvent { return &testEvent{} })
	return c
}

func (c *panicOnUnmarshalCodec) Marshal(ctx context.Context, ev domain.DomainEvent) (*message.Message, error) {
	return c.inner.Marshal(ctx, ev)
}

func (c *panicOnUnmarshalCodec) Unmarshal(_ context.Context, _ *message.Message) (domain.DomainEvent, string, error) {
	panic("unmarshal boom")
}

// --- helpers --------------------------------------------------------

func mustNewRelay(t *testing.T, store eventbus.OutboxStore, pub eventbus.Publisher, codec eventbus.Codec, opts ...outbox.RelayOption) *outbox.Relay {
	t.Helper()
	r, err := outbox.NewRelay(outbox.RelayConfig{
		Store:     store,
		Publisher: pub,
		Codec:     codec,
		Logger:    silentLog(),
	}, opts...)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	return r
}

func makeRegisteredCodec() *kafka.JSONCodec {
	c := kafka.NewJSONCodec()
	c.Register("test.event.v1", func() domain.DomainEvent { return &testEvent{} })
	return c
}

// stageOne stages one event into a helper Memory and returns its
// OutboxRecord, so scriptedStore can replay a realistic shape.
func stageOne(t *testing.T, codec eventbus.Codec, eventID string) eventbus.OutboxRecord {
	t.Helper()
	helper, err := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	if err != nil {
		t.Fatalf("helper NewMemory: %v", err)
	}
	if err := helper.Stage(context.Background(), "topic-test",
		makeEvent(eventID, "test.event.v1", "agg-1", "Test", "body")); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	recs, _ := helper.Fetch(context.Background(), 1)
	return recs[0]
}

// runWithOneTick spins Relay.Run briefly so at least one drain happens
// against scriptedStore.pending, then cancels.
func runWithOneTick(t *testing.T, r *outbox.Relay) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

// --- constructor invariants ----------------------------------------

func TestNewRelay_RequiresDLQWhenMaxAttemptsPositive(t *testing.T) {
	_, err := outbox.NewRelay(outbox.RelayConfig{
		Store:     dlqLessStore{},
		Publisher: &fakePublisher{},
		Codec:     makeRegisteredCodec(),
		Logger:    silentLog(),
	}, outbox.WithMaxAttempts(3))
	if err == nil {
		t.Fatal("NewRelay: want error when MaxAttempts > 0 and Store is DLQ-less")
	}
}

func TestNewRelay_AllowsMissingDLQWhenMaxAttemptsZero(t *testing.T) {
	if _, err := outbox.NewRelay(outbox.RelayConfig{
		Store:     dlqLessStore{},
		Publisher: &fakePublisher{},
		Codec:     makeRegisteredCodec(),
		Logger:    silentLog(),
	}, outbox.WithMaxAttempts(0)); err != nil {
		t.Fatalf("NewRelay with MaxAttempts=0 and DLQ-less store: %v", err)
	}
}

// --- happy path / fail routing -------------------------------------

func TestRelay_PublishSuccessCallsMarkSent(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "e1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}
	r := mustNewRelay(t, store, &fakePublisher{}, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if sent, failed, term := store.Counts(); sent != 1 || failed != 0 || term != 0 {
		t.Fatalf("counts sent=%d failed=%d term=%d", sent, failed, term)
	}
}

func TestRelay_PublishErrorBelowMaxAttemptsCallsMarkFailed(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "e1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}
	r := mustNewRelay(t, store, &fakePublisher{err: errors.New("broker down")}, codec,
		outbox.WithMaxAttempts(3),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if _, failed, term := store.Counts(); failed != 1 || term != 0 {
		t.Fatalf("want one MarkFailed, no Terminate; got failed=%d term=%d", failed, term)
	}
}

func TestRelay_PublishErrorAtMaxAttemptsCallsTerminate(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "e1")
	rec.Attempts = 2 // already tried twice; next failure crosses MaxAttempts=3.
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}
	r := mustNewRelay(t, store, &fakePublisher{err: errors.New("broker down")}, codec,
		outbox.WithMaxAttempts(3),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if sent, failed, term := store.Counts(); term != 1 || failed != 0 || sent != 0 {
		t.Fatalf("want one Terminate; got sent=%d failed=%d term=%d", sent, failed, term)
	}
}

func TestRelay_CodecUnmarshalErrorRoutesToFail(t *testing.T) {
	// Use a codec without the event registered → Unmarshal returns error.
	emptyCodec := kafka.NewJSONCodec()
	rec := stageOne(t, makeRegisteredCodec(), "e1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}
	r := mustNewRelay(t, store, &fakePublisher{}, emptyCodec,
		outbox.WithMaxAttempts(3),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if _, failed, term := store.Counts(); failed != 1 || term != 0 {
		t.Fatalf("want one MarkFailed via decode-fail; got failed=%d term=%d", failed, term)
	}
}

// --- reentry / context ---------------------------------------------

func TestRelay_SameRelayReentryReturnsErrAlreadyRunning(t *testing.T) {
	r := mustNewRelay(t, &scriptedStore{}, &fakePublisher{}, makeRegisteredCodec(),
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(20 * time.Millisecond) // let first Run grab the atomic flag

	if err := r.Run(ctx); !errors.Is(err, outbox.ErrRelayAlreadyRunning) {
		t.Fatalf("second Run err = %v, want ErrRelayAlreadyRunning", err)
	}
	cancel()
	<-done
}

func TestRelay_HonoursContextCancellationBetweenPolls(t *testing.T) {
	r := mustNewRelay(t, &scriptedStore{}, &fakePublisher{}, makeRegisteredCodec(),
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(time.Hour),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

// --- header restoration --------------------------------------------

func TestRelay_HeaderRestorerReinjectsCoreHeaders(t *testing.T) {
	codec := makeRegisteredCodec()
	helper, _ := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	ctx := kafka.WithTraceID(context.Background(), "trace-1")
	ctx = kafka.WithCausationID(ctx, "cause-1")
	ctx = kafka.WithCorrelationID(ctx, "corr-1")
	_ = helper.Stage(ctx, "topic", makeEvent("e1", "test.event.v1", "agg", "Test", "body"))
	recs, _ := helper.Fetch(context.Background(), 1)
	store := &scriptedStore{pending: recs}

	observed := map[string]string{}
	var mu sync.Mutex
	pub := &fakePublisher{hookEvent: func(pubCtx context.Context, _ domain.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := kafka.TraceIDFrom(pubCtx); ok {
			observed[eventbus.HeaderTraceID] = v
		}
		if v, ok := kafka.CausationIDFrom(pubCtx); ok {
			observed[eventbus.HeaderCausationID] = v
		}
		if v, ok := kafka.CorrelationIDFrom(pubCtx); ok {
			observed[eventbus.HeaderCorrelationID] = v
		}
	}}

	r := mustNewRelay(t, store, pub, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)

	mu.Lock()
	defer mu.Unlock()
	if observed[eventbus.HeaderTraceID] != "trace-1" {
		t.Errorf("trace_id = %q, want trace-1", observed[eventbus.HeaderTraceID])
	}
	if observed[eventbus.HeaderCausationID] != "cause-1" {
		t.Errorf("causation_id = %q, want cause-1", observed[eventbus.HeaderCausationID])
	}
	if observed[eventbus.HeaderCorrelationID] != "corr-1" {
		t.Errorf("correlation_id = %q, want corr-1", observed[eventbus.HeaderCorrelationID])
	}
}

func TestRelay_NoHeaderRestorerByDefault(t *testing.T) {
	codec := makeRegisteredCodec()
	helper, _ := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	ctx := kafka.WithTraceID(context.Background(), "trace-1")
	_ = helper.Stage(ctx, "topic", makeEvent("e1", "test.event.v1", "agg", "Test", "body"))
	recs, _ := helper.Fetch(context.Background(), 1)
	store := &scriptedStore{pending: recs}

	var (
		mu       sync.Mutex
		observed string
	)
	pub := &fakePublisher{hookEvent: func(pubCtx context.Context, _ domain.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := kafka.TraceIDFrom(pubCtx); ok {
			observed = v
		}
	}}
	r := mustNewRelay(t, store, pub, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	mu.Lock()
	defer mu.Unlock()
	if observed != "" {
		t.Errorf("trace_id propagated without restorer: %q", observed)
	}
}

func TestRelay_ArbitraryNonWellKnownHeadersNotPropagated(t *testing.T) {
	codec := makeRegisteredCodec()
	helper, _ := outbox.NewMemory(outbox.MemoryConfig{Codec: codec})
	_ = helper.Stage(context.Background(), "topic", makeEvent("e1", "test.event.v1", "agg", "Test", "body"))
	recs, _ := helper.Fetch(context.Background(), 1)
	recs[0].Headers["my-custom-header"] = "x"
	store := &scriptedStore{pending: recs}

	var (
		mu       sync.Mutex
		traceObs string
	)
	pub := &fakePublisher{hookEvent: func(pubCtx context.Context, _ domain.DomainEvent) {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := kafka.TraceIDFrom(pubCtx); ok {
			traceObs = v
		}
	}}
	r := mustNewRelay(t, store, pub, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	mu.Lock()
	defer mu.Unlock()
	if traceObs != "" {
		t.Errorf("RestoreCoreHeaders promoted a non-well-known header into ctx: trace_id=%q", traceObs)
	}
}

// --- panic recovery -------------------------------------------------

func TestRelay_PublisherPanicIsRecoveredAndRoutedToFail(t *testing.T) {
	codec := makeRegisteredCodec()
	r1 := stageOne(t, codec, "e1")
	r2 := stageOne(t, codec, "e2")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{r1, r2}}
	pub := &fakePublisher{panicMsg: "publisher panic"}

	r := mustNewRelay(t, store, pub, codec,
		outbox.WithMaxAttempts(3),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)

	_, failed, term := store.Counts()
	if failed != 2 || term != 0 {
		t.Fatalf("want 2 MarkFailed (both panicked below MaxAttempts); got failed=%d term=%d", failed, term)
	}
}

func TestRelay_CodecUnmarshalPanicIsRecoveredAndRoutedToFail(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "e1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}

	r := mustNewRelay(t, store, &fakePublisher{}, newPanicOnUnmarshalCodec(),
		outbox.WithMaxAttempts(3),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if _, failed, _ := store.Counts(); failed != 1 {
		t.Fatalf("want one MarkFailed via Unmarshal panic; got failed=%d", failed)
	}
}

func TestRelay_HeaderRestorerPanicIsRecoveredAndRoutedToFail(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "e1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}

	panicker := func(_ context.Context, _ map[string]string) context.Context {
		panic("restorer boom")
	}
	r := mustNewRelay(t, store, &fakePublisher{}, codec,
		outbox.WithMaxAttempts(3),
		outbox.WithHeaderRestorer(panicker),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	runWithOneTick(t, r)
	if _, failed, _ := store.Counts(); failed != 1 {
		t.Fatalf("want one MarkFailed via restorer panic; got failed=%d", failed)
	}
}
