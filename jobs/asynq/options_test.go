package jobsasynq

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
)

// okConnOpt is a shape-valid RedisConnOpt that never connects (go-redis lazy).
var okConnOpt = asynq.RedisClientOpt{Addr: "127.0.0.1:6379"}

// badConnOpt returns a non-UniversalClient to exercise the shape guard.
type badConnOpt struct{}

func (badConnOpt) MakeRedisClient() interface{} { return "not a client" }

func TestNewEnqueuer_NilConnOpt(t *testing.T) {
	if _, err := NewEnqueuer(nil); !errors.Is(err, ErrNilRedisConnOpt) {
		t.Fatalf("nil conn opt: err = %v, want ErrNilRedisConnOpt", err)
	}
}

func TestNewWorker_BadConnOptShape(t *testing.T) {
	if _, err := NewWorker(badConnOpt{}); !errors.Is(err, ErrInvalidRedisConnOpt) {
		t.Fatalf("bad conn opt: err = %v, want ErrInvalidRedisConnOpt", err)
	}
}

func TestNewEnqueuer_OptionValidation(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		want error
	}{
		{"blank queue", WithQueue("  "), ErrEmptyQueue},
		{"non-positive horizon", WithSchedulingHorizon(0), ErrSchedulingHorizonNonPos},
		{"sub-second retention", WithRetention(999 * time.Millisecond), ErrRetentionTooSmall},
		{"negative max retry", WithMaxRetry(-1), ErrMaxRetryNegative},
		{"non-positive task timeout", WithTaskTimeout(0), ErrTaskTimeoutNonPos},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEnqueuer(okConnOpt, tc.opt); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewWorker_OptionValidation(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		want error
	}{
		{"non-positive concurrency", WithConcurrency(0), ErrConcurrencyNonPos},
		{"non-positive shutdown timeout", WithShutdownTimeout(0), ErrShutdownTimeoutNonPos},
		{"nil retry delay", WithRetryDelay(nil), ErrNilRetryDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWorker(okConnOpt, tc.opt); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewEnqueuer_Defaults(t *testing.T) {
	e, err := NewEnqueuer(okConnOpt)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if e.schedulingHorizon != DefaultSchedulingHorizon {
		t.Fatalf("horizon = %v, want %v", e.schedulingHorizon, DefaultSchedulingHorizon)
	}
	if e.retention != DefaultRetention {
		t.Fatalf("retention = %v, want %v", e.retention, DefaultRetention)
	}
}

func TestSnapshot_IsolatesCaller(t *testing.T) {
	src := []byte("abc")
	got := snapshot(src)
	src[0] = 'X'
	if string(got) != "abc" {
		t.Fatalf("snapshot not isolated: got %q", got)
	}
	if got := snapshot(nil); len(got) != 0 {
		t.Fatalf("snapshot(nil) len = %d, want 0", len(got))
	}
}

func TestClassifyBackendErr(t *testing.T) {
	netErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if got := classifyBackendErr(netErr); got != errorsx.CodeUnavailable {
		t.Fatalf("net.Error → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("connection refused by host")); got != errorsx.CodeUnavailable {
		t.Fatalf("connection-refused string → %v, want CodeUnavailable", got)
	}
	if got := classifyBackendErr(errors.New("some weird logical failure")); got != errorsx.CodeInternal {
		t.Fatalf("unclassifiable → %v, want CodeInternal (never CodeUnknown)", got)
	}
	// guard: classify never yields CodeUnknown.
	if got := classifyBackendErr(goredis.Nil); got == errorsx.CodeUnknown {
		t.Fatal("classify yielded CodeUnknown")
	}
}
