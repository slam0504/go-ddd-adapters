//go:build integration

package pgxoutbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// --- fixtures --------------------------------------------------------

type testEvent struct {
	domain.BaseEvent
	Body string `json:"body"`
}

func makeEvent(id, name, aggID, aggType, body string) *testEvent {
	base := domain.NewBaseEvent(id, name, aggID, aggType, 1)
	base.At = time.Unix(0, 0).UTC()
	return &testEvent{BaseEvent: base, Body: body}
}

// jsonCodec is the same minimal Codec used by the memory adapter tests
// — it sets metadata headers from the DomainEvent methods. We do not
// reuse the kafka JSONCodec because that pulls in watermill-kafka
// dependencies that this package has no business depending on.
type jsonCodec struct {
	registry map[string]func() domain.DomainEvent
}

func newJSONCodec() *jsonCodec {
	return &jsonCodec{registry: map[string]func() domain.DomainEvent{}}
}

func (c *jsonCodec) Register(name string, factory func() domain.DomainEvent) {
	c.registry[name] = factory
}

func (c *jsonCodec) Marshal(_ context.Context, ev domain.DomainEvent) (*message.Message, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	msg := message.NewMessage(ev.EventID(), payload)
	msg.Metadata.Set(eventbus.HeaderEventID, ev.EventID())
	msg.Metadata.Set(eventbus.HeaderEventName, ev.EventName())
	msg.Metadata.Set(eventbus.HeaderAggregateID, ev.AggregateID())
	msg.Metadata.Set(eventbus.HeaderAggregateType, ev.AggregateType())
	return msg, nil
}

func (c *jsonCodec) Unmarshal(_ context.Context, msg *message.Message) (domain.DomainEvent, string, error) {
	name := msg.Metadata.Get(eventbus.HeaderEventName)
	factory, ok := c.registry[name]
	if !ok {
		return nil, name, fmt.Errorf("no factory for %q", name)
	}
	ev := factory()
	if err := json.Unmarshal(msg.Payload, ev); err != nil {
		return nil, name, err
	}
	return ev, name, nil
}

type publishCall struct {
	topic   string
	eventID string
}

// fakePublisher captures every Publish call. Goroutine-safe.
type fakePublisher struct {
	mu    sync.Mutex
	calls []publishCall
}

func (p *fakePublisher) Close() error { return nil }

func (p *fakePublisher) Publish(_ context.Context, topic string, events ...domain.DomainEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ev := range events {
		p.calls = append(p.calls, publishCall{topic: topic, eventID: ev.EventID()})
	}
	return nil
}

func (p *fakePublisher) snapshot() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishCall, len(p.calls))
	copy(out, p.calls)
	return out
}

func silentLog() logger.Logger {
	return slogger.New(slogger.Config{Writer: io.Discard, Level: slog.LevelError + 1})
}

// --- helpers ---------------------------------------------------------

func newStore(t *testing.T, opts ...pgxoutbox.Option) *pgxoutbox.Store {
	t.Helper()
	s, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: newJSONCodec()}, opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func newTxManager(t *testing.T) *pgxdb.TxManager {
	t.Helper()
	return pgxdb.NewTxManager(sharedPool)
}

// stageEvents wraps Stage in a TxManager.WithinTx so tests don't have
// to handle the tx-management boilerplate every time.
func stageEvents(t *testing.T, store *pgxoutbox.Store, topic string, events ...domain.DomainEvent) {
	t.Helper()
	tm := newTxManager(t)
	if err := tm.WithinTx(context.Background(), func(ctx context.Context) error {
		return store.Stage(ctx, topic, events...)
	}); err != nil {
		t.Fatalf("stageEvents: %v", err)
	}
}

// splitID parses an OutboxRecord.ID via the same shape pgxoutbox uses
// internally. Tests need to look up rows by their numeric dbid to do
// things like force-expire the lease.
func splitID(t *testing.T, id string) (dbID int64, token string) {
	t.Helper()
	idx := strings.LastIndex(id, ":")
	if idx <= 0 {
		t.Fatalf("splitID: malformed id %q", id)
	}
	dbID, err := strconv.ParseInt(id[:idx], 10, 64)
	if err != nil {
		t.Fatalf("splitID: parse dbid %q: %v", id[:idx], err)
	}
	return dbID, id[idx+1:]
}

func countActiveRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := sharedPool.QueryRow(context.Background(), "SELECT COUNT(*) FROM outbox_records").Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func countDLQRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := sharedPool.QueryRow(context.Background(), "SELECT COUNT(*) FROM outbox_dead_letters").Scan(&n); err != nil {
		t.Fatalf("count dlq: %v", err)
	}
	return n
}

// forceExpireLease pushes the row's claimed_until into the past so the
// next Fetch can re-claim it. Avoids wall-clock sleeps in tests.
func forceExpireLease(t *testing.T, dbID int64) {
	t.Helper()
	const sql = "UPDATE outbox_records SET claimed_until = now() - interval '1 second' WHERE id = $1"
	if _, err := sharedPool.Exec(context.Background(), sql, dbID); err != nil {
		t.Fatalf("forceExpireLease: %v", err)
	}
}

// rowFields returns the durable per-row state tests want to assert
// against. Returning zero values for NULL columns (last_error,
// claim_token) keeps the call site readable.
type rowState struct {
	Attempts    int
	LastError   string
	HasLastErr  bool
	ClaimToken  string
	HasToken    bool
	AvailableAt time.Time
}

func rowFields(t *testing.T, dbID int64) rowState {
	t.Helper()
	var (
		attempts    int
		lastError   *string
		claimToken  *string
		availableAt time.Time
	)
	const sql = `SELECT attempts, last_error, claim_token::text, available_at
                 FROM outbox_records WHERE id = $1`
	if err := sharedPool.QueryRow(context.Background(), sql, dbID).Scan(
		&attempts, &lastError, &claimToken, &availableAt,
	); err != nil {
		t.Fatalf("rowFields id=%d: %v", dbID, err)
	}
	st := rowState{Attempts: attempts, AvailableAt: availableAt}
	if lastError != nil {
		st.LastError = *lastError
		st.HasLastErr = true
	}
	if claimToken != nil {
		st.ClaimToken = *claimToken
		st.HasToken = true
	}
	return st
}

// --- Stage tests -----------------------------------------------------

func TestStage_EmptyEventsIsNoOp(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	// Stage with no events should be a no-op AND should NOT require a
	// tx in ctx — empty stage is fast-path before the tx check.
	if err := store.Stage(context.Background(), "topic"); err != nil {
		t.Fatalf("Stage empty: %v", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows = %d, want 0", got)
	}
}

func TestStage_NoTxReturnsErrNoTx(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	ev := makeEvent("evt-1", "order.placed.v1", "agg-1", "Order", "body")
	// No WithinTx wrapper → no tx in ctx → must refuse with ErrNoTx,
	// no silent autocommit (decision #11).
	err := store.Stage(context.Background(), "orders", ev)
	if !errors.Is(err, pgxoutbox.ErrNoTx) {
		t.Fatalf("Stage without tx err = %v, want ErrNoTx", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows after refused Stage = %d, want 0", got)
	}
}

func TestStage_HappyPathPersistsRow(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	ev := makeEvent("evt-stage-1", "order.placed.v1", "agg-1", "Order", "body-1")

	stageEvents(t, store, "orders", ev)

	recs, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("Fetch returned %d, want 1", len(recs))
	}
	r := recs[0]
	if r.EventID != "evt-stage-1" || r.Topic != "orders" {
		t.Fatalf("record fields off: %+v", r)
	}
	if r.EventName != "order.placed.v1" || r.AggregateID != "agg-1" || r.AggregateType != "Order" {
		t.Fatalf("canonical fields off: %+v", r)
	}
	if r.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0", r.Attempts)
	}
}

func TestStage_RollbackErasesAll(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	tm := newTxManager(t)
	ev := makeEvent("evt-rb-1", "order.placed.v1", "agg-1", "Order", "body")
	sentinel := errors.New("rollback me")

	err := tm.WithinTx(context.Background(), func(ctx context.Context) error {
		if err := store.Stage(ctx, "orders", ev); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithinTx err = %v, want sentinel", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows after rollback = %d, want 0", got)
	}
}

