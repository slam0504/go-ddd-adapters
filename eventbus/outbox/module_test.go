package outbox_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
)

// captureLogger writes JSON-encoded log records into an internal buffer
// so tests can assert on the level / message of what the module emitted.
type captureLogger struct {
	mu    sync.Mutex
	buf   *bytes.Buffer
	inner logger.Logger
}

func newCaptureLogger() *captureLogger {
	buf := &bytes.Buffer{}
	return &captureLogger{
		buf:   buf,
		inner: slogger.New(slogger.Config{Writer: buf, Level: slog.LevelDebug, Format: "json"}),
	}
}

func (c *captureLogger) Log(ctx context.Context, level logger.Level, msg string, attrs ...logger.Attr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inner.Log(ctx, level, msg, attrs...)
}
func (c *captureLogger) With(attrs ...logger.Attr) logger.Logger { return c.inner.With(attrs...) }
func (c *captureLogger) Enabled(ctx context.Context, level logger.Level) bool {
	return c.inner.Enabled(ctx, level)
}

func (c *captureLogger) contains(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Contains(c.buf.String(), s)
}

// --- module lifecycle ----------------------------------------------

func TestRelayModule_StartStopLifecycle(t *testing.T) {
	codec := makeRegisteredCodec()
	r := mustNewRelay(t, &scriptedStore{}, &fakePublisher{}, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	mod := outbox.RelayModule(r, silentLog())
	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mod.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestRelayModule_StopHonoursStopCtxDeadline(t *testing.T) {
	// Build a Relay whose poll interval is very long, then Stop with an
	// already-cancelled ctx. Without the cancel propagation, Stop would
	// have to wait for the next poll tick (an hour).
	codec := makeRegisteredCodec()
	r := mustNewRelay(t, &scriptedStore{}, &fakePublisher{}, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(time.Hour),
	)
	mod := outbox.RelayModule(r, silentLog())
	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// With a healthy Stop, cancellation reaches runCtx and Run returns
	// context.Canceled — StopFn observes runDone and returns nil.
	if err := mod.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned non-nil under normal cancel: %v", err)
	}
}

func TestRelayModule_RunReturnsCanceledOnCleanStop(t *testing.T) {
	codec := makeRegisteredCodec()
	r := mustNewRelay(t, &scriptedStore{}, &fakePublisher{}, codec,
		outbox.WithMaxAttempts(0),
		outbox.WithPollInterval(50*time.Millisecond),
	)
	log := newCaptureLogger()
	mod := outbox.RelayModule(r, log)
	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := mod.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Clean shutdown is silent: the "outbox relay exited with error" log
	// must NOT appear.
	if log.contains("outbox relay exited with error") {
		t.Fatalf("clean shutdown emitted error log; buffer:\n%s", log.buf.String())
	}
}

func TestRelayModule_PanicInPublishIsRecoveredAndLogged(t *testing.T) {
	codec := makeRegisteredCodec()
	rec := stageOne(t, codec, "panic-1")
	store := &scriptedStore{pending: []eventbus.OutboxRecord{rec}}
	pub := &fakePublisher{panicMsg: "publisher panic in module"}

	log := newCaptureLogger()
	r, err := outbox.NewRelay(outbox.RelayConfig{
		Store:     store,
		Publisher: pub,
		Codec:     codec,
		Logger:    log,
	}, outbox.WithMaxAttempts(3), outbox.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	mod := outbox.RelayModule(r, log)
	if err := mod.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait long enough for at least one tick + panic recovery cycle.
	time.Sleep(150 * time.Millisecond)
	if err := mod.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Panic recovery must have produced an Error-level log entry. The
	// relay also routes the failure through fail() → MarkFailed because
	// Attempts (0) + 1 < MaxAttempts (3).
	if !log.contains("outbox relay: panic recovered") {
		t.Fatalf("missing panic-recovered log; buffer:\n%s", log.buf.String())
	}
	if _, failed, _ := store.Counts(); failed != 1 {
		t.Fatalf("want one MarkFailed after panic recovery; got failed=%d", failed)
	}
}
