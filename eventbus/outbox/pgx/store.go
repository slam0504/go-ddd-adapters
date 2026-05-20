package pgxoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"

	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// ErrNoTx is re-exported from ports/database/pgx so callers of Stage
// don't have to import both packages just to switch on the sentinel.
var ErrNoTx = pgxdb.ErrNoTx

// ErrMalformedID is returned by MarkSent / MarkFailed / Terminate when
// the caller-supplied OutboxRecord.ID is not in the
// adapter-private "<dbid>:<UUID>" shape produced by Fetch
// (decision #21). It signals a programmer error — never the
// at-least-once normal flow.
var ErrMalformedID = errors.New("eventbus/outbox/pgx: malformed OutboxRecord.ID")

// uuidShape matches the canonical 36-char UUID string. We do not pull
// in google/uuid: the token is a wire-opaque string in the
// adapter-private ID encoding, and pgx encodes/decodes it via
// PostgreSQL's UUID::text cast. Pre-compiled so MarkSent / MarkFailed /
// Terminate stay allocation-free in their parse path.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Config wires a Store. Both fields are required; NewStore rejects nils.
type Config struct {
	// Pool is the connection pool used by Fetch / MarkSent / MarkFailed
	// / Terminate. Stage does NOT use it — Stage uses the caller's tx
	// pulled out of ctx via pgxdb.TxFromContext.
	Pool *pgxpool.Pool
	// Codec encodes domain events into payload + headers at Stage
	// time. The same codec (or one with an equivalent registry) must
	// drive the Relay that drains this Store; mismatch routes records
	// through Relay's fail() at drain time.
	Codec eventbus.Codec
}

// Store is the pgx/Postgres-backed Outbox + OutboxStore +
// DeadLetterRecorder.
type Store struct {
	pool       *pgxpool.Pool
	codec      eventbus.Codec
	claimLease time.Duration
}

// NewStore validates cfg, applies opts, and returns a ready Store.
// Returns an error when Pool or Codec is nil.
func NewStore(cfg Config, opts ...Option) (*Store, error) {
	switch {
	case cfg.Pool == nil:
		return nil, errors.New("eventbus/outbox/pgx: Pool is required")
	case cfg.Codec == nil:
		return nil, errors.New("eventbus/outbox/pgx: Codec is required")
	}
	s := &Store{
		pool:       cfg.Pool,
		codec:      cfg.Codec,
		claimLease: defaultClaimLease,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Stage encodes each event and INSERTs a row per event into
// outbox_records using the pgx.Tx carried by ctx. Returns ErrNoTx
// when ctx has no tx — Stage refuses silent autocommit because the
// core docstring requires it to participate in the caller's
// transaction.
//
// All marshalling happens before the INSERT so a single bad event
// aborts the batch without partial DB state (the caller's tx rollback
// is the final cleanup, but the early return keeps the rollback
// cheap). Empty events slice is a no-op.
//
// EventName / AggregateID / AggregateType are read from the
// domain.DomainEvent interface methods rather than from codec
// metadata so a buggy codec cannot silently drop those columns
// (matches memory adapter decision #23).
func (s *Store) Stage(ctx context.Context, topic string, events ...domain.DomainEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, ok := pgxdb.TxFromContext(ctx)
	if !ok {
		return ErrNoTx
	}

	type staged struct {
		eventID       string
		eventName     string
		aggregateID   string
		aggregateType string
		payload       []byte
		headers       []byte // pre-marshalled JSONB
	}

	pending := make([]staged, 0, len(events))
	for _, ev := range events {
		msg, err := s.codec.Marshal(ctx, ev)
		if err != nil {
			return fmt.Errorf("eventbus/outbox/pgx: marshal %s: %w", ev.EventID(), err)
		}
		headers := make(map[string]string, len(msg.Metadata))
		for k, v := range msg.Metadata {
			headers[k] = v
		}
		headersJSON, err := json.Marshal(headers)
		if err != nil {
			return fmt.Errorf("eventbus/outbox/pgx: marshal headers for %s: %w", ev.EventID(), err)
		}
		pending = append(pending, staged{
			eventID:       ev.EventID(),
			eventName:     ev.EventName(),
			aggregateID:   ev.AggregateID(),
			aggregateType: ev.AggregateType(),
			payload:       msg.Payload,
			headers:       headersJSON,
		})
	}

	// Build one multi-VALUES INSERT. clock_timestamp() per row means
	// back-to-back rows in the batch can carry distinct created_at /
	// available_at values; Fetch's ORDER BY (available_at, id)
	// remains stable even when timestamps tie because id is the
	// tiebreak.
	placeholders := make([]string, 0, len(pending))
	args := make([]any, 0, 7*len(pending))
	for i, p := range pending {
		n := i * 7
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d, clock_timestamp(), clock_timestamp())",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7,
		))
		args = append(args, p.eventID, topic, p.eventName, p.aggregateID, p.aggregateType, p.payload, p.headers)
	}
	sql := "INSERT INTO outbox_records (" + stageColumns + ") VALUES " + strings.Join(placeholders, ", ")

	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("eventbus/outbox/pgx: insert outbox rows: %w", err)
	}
	return nil
}