func TestStage_VariadicProducesNRowsWithDistinctEventIDs(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	events := []domain.DomainEvent{
		makeEvent("evt-a", "order.placed.v1", "agg-a", "Order", "body-a"),
		makeEvent("evt-b", "order.placed.v1", "agg-b", "Order", "body-b"),
		makeEvent("evt-c", "order.placed.v1", "agg-c", "Order", "body-c"),
	}
	stageEvents(t, store, "orders", events...)

	if got := countActiveRows(t); got != 3 {
		t.Fatalf("active rows = %d, want 3", got)
	}
	recs, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	seen := map[string]struct{}{}
	for _, r := range recs {
		if _, dup := seen[r.EventID]; dup {
			t.Fatalf("duplicate EventID %q in Fetch", r.EventID)
		}
		seen[r.EventID] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct EventIDs, want 3", len(seen))
	}
}

// --- Fetch tests -----------------------------------------------------

func TestFetch_IDFormatAndUniqueClaimTokens(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	events := []domain.DomainEvent{
		makeEvent("evt-1", "x.v1", "a-1", "X", "b1"),
		makeEvent("evt-2", "x.v1", "a-2", "X", "b2"),
		makeEvent("evt-3", "x.v1", "a-3", "X", "b3"),
	}
	stageEvents(t, store, "t", events...)

	recs, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tokens := map[string]struct{}{}
	for _, r := range recs {
		_, token := splitID(t, r.ID)
		// 36-char UUID canonical form check (a regex test exists in
		// the unit-level parseID, but verifying at the integration
		// boundary is the actually-running shape).
		if len(token) != 36 {
			t.Fatalf("token %q is not a 36-char UUID", token)
		}
		if _, dup := tokens[token]; dup {
			t.Fatalf("duplicate claim_token %q across records in same batch", token)
		}
		tokens[token] = struct{}{}
	}
}

func TestFetch_Ordering_AvailableAtThenID(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	// Stage 5 events back-to-back in one tx. clock_timestamp() is
	// sub-statement, so created_at / available_at may differ between
	// rows OR be equal — Fetch must order by (available_at, id) and
	// the result must be stable.
	events := []domain.DomainEvent{}
	for i := 0; i < 5; i++ {
		events = append(events, makeEvent(
			fmt.Sprintf("evt-%d", i), "x.v1", "a", "X", fmt.Sprintf("b-%d", i),
		))
	}
	stageEvents(t, store, "t", events...)

	// Run two Fetches against two stores against the same row state —
	// but to test ordering we need to make rows re-fetchable. So
	// force-expire all leases after the first Fetch and re-fetch.
	first, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	for _, r := range first {
		dbID, _ := splitID(t, r.ID)
		forceExpireLease(t, dbID)
	}
	second, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("len mismatch: first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		f := first[i]
		s := second[i]
		fDB, _ := splitID(t, f.ID)
		sDB, _ := splitID(t, s.ID)
		if fDB != sDB {
			t.Fatalf("ordering not stable at index %d: first dbid=%d, second dbid=%d", i, fDB, sDB)
		}
	}
}

func TestFetch_SkipsNotYetDue(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	ev := makeEvent("evt-future", "x.v1", "a", "X", "b")
	stageEvents(t, store, "t", ev)

	// Push available_at into the future.
	ctx := context.Background()
	if _, err := sharedPool.Exec(ctx,
		"UPDATE outbox_records SET available_at = now() + interval '1 hour'",
	); err != nil {
		t.Fatalf("push availability: %v", err)
	}

	recs, err := store.Fetch(ctx, 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("Fetch returned %d not-yet-due rows, want 0", len(recs))
	}
}

func TestFetch_SkipsCurrentlyClaimed(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-claim", "x.v1", "a", "X", "b"))

	// First Fetch claims the row with a fresh lease.
	first, err := store.Fetch(context.Background(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Fetch: err=%v len=%d", err, len(first))
	}

	// Second Fetch — lease still active — should see no rows.
	second, err := store.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second Fetch claimed-row leak: got %d, want 0", len(second))
	}
}

