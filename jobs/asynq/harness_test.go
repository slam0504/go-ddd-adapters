//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// startRedisContainer starts a real Redis. Per the merge gate, Docker being
// unavailable is a FAILURE, never a skip.
func startRedisContainer(t *testing.T) (testcontainers.Container, string) {
	t.Helper()
	ctx := context.Background()
	rc, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("testcontainers redis (Docker unavailable counts as gate failure, not skip): %v", err)
	}
	t.Cleanup(func() { _ = rc.Terminate(context.Background()) })
	addr, err := rc.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("redis endpoint: %v", err)
	}
	return rc, addr
}

func mustEnqueuer(t *testing.T, addr string, opts ...jobsasynq.Option) *jobsasynq.Enqueuer {
	t.Helper()
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: addr}, opts...)
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func mustWorker(t *testing.T, r asynq.RedisConnOpt, opts ...jobsasynq.Option) *jobsasynq.Worker {
	t.Helper()
	w, err := jobsasynq.NewWorker(r, opts...)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

func runWorker(t *testing.T, w *jobsasynq.Worker) (cancel func(), done chan error) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return c, done
}

func assertRunNilWithin(t *testing.T, done chan error, bound time.Duration, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: Run returned %v, want nil", what, err)
		}
	case <-time.After(bound):
		t.Fatalf("%s: Run did not return within declared bound %v", what, bound)
	}
}

// JobState classification for criterion (s).
type jobState int

const (
	stateCompleted        jobState = iota
	statePendingRetryable jobState = iota
	stateActiveLeased     jobState = iota
	stateLostDiscarded    jobState = iota
	stateOther            jobState = iota
)

func classifyJob(t *testing.T, insp *asynq.Inspector, queue, id string) jobState {
	t.Helper()
	info, err := insp.GetTaskInfo(queue, id)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskNotFound) {
			return stateLostDiscarded // no completion evidence (retention gives it otherwise)
		}
		t.Fatalf("GetTaskInfo: %v", err)
	}
	switch info.State {
	case asynq.TaskStateCompleted:
		return stateCompleted
	case asynq.TaskStatePending, asynq.TaskStateRetry, asynq.TaskStateScheduled:
		return statePendingRetryable
	case asynq.TaskStateActive:
		return stateActiveLeased
	default:
		return stateOther
	}
}

// recorderHook records failed redis commands while armed (criterion-2 fault
// evidence). redis.Nil is a reply, not a failure.
type recorderHook struct {
	armed  atomic.Bool
	mu     sync.Mutex
	failed []string
}

func (h *recorderHook) record(cmd goredis.Cmder, err error) {
	if err == nil || errors.Is(err, goredis.Nil) || !h.armed.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failed = append(h.failed, fmt.Sprint(cmd.Args()))
}
func (h *recorderHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}
func (h *recorderHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		err := next(ctx, cmd)
		h.record(cmd, err)
		return err
	}
}
func (h *recorderHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		err := next(ctx, cmds)
		if err != nil {
			for _, cmd := range cmds {
				h.record(cmd, err)
			}
		}
		return err
	}
}
func (h *recorderHook) failedTouchingActive() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, s := range h.failed {
		if strings.Contains(s, ":active") {
			out = append(out, s)
		}
	}
	return out
}

// hookedConnOpt is a custom RedisConnOpt whose client carries a hook.
type hookedConnOpt struct {
	addr string
	hook goredis.Hook
}

func (o hookedConnOpt) MakeRedisClient() interface{} {
	c := goredis.NewClient(&goredis.Options{Addr: o.addr})
	c.AddHook(o.hook)
	return c
}

// captureLogger records asynq's internal log lines while armed.
type captureLogger struct {
	armed   atomic.Bool
	mu      sync.Mutex
	entries []string
}

func (l *captureLogger) log(args ...interface{}) {
	if !l.armed.Load() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, fmt.Sprint(args...))
}
func (l *captureLogger) Debug(a ...interface{}) { l.log(a...) }
func (l *captureLogger) Info(a ...interface{})  { l.log(a...) }
func (l *captureLogger) Warn(a ...interface{})  { l.log(a...) }
func (l *captureLogger) Error(a ...interface{}) { l.log(a...) }
func (l *captureLogger) Fatal(a ...interface{}) { l.log(a...) }
func (l *captureLogger) matching(subs ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.entries {
		low := strings.ToLower(e)
		for _, s := range subs {
			if strings.Contains(low, s) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// newInspector builds an Inspector for a plain addr.
func newInspector(t *testing.T, addr string) *asynq.Inspector {
	t.Helper()
	insp := asynq.NewInspector(asynq.RedisClientOpt{Addr: addr})
	t.Cleanup(func() { _ = insp.Close() })
	return insp
}
