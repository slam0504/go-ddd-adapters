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