// TestFetch_SkipLockedTwoPoolsDisjoint: two pgxoutbox.Store instances
// backed by the same pool race to Fetch the same backlog. SKIP LOCKED
// guarantees the union of their results equals the backlog with no
// overlap. Codifies the multi-Relay safety claim in doc.go.
func TestFetch_SkipLockedTwoPoolsDisjoint(t *testing.T) {
	truncateTables(t)
	storeA := newStore(t)
	storeB := newStore(t)

	const N = 8
	events := []domain.DomainEvent{}
	for i := 0; i < N; i++ {
		events = append(events, makeEvent(
			fmt.Sprintf("evt-%d", i), "x.v1", "a", "X", "b",
		))
	}
	stageEvents(t, storeA, "t", events...)

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		fromA, fromB []eventbus.OutboxRecord
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		recs, err := storeA.Fetch(context.Background(), N)
		if err != nil {
			t.Errorf("storeA.Fetch: %v", err)
			return
		}
		mu.Lock()
		fromA = recs
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		recs, err := storeB.Fetch(context.Background(), N)
		if err != nil {
			t.Errorf("storeB.Fetch: %v", err)
			return
		}
		mu.Lock()
		fromB = recs
		mu.Unlock()
	}()
	wg.Wait()

	seen := map[int64]string{}
	for _, r := range fromA {
		id, _ := splitID(t, r.ID)
		seen[id] = "A"
	}
	for _, r := range fromB {
		id, _ := splitID(t, r.ID)
		if prev, dup := seen[id]; dup {
			t.Fatalf("dbid %d claimed by both %s and B", id, prev)
		}
		seen[id] = "B"
	}
	if total := len(fromA) + len(fromB); total > N {
		t.Fatalf("union size %d > backlog %d (overlap)", total, N)
	}
}