// Fetch claims up to limit due rows under FOR UPDATE SKIP LOCKED,
// stamps a fresh claim_token + lease window on each, and returns
// them as OutboxRecord values. OutboxRecord.ID is encoded as
// "<dbid>:<UUID>" so MarkSent / MarkFailed / Terminate can recover
// both halves and guard on the token (decision #21).
func (s *Store) Fetch(ctx context.Context, limit int) ([]eventbus.OutboxRecord, error) {
	if limit <= 0 {
		return nil, nil
	}

	leaseArg := fmt.Sprintf("%d milliseconds", s.claimLease.Milliseconds())

	rows, err := s.pool.Query(ctx, fetchSQL, limit, leaseArg)
	if err != nil {
		return nil, fmt.Errorf("eventbus/outbox/pgx: fetch: %w", err)
	}
	defer rows.Close()

	out := make([]eventbus.OutboxRecord, 0, limit)
	for rows.Next() {
		var (
			dbID          int64
			tokenStr      string
			eventID       string
			topic         string
			eventName     string
			aggregateID   string
			aggregateType string
			payload       []byte
			headersJSON   []byte
			createdAt     time.Time
			availableAt   time.Time
			attempts      int
		)
		if err := rows.Scan(
			&dbID, &tokenStr, &eventID, &topic, &eventName, &aggregateID, &aggregateType,
			&payload, &headersJSON, &createdAt, &availableAt, &attempts,
		); err != nil {
			return nil, fmt.Errorf("eventbus/outbox/pgx: scan fetched row: %w", err)
		}

		headers := map[string]string{}
		if len(headersJSON) > 0 {
			if err := json.Unmarshal(headersJSON, &headers); err != nil {
				return nil, fmt.Errorf("eventbus/outbox/pgx: unmarshal headers for id=%d: %w", dbID, err)
			}
		}

		out = append(out, eventbus.OutboxRecord{
			ID:            encodeID(dbID, tokenStr),
			EventID:       eventID,
			Topic:         topic,
			EventName:     eventName,
			AggregateID:   aggregateID,
			AggregateType: aggregateType,
			Payload:       slices.Clone(payload),
			Headers:       headers,
			CreatedAt:     createdAt,
			AvailableAt:   availableAt,
			Attempts:      attempts,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventbus/outbox/pgx: iterate fetched rows: %w", err)
	}
	return out, nil
}

// MarkSent deletes the record. Zero rows affected (stale token,
// already-deleted, etc.) is silent success per decision #22.
// Malformed id (not produced by Fetch) returns ErrMalformedID.
func (s *Store) MarkSent(ctx context.Context, id string) error {
	dbID, token, err := parseID(id)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, markSentSQL, dbID, token); err != nil {
		return fmt.Errorf("eventbus/outbox/pgx: mark sent id=%d: %w", dbID, err)
	}
	return nil
}

// MarkFailed bumps attempts, sets last_error, clears the lease, and
// advances available_at to Relay's app-clock nextAttemptAt.
// Token guard means a stale worker (whose lease already expired and
// got re-claimed by another) hits zero rows affected and returns
// nil silently per decision #22.
func (s *Store) MarkFailed(ctx context.Context, id, reason string, nextAttemptAt time.Time) error {
	dbID, token, err := parseID(id)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, markFailedSQL, dbID, nextAttemptAt, reason, token); err != nil {
		return fmt.Errorf("eventbus/outbox/pgx: mark failed id=%d: %w", dbID, err)
	}
	return nil
}

// Terminate is the DLQ path. Atomic via DELETE ... RETURNING CTE:
// only the worker whose DELETE actually returned a row inserts into
// outbox_dead_letters. Stale concurrent Terminate calls see zero
// rows from `del` and INSERT zero rows. Decision #23 closes the
// duplicate-DLQ race.
func (s *Store) Terminate(ctx context.Context, id, reason string) error {
	dbID, token, err := parseID(id)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, terminateSQL, dbID, token, reason); err != nil {
		return fmt.Errorf("eventbus/outbox/pgx: terminate id=%d: %w", dbID, err)
	}
	return nil
}

// encodeID produces the adapter-private OutboxRecord.ID format.
// Format: "<dbid>:<UUID>". Memory adapter uses "mem-outbox-<n>";
// both are opaque to the core OutboxStore contract.
func encodeID(dbID int64, token string) string {
	return strconv.FormatInt(dbID, 10) + ":" + token
}

// parseID inverts encodeID. The UUID itself has no ':' so LastIndex
// is equivalent to IndexOf but slightly more defensive should the
// encoding ever grow a richer dbid format. Both halves are validated:
// dbid must be a base-10 int64 and the UUID half must match the
// canonical 36-char shape.
func parseID(id string) (int64, string, error) {
	idx := strings.LastIndex(id, ":")
	if idx <= 0 || idx == len(id)-1 {
		return 0, "", ErrMalformedID
	}
	dbID, err := strconv.ParseInt(id[:idx], 10, 64)
	if err != nil {
		return 0, "", ErrMalformedID
	}
	token := id[idx+1:]
	if !uuidShape.MatchString(token) {
		return 0, "", ErrMalformedID
	}
	return dbID, token, nil
}

var (
	_ eventbus.Outbox           = (*Store)(nil)
	_ eventbus.OutboxStore      = (*Store)(nil)
	_ outbox.DeadLetterRecorder = (*Store)(nil)
)
