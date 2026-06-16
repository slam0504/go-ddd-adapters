//go:build integration

package jobsasynq_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/slam0504/go-ddd-core/pkg/errorsx"
	"github.com/slam0504/go-ddd-core/ports/jobs"

	jobsasynq "github.com/slam0504/go-ddd-adapters/jobs/asynq"
)

const shutTO = 2 * time.Second // configured ShutdownTimeout for shutdown tests

// (f) unreachable backend at Enqueue → CodeUnavailable (and never CodeUnknown).
func TestShutdown_Enqueue_UnreachableBackend(t *testing.T) {
	e, err := jobsasynq.NewEnqueuer(asynq.RedisClientOpt{Addr: "127.0.0.1:6391"}) // nothing listening
	if err != nil {
		t.Fatalf("NewEnqueuer: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	_, err = e.Enqueue(context.Background(), jobs.Job{Type: "f:job"})
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("unreachable backend: code = %v, want CodeUnavailable", code)
	}
	if errorsx.CodeOf(err) == errorsx.CodeUnknown {
		t.Fatal("backend failure yielded CodeUnknown")
	}
}

// (h) fatal startup (unreachable backend) → coded errorsx from Run, not a ctx
// error, not nil.
func TestShutdown_Run_FatalStartupUnreachable(t *testing.T) {
	w := mustWorker(t, asynq.RedisClientOpt{Addr: "127.0.0.1:6392"}) // nothing listening
	if err := w.Register("h:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error { return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil for an unreachable backend; want a coded fatal")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned a ctx error %v; a fatal must be a coded errorsx", err)
	}
	if code := errorsx.CodeOf(err); code != errorsx.CodeUnavailable {
		t.Fatalf("fatal code = %v, want CodeUnavailable", code)
	}
}

