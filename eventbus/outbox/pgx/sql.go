package pgxoutbox

// SQL statements used by Store. Kept as exported-style const block so
// schema changes during a future migration bump have a single grep
// target. claim_token semantics (decisions #21 / #22 / #23) are
// enforced here: every Mark*/Terminate predicate ends with
// `AND claim_token = $token`; Fetch issues a fresh token per claim.
//
// Timestamp clock ownership (decision #16): Stage uses
// clock_timestamp() so back-to-back rows in one INSERT can get
// distinct wall-clock values; Fetch / MarkFailed lease columns use
// now() (tx-start stable) which is the natural choice for
// lease-window arithmetic.

const (
	// stageColumns is the column list shared by Stage INSERT and Fetch
	// SELECT RETURNING. Keep it in one place so a column rename touches
	// only this constant.
	stageColumns = "event_id, topic, event_name, aggregate_id, aggregate_type, payload, headers, created_at, available_at"

	// fetchSQL is a single statement, three-stage CTE:
	//
	//   1. `due` selects up to $1 due rows under FOR UPDATE SKIP
	//      LOCKED, ordered by (available_at, id) — this is the lock
	//      order. Multiple Relays converging on the same backlog
	//      claim disjoint rows via SKIP LOCKED (no overlap; no
	//      fairness guarantee).
	//   2. `updated` stamps a fresh claim_token + lease window on
	//      each locked row and RETURNs the full row.
	//   3. The outer SELECT projects the columns the Store needs and
	//      re-applies `ORDER BY available_at, id`.
	//
	// The outer ORDER BY matters because PostgreSQL's UPDATE ...
	// RETURNING does NOT preserve the CTE's row order — RETURNING
	// emits rows in implementation-defined order (typically heap
	// scan), independent of any ORDER BY upstream. Without the outer
	// SELECT, Fetch results would be nondeterministically ordered
	// even though `due` picks rows in a fixed sequence. UPDATE
	// touches only claimed_until / claim_token, so `available_at` in
	// `updated` is the same value the lock-order in `due` used; the
	// outer ORDER BY is therefore equivalent to the lock order.
	//
	// claim_token is returned via the ::text cast so the Go side
	// never has to depend on a UUID library — the token stays an
	// opaque string in the adapter-private OutboxRecord.ID encoding.
	fetchSQL = `WITH due AS (
    SELECT id FROM outbox_records
    WHERE available_at <= now()
      AND (claimed_until IS NULL OR claimed_until <= now())
    ORDER BY available_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
),
updated AS (
    UPDATE outbox_records
    SET claimed_until = now() + $2::interval,
        claim_token   = gen_random_uuid()
    FROM due
    WHERE outbox_records.id = due.id
    RETURNING outbox_records.id,
              outbox_records.claim_token,
              outbox_records.event_id,
              outbox_records.topic,
              outbox_records.event_name,
              outbox_records.aggregate_id,
              outbox_records.aggregate_type,
              outbox_records.payload,
              outbox_records.headers,
              outbox_records.created_at,
              outbox_records.available_at,
              outbox_records.attempts
)
SELECT id,
       claim_token::text,
       event_id,
       topic,
       event_name,
       aggregate_id,
       aggregate_type,
       payload,
       headers,
       created_at,
       available_at,
       attempts
FROM updated
ORDER BY available_at, id`

	// markSentSQL deletes the row only when the supplied claim_token
	// still matches — stale workers (whose lease expired and was
	// re-claimed) produce zero affected rows and the Store treats that
	// as silent success per decision #22.
	markSentSQL = `DELETE FROM outbox_records WHERE id = $1 AND claim_token = $2::uuid`

	// markFailedSQL also guards on claim_token. Clears both
	// claimed_until and claim_token so the next Fetch can re-claim
	// cleanly. $2 is Relay's app-clock nextAttemptAt (decision #16's
	// app-clock leg).
	markFailedSQL = `UPDATE outbox_records
SET attempts      = attempts + 1,
    available_at  = $2,
    last_error    = $3,
    claimed_until = NULL,
    claim_token   = NULL
WHERE id = $1 AND claim_token = $4::uuid`

	// terminateSQL is atomic via DELETE ... RETURNING CTE (decision
	// #23). Only the worker whose DELETE actually returned a row
	// inserts into outbox_dead_letters; stale concurrent Terminate
	// calls see zero rows from `del` and INSERT zero rows. Attempts is
	// stored as the row's surviving count + 1 (the attempt that caused
	// termination), matching memory adapter semantics.
	terminateSQL = `WITH del AS (
    DELETE FROM outbox_records
    WHERE id = $1 AND claim_token = $2::uuid
    RETURNING id, event_id, topic, event_name, aggregate_id, aggregate_type,
              payload, headers, created_at, available_at, attempts
)
INSERT INTO outbox_dead_letters (
    outbox_id, event_id, topic, event_name, aggregate_id, aggregate_type,
    payload, headers, created_at, available_at, attempts, reason, failed_at
)
SELECT id, event_id, topic, event_name, aggregate_id, aggregate_type,
       payload, headers, created_at, available_at, attempts + 1, $3, now()
FROM del`
)