// TestFetch_LeaseExpiryReclaimsWithFreshToken codifies the recovery
// path after a worker crash: lease expires, next Fetch re-claims the
// row, AND the new claim_token differs from the prior one. Decision
// #21 says the token must rotate per claim.
func TestFetch_LeaseExpiryReclaimsWithFreshToken(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-lease", "x.v1", "a", "X", "b"))

	first, err := store.Fetch(context.Background(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Fetch: err=%v len=%d", err, len(first))
	}
	dbID, token1 := splitID(t, first[0].ID)

	forceExpireLease(t, dbID)

	second, err := store.Fetch(context.Background(), 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("second Fetch: err=%v len=%d", err, len(second))
	}
	dbID2, token2 := splitID(t, second[0].ID)
	if dbID2 != dbID {
		t.Fatalf("second Fetch returned different dbid: %d vs %d", dbID2, dbID)
	}
	if token1 == token2 {
		t.Fatalf("claim_token did not rotate across claims: %q == %q", token1, token2)
	}
}

// TestFetch_AtLeastOnceDuplicateObservation codifies decision #17:
// the same row can be claimed and (would be) published twice across
// the lease boundary; the stale-token guard on MarkSent makes the
// first worker's ack a no-op while the second worker's ack succeeds.
// Simulated via two manual Fetches + two fake Publish calls rather
// than running a full slow-publisher Relay — the SQL invariant is
// the same and the test is deterministic.
func TestFetch_AtLeastOnceDuplicateObservation(t *testing.T) {
	truncateTables(t)
	storeA := newStore(t)
	storeB := newStore(t)
	stageEvents(t, storeA, "t", makeEvent("evt-dup", "x.v1", "a", "X", "b"))

	first, err := storeA.Fetch(context.Background(), 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first Fetch: err=%v len=%d", err, len(first))
	}
	dbID, _ := splitID(t, first[0].ID)
	forceExpireLease(t, dbID)

	second, err := storeB.Fetch(context.Background(), 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("second Fetch: err=%v len=%d", err, len(second))
	}
	if first[0].EventID != second[0].EventID {
		t.Fatalf("EventIDs differ between claims: %q vs %q", first[0].EventID, second[0].EventID)
	}

	// Simulate that both Relay instances Published. Their MarkSent
	// calls happen in arbitrary order; what matters is that exactly
	// the second one (with the current token) deletes the row.
	pub := &fakePublisher{}
	_ = pub.Publish(context.Background(), "t", makeEvent(first[0].EventID, "x.v1", "a", "X", "b"))
	_ = pub.Publish(context.Background(), "t", makeEvent(second[0].EventID, "x.v1", "a", "X", "b"))
	if got := len(pub.snapshot()); got != 2 {
		t.Fatalf("simulated publishes = %d, want 2", got)
	}

	// Stale worker MarkSent → no-op.
	if err := storeA.MarkSent(context.Background(), first[0].ID); err != nil {
		t.Fatalf("storeA stale MarkSent: %v", err)
	}
	if got := countActiveRows(t); got != 1 {
		t.Fatalf("stale MarkSent affected row: count=%d, want 1", got)
	}

	// Current worker MarkSent → row deleted.
	if err := storeB.MarkSent(context.Background(), second[0].ID); err != nil {
		t.Fatalf("storeB current MarkSent: %v", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("current MarkSent left row behind: count=%d, want 0", got)
	}
}

// --- MarkSent tests --------------------------------------------------

func TestMarkSent_RemovesRow(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-1", "x.v1", "a", "X", "b"))

	recs, _ := store.Fetch(context.Background(), 10)
	if err := store.MarkSent(context.Background(), recs[0].ID); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows after MarkSent = %d, want 0", got)
	}
}

func TestMarkSent_StaleTokenSilentNoOp(t *testing.T) {
	truncateTables(t)
	storeA := newStore(t)
	storeB := newStore(t)
	stageEvents(t, storeA, "t", makeEvent("evt-1", "x.v1", "a", "X", "b"))

	first, _ := storeA.Fetch(context.Background(), 10)
	dbID, _ := splitID(t, first[0].ID)
	forceExpireLease(t, dbID)
	second, _ := storeB.Fetch(context.Background(), 10)

	// Stale token MarkSent — nil error, row untouched.
	if err := storeA.MarkSent(context.Background(), first[0].ID); err != nil {
		t.Fatalf("stale MarkSent: %v", err)
	}
	if got := countActiveRows(t); got != 1 {
		t.Fatalf("active rows = %d, want 1", got)
	}

	// Current MarkSent does delete.
	if err := storeB.MarkSent(context.Background(), second[0].ID); err != nil {
		t.Fatalf("current MarkSent: %v", err)
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows = %d, want 0", got)
	}
}

func TestMarkSent_UnknownDBIDSilentNoOp(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	// Well-formed id, but no row exists with this dbid.
	const fakeID = "99999:550e8400-e29b-41d4-a716-446655440000"
	if err := store.MarkSent(context.Background(), fakeID); err != nil {
		t.Fatalf("MarkSent unknown id: %v", err)
	}
}

func TestMarkSent_MalformedIDReturnsErrMalformedID(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	cases := []string{
		"",
		"42",
		"42:",
		":550e8400-e29b-41d4-a716-446655440000",
		"42:not-a-uuid",
		"abc:550e8400-e29b-41d4-a716-446655440000",
		"42:550e8400-e29b-41d4-a716", // too short
	}
	for _, id := range cases {
		err := store.MarkSent(context.Background(), id)
		if !errors.Is(err, pgxoutbox.ErrMalformedID) {
			t.Errorf("MarkSent(%q) err = %v, want ErrMalformedID", id, err)
		}
	}
}

// --- MarkFailed tests ------------------------------------------------

func TestMarkFailed_BumpsAttemptsAndRotatesScheduling(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-mf", "x.v1", "a", "X", "b"))

	recs, _ := store.Fetch(context.Background(), 10)
	dbID, _ := splitID(t, recs[0].ID)

	next := time.Now().Add(30 * time.Second).UTC().Truncate(time.Millisecond)
	if err := store.MarkFailed(context.Background(), recs[0].ID, "boom", next); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	st := rowFields(t, dbID)
	if st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", st.Attempts)
	}
	if !st.HasLastErr || st.LastError != "boom" {
		t.Errorf("last_error = %v %q, want \"boom\"", st.HasLastErr, st.LastError)
	}
	if st.HasToken {
		t.Errorf("claim_token should be NULL after MarkFailed, got %q", st.ClaimToken)
	}
	// available_at should equal nextAttemptAt (UTC truncation aligns
	// with Postgres TIMESTAMPTZ microsecond precision).
	if !st.AvailableAt.Equal(next) {
		t.Errorf("available_at = %v, want %v", st.AvailableAt, next)
	}
}

