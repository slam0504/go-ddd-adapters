package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
)

// silentLog discards log output so test failures aren't drowned in
// expected error/panic lines. Tests assert on Ack/Nack channels instead.
func silentLog() logger.Logger {
	return slogger.New(slogger.Config{Writer: io.Discard, Level: slog.LevelError + 1})
}

func newEnvelopeWithRaw(name string) eventbus.Envelope {
	return eventbus.Envelope{
		Name: name,
		Raw:  message.NewMessage("test-id", message.Payload(nil)),
	}
}

// waitOn returns true if c closed within timeout.
func waitOn(c <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-c:
		return true
	case <-time.After(timeout):
		return false
	}
}

func TestProcessEnvelope_NilErrorAcksMessage(t *testing.T) {
	env := newEnvelopeWithRaw("evt.ok")
	handle := func(_ context.Context, _ eventbus.Envelope) error { return nil }

	processEnvelope(context.Background(), silentLog(), "test", handle, env)

	if !waitOn(env.Raw.Acked(), 100*time.Millisecond) {
		t.Fatal("expected Ack within 100ms, got none")
	}
}

func TestProcessEnvelope_ErrorNacksMessage(t *testing.T) {
	env := newEnvelopeWithRaw("evt.fail")
	handle := func(_ context.Context, _ eventbus.Envelope) error {
		return errors.New("boom")
	}

	processEnvelope(context.Background(), silentLog(), "test", handle, env)

	if !waitOn(env.Raw.Nacked(), 100*time.Millisecond) {
		t.Fatal("expected Nack within 100ms, got none")
	}
}

func TestProcessEnvelope_PanicIsRecoveredAndNacked(t *testing.T) {
	env := newEnvelopeWithRaw("evt.panic")
	handle := func(_ context.Context, _ eventbus.Envelope) error {
		panic("kaboom")
	}

	// processEnvelope must not propagate the panic; if it does, the
	// test deferred recover catches it and fails.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("processEnvelope leaked panic: %v", r)
		}
	}()
	processEnvelope(context.Background(), silentLog(), "test", handle, env)

	if !waitOn(env.Raw.Nacked(), 100*time.Millisecond) {
		t.Fatal("expected Nack on panic within 100ms, got none")
	}
}

// fakeSubscriber lets module tests exercise the consumer lifecycle
// without a real Kafka. Each Subscribe call:
//   - records the per-call ctx so tests can observe cancel propagation,
//   - returns a channel that stays open until ctx is cancelled, then
//     blocks drainDelay before closing (simulating in-flight handlers
//     that take time to wind down).
//
// The drainDelay is what makes the concurrent-vs-serial Stop test
// discriminating: with N topics serialized through the bootstrap
// lifecycle the total Stop time would be N×drainDelay; with the fix
// (single Module fanning cancel out to all topics at once) it's ~1×.
type fakeSubscriber struct {
	drainDelay time.Duration
	mu         sync.Mutex
	contexts   []context.Context
}

func (f *fakeSubscriber) Subscribe(ctx context.Context, _ string) (<-chan eventbus.Envelope, error) {
	f.mu.Lock()
	f.contexts = append(f.contexts, ctx)
	f.mu.Unlock()
	out := make(chan eventbus.Envelope)
	go func() {
		<-ctx.Done()
		time.Sleep(f.drainDelay)
		close(out)
	}()
	return out, nil
}

func (f *fakeSubscriber) Close() error { return nil }

func (f *fakeSubscriber) subscribedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.contexts)
}

func TestConsumerGroup_StopDrainsTopicsConcurrently(t *testing.T) {
	const drainDelay = 50 * time.Millisecond
	sub := &fakeSubscriber{drainDelay: drainDelay}
	handle := func(_ context.Context, _ eventbus.Envelope) error { return nil }

	mod := ConsumerGroup(sub, []string{"a", "b", "c"}, silentLog(), handle)

	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := sub.subscribedCount(); got != 3 {
		t.Fatalf("subscribed topics = %d, want 3", got)
	}

	start := time.Now()
	if err := mod.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)

	// Concurrent fan-out drain ≈ 1×drainDelay; the broken serial shape
	// would be 3×drainDelay = 150ms. Threshold at 1.5× catches the
	// regression while leaving room for scheduler jitter.
	if max := drainDelay * 3 / 2; elapsed > max {
		t.Fatalf("Stop took %v, want < %v (drain should be concurrent)", elapsed, max)
	}
}

var errSubscribeBoom = errors.New("subscribe boom")

// flakySubscriber succeeds for every topic except failOn, where it
// returns errSubscribeBoom. Used to verify that ConsumerGroup tears
// down already-spawned goroutines when a later Subscribe fails.
type flakySubscriber struct {
	failOn     string
	drainDelay time.Duration
}

func (f *flakySubscriber) Subscribe(ctx context.Context, topic string) (<-chan eventbus.Envelope, error) {
	if topic == f.failOn {
		return nil, errSubscribeBoom
	}
	out := make(chan eventbus.Envelope)
	go func() {
		<-ctx.Done()
		time.Sleep(f.drainDelay)
		close(out)
	}()
	return out, nil
}

func (f *flakySubscriber) Close() error { return nil }

func TestConsumerGroup_StartPropagatesSubscribeErrorAndDrains(t *testing.T) {
	const drainDelay = 30 * time.Millisecond
	sub := &flakySubscriber{failOn: "c", drainDelay: drainDelay}
	handle := func(_ context.Context, _ eventbus.Envelope) error { return nil }

	mod := ConsumerGroup(sub, []string{"a", "b", "c"}, silentLog(), handle)

	start := time.Now()
	err := mod.Start(context.Background(), nil)
	elapsed := time.Since(start)

	if !errors.Is(err, errSubscribeBoom) {
		t.Fatalf("Start err = %v, want errSubscribeBoom", err)
	}
	// Start must wait for already-spawned goroutines to drain before
	// returning so the caller isn't left with leaked workers. Concurrent
	// drain of two surviving topics ≈ 1×drainDelay.
	if min := drainDelay; elapsed < min {
		t.Fatalf("Start returned in %v, want >= %v (must drain spawned goroutines)", elapsed, min)
	}
	if max := drainDelay * 3; elapsed > max {
		t.Fatalf("Start took %v, want < %v (drain should be concurrent)", elapsed, max)
	}
}
