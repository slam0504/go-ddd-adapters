//go:build integration

package jobsasynq_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/ports/jobs"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

// (a) at-least-once redelivery incl. concurrent-duplicate tolerance. A handler
// that fails its first attempt is redelivered; the second attempt succeeds.
func TestDelivery_AtLeastOnceRedelivery(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("a-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("a-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	succeeded := make(chan struct{}, 1)
	if err := w.Register("a:retry", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		if attempts.Add(1) == 1 {
			return errTransient
		}
		succeeded <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "a shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "a:retry"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-succeeded:
		if got := attempts.Load(); got < 2 {
			t.Fatalf("succeeded after %d attempts, want >= 2 (redelivery)", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("job was not redelivered+succeeded within bound")
	}
}

var errTransient = &transientErr{}

type transientErr struct{}

func (*transientErr) Error() string { return "transient" }

// (b) dispatch not before ProcessAt on the backend's own scheduling clock.
// Enqueue with ProcessAt = now+3s; assert the handler does not fire before then.
func TestDelivery_NotBeforeProcessAt(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("b-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("b-queue"))

	const delay = 3 * time.Second
	processAt := time.Now().Add(delay)
	fired := make(chan time.Time, 1)
	if err := w.Register("b:sched", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		fired <- time.Now()
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "b shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "b:sched", ProcessAt: processAt}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case at := <-fired:
		if at.Before(processAt) {
			t.Fatalf("dispatched at %v, before ProcessAt %v", at, processAt)
		}
	case <-time.After(delay + 30*time.Second):
		t.Fatal("scheduled job never fired")
	}
}

// (d) handler payload mutation does not pollute redelivery: attempt 1 mutates
// its copy then fails; attempt 2 must see the original bytes.
func TestDelivery_PayloadMutationIsolated(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("d-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("d-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	secondSaw := make(chan string, 1)
	if err := w.Register("d:mut", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error {
		if attempts.Add(1) == 1 {
			for i := range task.Payload {
				task.Payload[i] = 'X' // mutate the private copy
			}
			return errTransient
		}
		secondSaw <- string(task.Payload)
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "d shutdown") }()

	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "d:mut", Payload: []byte("orig")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case got := <-secondSaw:
		if got != "orig" {
			t.Fatalf("redelivery saw mutated payload %q, want \"orig\"", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no redelivery")
	}
}

// (e) ID stable across redeliveries: Task.ID equals the JobInfo.ID on every
// delivery.
func TestDelivery_IDStableAcrossRedeliveries(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("e-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("e-queue"),
		jobsasynq.WithMaxRetry(3),
		jobsasynq.WithRetryDelay(func(int, error, *asynq.Task) time.Duration { return 200 * time.Millisecond }))

	var attempts atomic.Int32
	var mu sync.Mutex
	var seen []string
	twice := make(chan struct{}, 1)
	if err := w.Register("e:id", jobs.HandlerFunc(func(_ context.Context, task jobs.Task) error {
		mu.Lock()
		seen = append(seen, task.ID)
		mu.Unlock()
		if attempts.Add(1) == 1 {
			return errTransient
		}
		twice <- struct{}{}
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	defer func() { cancel(); assertRunNilWithin(t, done, w.ShutdownWithin(), "e shutdown") }()

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "e:id"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-twice:
	case <-time.After(30 * time.Second):
		t.Fatal("no redelivery")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, id := range seen {
		if id != info.ID {
			t.Fatalf("delivery %d had ID %q, want stable %q", i, id, info.ID)
		}
	}
}

// (n) an accepted scheduled job is retained past ProcessAt with NO worker
// running (retain-until-dequeue). Enqueue ProcessAt=now+2s, run no worker, wait
// past it, assert the job is still present (scheduled/pending), not expired.
func TestDelivery_ScheduledRetainedWithoutWorker(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("n-queue"))

	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "n:sched", ProcessAt: time.Now().Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	time.Sleep(6 * time.Second) // well past ProcessAt; still no worker
	ti, err := insp.GetTaskInfo("n-queue", info.ID)
	if err != nil {
		t.Fatalf("job vanished without a worker (want retained): %v", err)
	}
	switch ti.State {
	case asynq.TaskStatePending, asynq.TaskStateScheduled:
		// good: still deliverable, never expired pre-dequeue.
	default:
		t.Fatalf("job state = %v past ProcessAt with no worker; want pending/scheduled", ti.State)
	}
}

// (r) a worker that stops before dequeue does not consume the job; a NEW Worker
// instance over the same store delivers it (Run is once-per-instance).
func TestDelivery_NewWorkerDeliversAfterStop(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("r-queue"))

	// Enqueue first, then start w1 but cancel it immediately so it has no chance
	// to dequeue (ProcessAt slightly in the future guarantees it stays pending).
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "r:job", ProcessAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("r-queue"))
	if err := w1.Register("r:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	cancel1() // stop before the scheduled job becomes eligible
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "r w1 shutdown")

	// Fresh instance delivers it.
	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("r-queue"))
	got := make(chan struct{}, 1)
	if err := w2.Register("r:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { got <- struct{}{}; return nil })); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	defer func() { cancel2(); assertRunNilWithin(t, done2, w2.ShutdownWithin(), "r w2 shutdown") }()
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("new Worker did not deliver the job left by the stopped worker")
	}
}