func TestMarkFailed_StaleTokenSilentNoOp(t *testing.T) {
	truncateTables(t)
	storeA := newStore(t)
	storeB := newStore(t)
	stageEvents(t, storeA, "t", makeEvent("evt-mf-stale", "x.v1", "a", "X", "b"))

	first, _ := storeA.Fetch(context.Background(), 10)
	dbID, _ := splitID(t, first[0].ID)
	forceExpireLease(t, dbID)
	// storeB.Fetch rotates the token; we only care about the
	// side-effect, not the returned record.
	if _, err := storeB.Fetch(context.Background(), 10); err != nil {
		t.Fatalf("storeB.Fetch: %v", err)
	}

	// storeA's token is now stale. MarkFailed must not bump attempts
	// or change available_at on the row currently owned by storeB.
	preB := rowFields(t, dbID)
	if err := storeA.MarkFailed(context.Background(), first[0].ID, "ignored", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("stale MarkFailed: %v", err)
	}
	postB := rowFields(t, dbID)
	if postB.Attempts != preB.Attempts {
		t.Errorf("stale MarkFailed bumped attempts: pre=%d post=%d", preB.Attempts, postB.Attempts)
	}
	if postB.ClaimToken != preB.ClaimToken {
		t.Errorf("stale MarkFailed rotated token: pre=%q post=%q", preB.ClaimToken, postB.ClaimToken)
	}
}

func TestMarkFailed_MalformedIDReturnsErrMalformedID(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	err := store.MarkFailed(context.Background(), "garbage", "r", time.Now())
	if !errors.Is(err, pgxoutbox.ErrMalformedID) {
		t.Fatalf("err = %v, want ErrMalformedID", err)
	}
}

// --- Terminate tests -------------------------------------------------

