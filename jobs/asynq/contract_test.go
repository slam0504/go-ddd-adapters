//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"
	"github.com/slam0504/go-ddd-core/ports/jobs/jobstest"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// (0) jobstest.RunContract over a real backend. Each subtest gets an isolated
// queue (fresh container is overkill; a per-call unique queue isolates state).
func TestContract_RunContract(t *testing.T) {
	_, addr := startRedisContainer(t)
	var n int
	factory := func(t *testing.T) jobstest.Backend {
		n++
		q := fmt.Sprintf("contract-%d", n)
		e := mustEnqueuer(t, addr, jobsasynq.WithQueue(q))
		w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue(q))
		return jobstest.Backend{Enqueuer: e, Worker: w}
	}
	jobstest.RunContract(t, factory)
}

// (g) malformed Enqueue writes nothing — verified via Inspector queue counts.
func TestContract_MalformedEnqueueWritesNothing(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("g-queue"))

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: ""}); errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("empty type: code = %v", errorsx.CodeOf(err))
	}
	qs, err := insp.GetQueueInfo("g-queue")
	if err == nil && qs.Size != 0 {
		t.Fatalf("queue size = %d after malformed enqueue, want 0", qs.Size)
	}
	// err != nil means the queue does not exist, which is also acceptable evidence
	// nothing was written. GetQueueInfo returns an internal QueueNotFoundError (not
	// asynq.ErrQueueNotFound) for non-existent queues, so any non-nil error here is
	// acceptable.
	_ = err
}

// (m) out-of-horizon ProcessAt rejected at Enqueue, precedence over cancelled ctx.
func TestContract_OutOfHorizonRejected(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("m-queue"))

	far := time.Now().Add(jobsasynq.DefaultSchedulingHorizon + time.Hour)
	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "m:far", ProcessAt: far})
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("far ProcessAt: code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if info.ID != "" {
		t.Fatalf("error carried non-zero JobInfo.ID %q", info.ID)
	}
	// Precedence: out-of-horizon (class 1b) beats a cancelled ctx (class 2a).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = e.Enqueue(ctx, jobs.Job{Type: "m:far", ProcessAt: far})
	if errorsx.CodeOf(err) != errorsx.CodeInvalidArgument {
		t.Fatalf("far + cancelled: code = %v, want CodeInvalidArgument", errorsx.CodeOf(err))
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("far + cancelled returned ctx error; class-1b must win")
	}
}

// (p) past ProcessAt is immediately eligible (accepted, then delivered).
func TestContract_PastProcessAtEligible(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("p-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("p-queue"))

	got := make(chan struct{}, 1)
	if err := w.Register("p:past", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		got <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "p shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "p:past", ProcessAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("past-ProcessAt job was not delivered promptly")
	}
}

// (q) within class 2, ctx is observed before the backend — both ctx-error kinds,
// even when the backend is unavailable (bad addr).
func TestContract_CtxPrecedesBackend(t *testing.T) {
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "127.0.0.1:6390"}) // nothing listening
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Enqueue(cctx, jobs.Job{Type: "q:c"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled + unavailable: err = %v, want Canceled (no backend contact)", err)
	}
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	if _, err := e.Enqueue(dctx, jobs.Job{Type: "q:d"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pre-expired + unavailable: err = %v, want DeadlineExceeded", err)
	}
}

// (o) duplicate Register keeps the original handler: h1 receives, not h2.
func TestContract_DuplicateRegisterKeepsOriginal(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("o-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("o-queue"))

	h1Got := make(chan struct{}, 1)
	h2Got := make(chan struct{}, 1)
	if err := w.Register("o:dup", jobs.HandlerFunc(func(context.Context, jobs.Task) error { h1Got <- struct{}{}; return nil })); err != nil {
		t.Fatalf("Register h1: %v", err)
	}
	if err := w.Register("o:dup", jobs.HandlerFunc(func(context.Context, jobs.Task) error { h2Got <- struct{}{}; return nil })); errorsx.CodeOf(err) != errorsx.CodeAlreadyExists {
		t.Fatalf("duplicate Register: code = %v, want CodeAlreadyExists", errorsx.CodeOf(err))
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "o shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "o:dup"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-h1Got:
	case <-h2Got:
		t.Fatal("h2 received; the original handler must stay installed")
	case <-time.After(30 * time.Second):
		t.Fatal("neither handler received")
	}
}

// (k) exact-type-match dispatch rejects prefixes: a handler for "k:email" must
// NOT receive "k:email:weekly".
func TestContract_ExactTypeMatch(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("k-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("k-queue"),
		jobsasynq.WithMaxRetry(0)) // archive fast; the prefix job is unhandled here

	exact := make(chan string, 1)
	if err := w.Register("k:email", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error { exact <- task.Type; return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "k shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "k:email:weekly"}); err != nil {
		t.Fatalf("Enqueue prefix: %v", err)
	}
	select {
	case got := <-exact:
		t.Fatalf("prefix job dispatched to k:email handler (got %q); exact match required", got)
	case <-time.After(5 * time.Second):
		// good: the prefix job did not match the shorter-typed handler.
	}
}

// (u) unhandled type is never acked as success and follows the documented
// policy (retry → archive). With MaxRetry(0) it lands in archived, never completed.
func TestContract_UnhandledTypeNeverAcked(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("u-queue"), jobsasynq.WithMaxRetry(0))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("u-queue"))
	// Register an UNRELATED type so the worker runs but the enqueued type is unhandled.
	if err := w.Register("u:other", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "u shutdown") }()

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "u:unhandled"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ti, gerr := insp.GetTaskInfo("u-queue", info.ID)
		if gerr == nil {
			if ti.State == asynq.TaskStateCompleted {
				t.Fatal("unhandled type was acked as completed")
			}
			if ti.State == asynq.TaskStateArchived {
				return // documented policy reached: retried (0) then archived
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("unhandled job did not reach archived within bound (last err=%v)", gerr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
