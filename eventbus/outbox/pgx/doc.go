// Package pgxoutbox is the pgx/v5-backed Postgres implementation of
// the go-ddd-core eventbus Outbox + OutboxStore interfaces, plus the
// local outbox.DeadLetterRecorder extension. It is the production
// successor to the in-process Memory adapter shipped in v0.3.0; the
// Memory adapter is suitable for tests and single-instance examples
// only and does NOT participate in the caller's transaction.
//
// # Postgres baseline
//
// Postgres 12+ is the declared baseline. Migration 001 includes
// `CREATE EXTENSION IF NOT EXISTS pgcrypto` so `gen_random_uuid()` is
// available on 12 (where it is otherwise a pgcrypto function);
// 13+ already provides it as a builtin and the extension call is a
// no-op. Tests run against `postgres:16-alpine` via testcontainers.
//
// # Transactional Stage contract
//
// Stage requires a pgx.Tx attached to ctx via ports/database/pgx
// (`pgxdb.TxFromContext`). Without one it returns ErrNoTx — never a
// silent autocommit. This matches the core docstring requirement that
// Outbox implementations participate in the caller's transaction. The
// expected wiring is:
//
//	err := txManager.WithinTx(ctx, func(ctx context.Context) error {
//	    if err := repo.Save(ctx, agg); err != nil { return err }
//	    return outboxStore.Stage(ctx, "orders.events", agg.PullEvents()...)
//	})
//
// Stage marshals every event before issuing a single multi-VALUES
// INSERT so a bad event aborts the batch without partial state. The
// caller's tx rollback is the final guarantor of all-or-nothing.
//
// # Fetch / claim model
//
// Fetch is a short-tx, lease-based claim (the "Option A" pattern in
// the adapter `.agent/decisions.md`). One CTE round trip:
//
//	WITH due AS (SELECT id ... ORDER BY available_at, id LIMIT $1 FOR UPDATE SKIP LOCKED)
//	UPDATE outbox_records
//	SET claimed_until = now() + lease, claim_token = gen_random_uuid()
//	FROM due WHERE outbox_records.id = due.id RETURNING ...
//
// `FOR UPDATE SKIP LOCKED` makes concurrent Relay instances safe
// across processes / hosts. Each claimed row gets a fresh UUID
// `claim_token`; the Store encodes the token into OutboxRecord.ID as
// `"<dbid>:<UUID>"` so subsequent MarkSent / MarkFailed / Terminate
// calls can guard on it. The core OutboxStore interface stays
// id-only.
//
// # At-least-once delivery
//
// pgxoutbox provides at-least-once delivery, NOT exactly-once. A
// duplicate publish can happen whenever:
//
//   - Publisher.Publish takes longer than the claim lease to return,
//     a second Relay re-claims the row, and both eventually succeed.
//   - The Relay process crashes after Publish but before MarkSent.
//   - A network partition between Relay and Postgres causes MarkSent
//     to fail while the broker already accepted the message.
//
// Downstream consumers MUST deduplicate via eventbus/inbox (or an
// equivalent Inbox-style idempotency layer) using
// OutboxRecord.EventID (the domain event's identity, carried verbatim
// to the broker as message-id). Tuning WithClaimLease reduces the
// duplicate window but does NOT eliminate it.
//
// # Lease tuning
//
// The lease window is the at-least-once duplicate-publish window AND
// the crash-recovery window:
//
//   - Lease too short → slow Publisher.Publish exceeds it → second
//     Relay re-claims → duplicate publish.
//   - Lease too long → Relay crash → row stuck for the lease duration
//     before redelivery.
//
// Default is 5 seconds, targeting the common case where Publish
// completes in well under one second. WithClaimLease overrides;
// values below 100ms are silently clamped up.
//
// # Stale-worker safety (claim_token)
//
// Every mutating SQL (MarkSent / MarkFailed / Terminate) ends with
// `AND claim_token = $token`. If worker A's lease expired between
// Fetch and Mark*, worker B re-claimed the row (and gen_random_uuid
// produced a fresh token), and worker A then attempts Mark*, the
// UPDATE / DELETE affects zero rows. pgxoutbox treats zero rows
// affected as silent success (returns nil) — this matches the
// at-least-once contract: stale Mark* is a no-op, not an error.
//
// # ErrMalformedID
//
// MarkSent / MarkFailed / Terminate parse OutboxRecord.ID as
// "<dbid>:<UUID>". An id not in that shape returns ErrMalformedID.
// This is a programmer-error path (an id constructed by something
// other than this adapter's Fetch). Zero-row Mark* with a
// well-formed but unknown id still returns nil.
//
// # Terminate atomicity
//
// Terminate is one SQL statement, a DELETE ... RETURNING CTE feeding
// an INSERT into outbox_dead_letters. Only the worker whose DELETE
// actually returned a row gets its data into the DLQ; concurrent
// stale Terminate calls see zero rows from `del` and INSERT zero
// rows. There is no INSERT-then-DELETE window where two workers
// could double-DLQ the same row.
//
// # Clock model
//
// Mixed by column ownership (decision #16 in
// .agent/decisions.md):
//
//   - outbox_records.created_at, .available_at (Stage), .claimed_until,
//     outbox_dead_letters.failed_at  — DB clock via `clock_timestamp()`
//     or `now()`.
//   - outbox_records.available_at  written by MarkFailed — Relay's
//     app clock, passed in via the OutboxStore.MarkFailed
//     nextAttemptAt argument.
//
// The mixed model affects retry scheduling precision (a Relay host
// clock skewed +N relative to DB retries N earlier/later than
// nominal) but does NOT affect claim correctness. Operate with NTP
// sync.
//
// # What this adapter does NOT do
//
//   - Run migrations at runtime. `migrations.FS` is exposed for
//     testcontainers and example wiring, but production schema
//     management is the caller's responsibility (golang-migrate,
//     goose, atlas, flyway — any tool that consumes versioned SQL
//     files).
//   - Worker attribution. The lease answers "is this row claimed?"
//     via wall-clock `claimed_until`; it does NOT identify WHICH
//     worker holds it. Adding a `claim_id` column is a separate
//     follow-up if operator visibility into worker identity is
//     required.
//   - Per-call pgx.TxOptions overrides. Isolation / access-mode is
//     a TxManager-level default (see pgxdb.WithTxOptions). Core's
//     database.TxManager.WithinTx contract has no slot for per-call
//     options.
package pgxoutbox