func TestTerminate_MovesToDLQWithAttemptsPlusOne(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-term", "x.v1", "a", "X", "b"))

	first, _ := store.Fetch(context.Background(), 10)
	// Bump attempts once so we can prove Terminate stores attempts+1
	// (the attempt that caused termination) — matching memory adapter.
	dbID, _ := splitID(t, first[0].ID)
	forceExpireLease(t, dbID)
	first2, _ := store.Fetch(context.Background(), 10)
	if err := store.MarkFailed(context.Background(), first2[0].ID, "first failure", time.Now()); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	// Now re-fetch (lease expired by MarkFailed setting claimed_until=NULL),
	// then Terminate.
	rec3, _ := store.Fetch(context.Background(), 10)
	if err := store.Terminate(context.Background(), rec3[0].ID, "give up"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	if got := countActiveRows(t); got != 0 {
		t.Errorf("active rows after Terminate = %d, want 0", got)
	}
	if got := countDLQRows(t); got != 1 {
		t.Fatalf("dlq rows = %d, want 1", got)
	}

	var (
		dlqAttempts int
		dlqReason   string
		dlqOutboxID int64
		dlqEventID  string
	)
	if err := sharedPool.QueryRow(context.Background(),
		"SELECT outbox_id, event_id, attempts, reason FROM outbox_dead_letters",
	).Scan(&dlqOutboxID, &dlqEventID, &dlqAttempts, &dlqReason); err != nil {
		t.Fatalf("read dlq: %v", err)
	}
	if dlqOutboxID != dbID {
		t.Errorf("dlq outbox_id = %d, want %d", dlqOutboxID, dbID)
	}
	if dlqEventID != "evt-term" {
		t.Errorf("dlq event_id = %q, want \"evt-term\"", dlqEventID)
	}
	// Active row's attempts was 1 at Terminate time → DLQ stores 2.
	if dlqAttempts != 2 {
		t.Errorf("dlq attempts = %d, want 2 (active+1)", dlqAttempts)
	}
	if dlqReason != "give up" {
		t.Errorf("dlq reason = %q, want \"give up\"", dlqReason)
	}
}

// TestTerminate_ConcurrentRaceProducesOneDLQRow codifies decision #23:
// two concurrent Terminate calls against the same id+token race in
// the SAME tx serialization; the DELETE ... RETURNING CTE means only
// the worker whose DELETE actually returned a row inserts into the
// DLQ. We launch many goroutines to maximise contention.
func TestTerminate_ConcurrentRaceProducesOneDLQRow(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	stageEvents(t, store, "t", makeEvent("evt-race", "x.v1", "a", "X", "b"))

	first, _ := store.Fetch(context.Background(), 10)
	id := first[0].ID

	const racers = 8
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			if err := store.Terminate(context.Background(), id, "race"); err != nil {
				t.Errorf("racer Terminate: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := countActiveRows(t); got != 0 {
		t.Errorf("active rows after race = %d, want 0", got)
	}
	if got := countDLQRows(t); got != 1 {
		t.Fatalf("dlq rows after concurrent Terminate = %d, want 1", got)
	}
}

func TestTerminate_StaleTokenSilentNoOp(t *testing.T) {
	truncateTables(t)
	storeA := newStore(t)
	storeB := newStore(t)
	stageEvents(t, storeA, "t", makeEvent("evt-term-stale", "x.v1", "a", "X", "b"))

	first, _ := storeA.Fetch(context.Background(), 10)
	dbID, _ := splitID(t, first[0].ID)
	forceExpireLease(t, dbID)
	_, _ = storeB.Fetch(context.Background(), 10)

	if err := storeA.Terminate(context.Background(), first[0].ID, "stale"); err != nil {
		t.Fatalf("stale Terminate: %v", err)
	}
	// Stale Terminate must NOT delete the row OR write a DLQ entry.
	if got := countActiveRows(t); got != 1 {
		t.Errorf("active rows after stale Terminate = %d, want 1", got)
	}
	if got := countDLQRows(t); got != 0 {
		t.Errorf("dlq rows after stale Terminate = %d, want 0", got)
	}
}

func TestTerminate_MalformedIDReturnsErrMalformedID(t *testing.T) {
	truncateTables(t)
	store := newStore(t)
	err := store.Terminate(context.Background(), "garbage", "r")
	if !errors.Is(err, pgxoutbox.ErrMalformedID) {
		t.Fatalf("err = %v, want ErrMalformedID", err)
	}
}

// --- Relay end-to-end ------------------------------------------------

// TestRelay_EndToEndHappyPath wires outbox.NewRelay against the pgx
// Store + a fake Publisher and observes the full drain cycle: Stage
// → Relay polls → Publisher.Publish → Store.MarkSent → row gone.
func TestRelay_EndToEndHappyPath(t *testing.T) {
	truncateTables(t)
	codec := newJSONCodec()
	codec.Register("x.v1", func() domain.DomainEvent { return &testEvent{} })

	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: sharedPool, Codec: codec})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	pub := &fakePublisher{}
	relay, err := outbox.NewRelay(outbox.RelayConfig{
		Store:     store,
		Publisher: pub,
		Codec:     codec,
		Logger:    silentLog(),
	},
		outbox.WithPollInterval(50*time.Millisecond),
		outbox.WithBatchSize(10),
		outbox.WithMaxAttempts(3),
	)
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}

	// Stage one event using a separate tx; then start the Relay.
	tm := pgxdb.NewTxManager(sharedPool)
	if err := tm.WithinTx(context.Background(), func(ctx context.Context) error {
		return store.Stage(ctx, "orders", makeEvent("evt-relay", "x.v1", "a", "X", "body"))
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	relayCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(relayCtx) }()

	// Wait for the Relay to drain — poll Publish count.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(pub.snapshot()) > 0 && countActiveRows(t) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-relayDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Relay.Run: %v", err)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("publish calls = %d, want 1; calls=%+v", len(calls), calls)
	}
	if calls[0].topic != "orders" || calls[0].eventID != "evt-relay" {
		t.Fatalf("publish call off: %+v", calls[0])
	}
	if got := countActiveRows(t); got != 0 {
		t.Fatalf("active rows after Relay drain = %d, want 0", got)
	}
}

// --- ErrNoTx / ErrMalformedID sentinel identity ----------------------

// TestSentinels_ReExportedFromPgxdb verifies ErrNoTx is the same
// sentinel as pgxdb.ErrNoTx (so callers can compare against either).
func TestSentinels_ReExportedFromPgxdb(t *testing.T) {
	if !errors.Is(pgxoutbox.ErrNoTx, pgxdb.ErrNoTx) {
		t.Fatalf("pgxoutbox.ErrNoTx is not pgxdb.ErrNoTx")
	}
}
