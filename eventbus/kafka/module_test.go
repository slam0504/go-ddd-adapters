package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

var errSubscribeBoom = errors.New("subscribe boom")

// fakeSubscriber lets module tests exercise the consumer lifecycle
// without a real Kafka and without relying on wall-clock timing.
//
// Each successful Subscribe spawns a goroutine that:
//  1. waits for the per-subscribe ctx to be cancelled,
//  2. reports its topic name to cancelObserved (the synchronization
//     point — tests assert here that cancel fanned out to every topic
//     before any drain completed),
//  3. blocks on release until the test allows the output channel to
//     close (which is what lets the consumer goroutine in
//     newConsumerLifecycle exit its for-range and wg.Done).
//
// When failOn is non-empty, Subscribe for that topic returns
// errSubscribeBoom immediately (exercises the partial-Start cleanup
// path).
type fakeSubscriber struct {
	failOn         string
	cancelObserved chan string
	release        chan struct{}
}

func (f *fakeSubscriber) Subscribe(ctx context.Context, topic string) (<-chan eventbus.Envelope, error) {
	if topic == f.failOn {
		return nil, errSubscribeBoom
	}
	out := make(chan eventbus.Envelope)
	go func() {
		<-ctx.Done()
		f.cancelObserved <- topic
		<-f.release
		close(out)
	}()
	return out, nil
}

func (f *fakeSubscriber) Close() error { return nil }

// drainCancels reads exactly n topic reports from cancelObserved or
// fails the test. Reports are returned as a set so callers can assert
// on identity ("all of a, b, c reported") rather than order.
func drainCancels(t *testing.T, ch <-chan string, n int) map[string]bool {
	t.Helper()
	observed := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		select {
		case topic := <-ch:
			if observed[topic] {
				t.Fatalf("duplicate cancel report for %q", topic)
			}
			observed[topic] = true
		case <-time.After(time.Second):
			t.Fatalf("only %d/%d topics observed cancel within 1s: %v", len(observed), n, observed)
		}
	}
	return observed
}

func TestConsumerGroup_StopCancelsAllTopicsAtomically(t *testing.T) {
	topics := []string{"a", "b", "c"}
	sub := &fakeSubscriber{
		cancelObserved: make(chan string, len(topics)),
		release:        make(chan struct{}),
	}
	handle := func(_ context.Context, _ eventbus.Envelope) error { return nil }

	mod := ConsumerGroup(sub, topics, silentLog(), handle)
	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stop will cancel the shared ctx (so every subscribe goroutine
	// reports to cancelObserved) and then block on wg.Wait — which
	// can't progress until the test closes release. Run async so we
	// can assert the in-between state.
	stopErr := make(chan error, 1)
	go func() { stopErr <- mod.Stop(context.Background()) }()

	// The invariant: every topic must observe ctx.Done before any of
	// them is allowed to actually drain. If cancel were serialized
	// through the bootstrap lifecycle (the bug), this read would
	// only see one topic until that topic's drain completed.
	observed := drainCancels(t, sub.cancelObserved, len(topics))
	for _, want := range topics {
		if !observed[want] {
			t.Fatalf("topic %q did not observe cancel; observed=%v", want, observed)
		}
	}

	close(sub.release)

	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not return within 1s after release")
	}
}

func TestConsumerGroup_StartPropagatesSubscribeErrorAndDrains(t *testing.T) {
	sub := &fakeSubscriber{
		failOn:         "c",
		cancelObserved: make(chan string, 2),
		release:        make(chan struct{}),
	}
	handle := func(_ context.Context, _ eventbus.Envelope) error { return nil }

	mod := ConsumerGroup(sub, []string{"a", "b", "c"}, silentLog(), handle)

	// Start runs the Subscribe loop synchronously and, when "c" fails,
	// cancels the shared ctx then wg.Waits for the a + b goroutines to
	// exit. Run async so we can assert "Start is still blocked on
	// drain" before letting it proceed.
	startErr := make(chan error, 1)
	go func() { startErr <- mod.Start(context.Background(), nil) }()

	observed := drainCancels(t, sub.cancelObserved, 2)
	for _, want := range []string{"a", "b"} {
		if !observed[want] {
			t.Fatalf("topic %q did not observe cancel; observed=%v", want, observed)
		}
	}

	// Non-blocking peek: drain hasn't completed (release still open),
	// so Start must not have returned yet. No wall-clock dependency.
	select {
	case err := <-startErr:
		t.Fatalf("Start returned %v before goroutines drained — would leak workers", err)
	default:
	}

	close(sub.release)

	select {
	case err := <-startErr:
		if !errors.Is(err, errSubscribeBoom) {
			t.Fatalf("Start err = %v, want errSubscribeBoom", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return within 1s after release")
	}
}