// (j) handler ctx is cancelled when Run is cancelled (via BaseContext).
func TestShutdown_HandlerCtxCancelledOnRunCancel(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("j-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("j-queue"),
		jobsasynq.WithShutdownTimeout(shutTO))

	inHandler := make(chan struct{}, 1)
	ctxErr := make(chan error, 1)
	if err := w.Register("j:wait", jobs.HandlerFunc(func(hctx context.Context, _ jobs.Task) error {
		inHandler <- struct{}{}
		<-hctx.Done() // observes cancellation
		ctxErr <- hctx.Err()
		return hctx.Err()
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "j:wait"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-inHandler:
	case <-time.After(30 * time.Second):
		t.Fatal("handler never started")
	}
	cancel() // cancelling Run must cancel the handler ctx
	select {
	case err := <-ctxErr:
		if err == nil {
			t.Fatal("handler ctx was not cancelled on Run cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler ctx never observed cancellation")
	}
	assertRunNilWithin(t, done, w.ShutdownWithin(), "j shutdown")
}

// (l) concurrent Enqueue is clean under -race and the two Run endpoints hold.
func TestShutdown_ConcurrentEnqueueAndRunEndpoints(t *testing.T) {
	_, addr := startRedisContainer(t)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("l-queue"))
	w := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, jobsasynq.WithQueue("l-queue"),
		jobsasynq.WithShutdownTimeout(shutTO), jobsasynq.WithConcurrency(8))

	var processed atomic.Int32
	if err := w.Register("l:job", jobs.HandlerFunc(func(context.Context, jobs.Task) error {
		processed.Add(1)
		return nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cancel, done := runWorker(t, w)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = e.Enqueue(context.Background(), jobs.Job{Type: "l:job", Payload: []byte(fmt.Sprintf("%d", n))})
		}(i)
	}
	wg.Wait()
	time.Sleep(2 * time.Second) // let some drain
	// Endpoint A: cancellation returns nil within the declared bound.
	cancel()
	assertRunNilWithin(t, done, w.ShutdownWithin(), "l shutdown")
}

// (t) accepted-but-ack-lost fault: an Enqueue that errors mid-flight follows
// class-2 rules (ctx error OR coded non-Unknown) + zero JobInfo, and caller
// mutation after the error does not corrupt an accepted job's payload
// (snapshot-before-submit). Modeled by a deadline that trips during the backend
// call against a reachable server.
func TestShutdown_AcceptedButAckLost_SnapshotIsolation(t *testing.T) {
	_, addr := startRedisContainer(t)
	insp := newInspector(t, addr)
	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("t-queue"))

	// A very short deadline may surface a ctx error from EnqueueContext even if
	// the backend accepted the write — the class-2 indeterminate case.
	payload := []byte("origt")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	info, err := e.Enqueue(ctx, jobs.Job{Type: "t:job", Payload: payload})
	// Mutate the caller's slice immediately after the call returns.
	for i := range payload {
		payload[i] = 'Z'
	}
	if err == nil {
		// Backend won the race: accepted. JobInfo is valid; the stored payload
		// must still be "origt" (snapshot isolated it from the mutation above).
		ti, gerr := insp.GetTaskInfo("t-queue", info.ID)
		if gerr != nil {
			t.Fatalf("accepted job not found: %v", gerr)
		}
		if string(ti.Payload) != "origt" {
			t.Fatalf("stored payload = %q, want \"origt\" (snapshot must isolate caller mutation)", ti.Payload)
		}
		return
	}
	// Error path: class-2 — ctx error OR coded non-Unknown; zero JobInfo.
	if !errors.Is(err, context.DeadlineExceeded) && errorsx.CodeOf(err) == errorsx.CodeUnknown {
		t.Fatalf("error neither ctx nor coded non-Unknown: %v", err)
	}
	if info.ID != "" {
		t.Fatalf("error carried non-zero JobInfo.ID %q", info.ID)
	}
}

// --- ported spike tests (constructors now return error) ---

// (c)+(s2): a stuck handler that ignores its cancelled ctx must not keep Run
// from returning; the un-acked task is actually redelivered by a fresh Worker.
func TestShutdown_StuckHandler_RunNil_NewWorkerRedelivers(t *testing.T) {
	_, addr := startRedisContainer(t)
	opts := []jobsasynq.Option{jobsasynq.WithQueue("c-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO)}

	var attempts atomic.Int32
	started := make(chan struct{}, 1)
	redelivered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := jobs.HandlerFunc(func(_ context.Context, _ jobs.Task) error {
		if attempts.Add(1) == 1 {
			started <- struct{}{}
			<-release // deliberately ignores ctx: the stuck straggler
			return nil
		}
		redelivered <- struct{}{}
		return nil
	})
	t.Cleanup(func() { close(release) })

	e := mustEnqueuer(t, addr, opts...)
	if _, err := e.Enqueue(context.Background(), jobs.Job{Type: "c:stuck", Payload: []byte("x")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
	if err := w1.Register("c:stuck", handler); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("first delivery did not happen")
	}
	cancel1()
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "stuck-handler shutdown")

	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
	if err := w2.Register("c:stuck", handler); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	select {
	case <-redelivered:
	case <-time.After(jobsasynq.RedeliverWithin):
		t.Fatalf("task not redelivered within RedeliverWithin %v", jobsasynq.RedeliverWithin)
	}
	cancel2()
	assertRunNilWithin(t, done2, w2.ShutdownWithin(), "second worker shutdown")
}

// (s1)+(s2)+(c teardown-failure variant): Redis taken down DURING shutdown —
// Run still returns nil, the requeue path provably hit the outage, and the job
// recovers (completed or actually redelivered) after restart.
func TestShutdown_RedisDownDuringShutdown_RunNil_JobRecovers(t *testing.T) {
	container, addr := startRedisContainer(t)
	hook := &recorderHook{}
	logger := &captureLogger{}
	workerOpts := []jobsasynq.Option{jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO), jobsasynq.WithLogger(logger)}

	var attempts atomic.Int32
	started := make(chan struct{}, 1)
	redelivered := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := jobs.HandlerFunc(func(_ context.Context, _ jobs.Task) error {
		if attempts.Add(1) == 1 {
			started <- struct{}{}
			<-release
			return nil
		}
		redelivered <- struct{}{}
		return nil
	})
	t.Cleanup(func() { close(release) })

	e := mustEnqueuer(t, addr, jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10*time.Minute))
	info, err := e.Enqueue(context.Background(), jobs.Job{Type: "s:outage", Payload: []byte("x")})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	w1 := mustWorker(t, hookedConnOpt{addr: addr, hook: hook}, workerOpts...)
	if err := w1.Register("s:outage", handler); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	cancel1, done1 := runWorker(t, w1)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("first delivery did not happen")
	}

	hook.armed.Store(true)
	logger.armed.Store(true)
	stopTimeout := 10 * time.Second
	if err := container.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stopping redis: %v", err)
	}
	cancel1()
	assertRunNilWithin(t, done1, w1.ShutdownWithin(), "shutdown during outage")
	hook.armed.Store(false)
	logger.armed.Store(false)

	if len(hook.failedTouchingActive()) == 0 {
		t.Fatalf("no failed redis command touching :active during outage; failures: %v", hook.failed)
	}
	if len(logger.matching("requeue", "back to queue", "push task", "move", "could not move", "retry")) == 0 {
		t.Fatalf("no asynq requeue-path error logged; entries: %v", logger.entries)
	}

	if err := container.Start(context.Background()); err != nil {
		t.Fatalf("restarting redis: %v", err)
	}
	addr2, err := container.Endpoint(context.Background(), "")
	if err != nil {
		t.Fatalf("re-resolving endpoint: %v", err)
	}
	insp := newInspector(t, addr2)
	w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr2}, jobsasynq.WithQueue("s-queue"), jobsasynq.WithRetention(10*time.Minute), jobsasynq.WithShutdownTimeout(shutTO))
	if err := w2.Register("s:outage", handler); err != nil {
		t.Fatalf("Register w2: %v", err)
	}
	cancel2, done2 := runWorker(t, w2)
	defer func() { cancel2(); assertRunNilWithin(t, done2, w2.ShutdownWithin(), "recovery shutdown") }()

	deadline := time.Now().Add(jobsasynq.RecoverWithin)
	for {
		select {
		case <-redelivered:
			return // retryable branch proven
		default:
		}
		switch classifyJob(t, insp, "s-queue", info.ID) {
		case stateCompleted:
			select {
			case <-redelivered:
				t.Fatal("job completed but was also redelivered")
			case <-time.After(5 * time.Second):
				return
			}
		case stateLostDiscarded:
			t.Fatalf("job %s lost (no completion evidence)", info.ID)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not resolve within RecoverWithin %v", jobsasynq.RecoverWithin)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// (s1)+(s2) ack/shutdown race ×N: every job ends completed or retryable —
// never lost, never stuck active beyond the declared bound.
func TestShutdown_AckShutdownRace_CompletedOrRetryable(t *testing.T) {
	_, addr := startRedisContainer(t)
	opts := []jobsasynq.Option{jobsasynq.WithQueue("race-queue"), jobsasynq.WithRetention(10 * time.Minute), jobsasynq.WithShutdownTimeout(shutTO)}
	e := mustEnqueuer(t, addr, opts...)
	insp := newInspector(t, addr)

	const iterations = 20
	for i := 0; i < iterations; i++ {
		jobType := fmt.Sprintf("race:%d", i)
		inHandler := make(chan struct{}, 1)
		var deliveries atomic.Int32
		handler := jobs.HandlerFunc(func(context.Context, jobs.Task) error {
			deliveries.Add(1)
			select {
			case inHandler <- struct{}{}:
			default:
			}
			return nil // immediate ack, racing the shutdown
		})
		w1 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
		if err := w1.Register(jobType, handler); err != nil {
			t.Fatalf("iter %d Register: %v", i, err)
		}
		cancel1, done1 := runWorker(t, w1)
		info, err := e.Enqueue(context.Background(), jobs.Job{Type: jobType, Payload: []byte("x")})
		if err != nil {
			t.Fatalf("iter %d Enqueue: %v", i, err)
		}
		select {
		case <-inHandler:
		case <-time.After(30 * time.Second):
			t.Fatalf("iter %d: delivery did not happen", i)
		}
		cancel1()
		assertRunNilWithin(t, done1, w1.ShutdownWithin(), fmt.Sprintf("iter %d shutdown", i))

		if classifyJob(t, insp, "race-queue", info.ID) == stateCompleted {
			continue
		}
		w2 := mustWorker(t, asynq.RedisClientOpt{Addr: addr}, opts...)
		if err := w2.Register(jobType, handler); err != nil {
			t.Fatalf("iter %d Register w2: %v", i, err)
		}
		cancel2, done2 := runWorker(t, w2)
		deadline := time.Now().Add(jobsasynq.RecoverWithin)
		resolved := false
		for !resolved {
			switch classifyJob(t, insp, "race-queue", info.ID) {
			case stateCompleted:
				resolved = true
			case stateLostDiscarded:
				t.Fatalf("iter %d: job %s lost", i, info.ID)
			}
			if !resolved && deliveries.Load() >= 2 {
				resolved = true // actual redelivery observed
			}
			if !resolved {
				if time.Now().After(deadline) {
					t.Fatalf("iter %d: job stuck beyond RecoverWithin %v", i, jobsasynq.RecoverWithin)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		cancel2()
		assertRunNilWithin(t, done2, w2.ShutdownWithin(), fmt.Sprintf("iter %d recovery shutdown", i))
	}
}
