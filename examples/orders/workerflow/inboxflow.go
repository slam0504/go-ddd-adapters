package workerflow

import (
	"context"
	"errors"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/database"
	"github.com/slam0504/go-ddd-core/ports/logger"

	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

// ConsumerName is the inbox consumer identity for the orders worker. One
// stable name per logical consumer — see eventbus.InboxKey.
const ConsumerName = "orders-worker"

// JoiningUnitOfWork adapts txMgr into an application.UnitOfWork that JOINS
// an ambient pgx transaction when ctx carries one (pgxdb.TxFromContext) and
// only opens a new transaction otherwise. The worker wraps each message in
// one outer transaction; ShipOrderHandler's internal uow.Do then joins that
// transaction instead of nesting a second one — pgxdb.TxManager.WithinTx
// always calls pool.BeginTx and never joins by itself (confirmed constraint,
// spec §5.1). Core is deliberately untouched.
func JoiningUnitOfWork(txMgr database.TxManager) application.UnitOfWork {
	return application.UnitOfWorkFunc(func(ctx context.Context, fn func(ctx context.Context) error) error {
		if _, ok := pgxdb.TxFromContext(ctx); ok {
			return fn(ctx)
		}
		return txMgr.WithinTx(ctx, database.TxFunc(fn))
	})
}

// HandleOrderPlacedOnce wraps HandleOrderPlaced in ONE per-message
// transaction with durable inbox dedup:
//
//	Seen (true → ack, skip handler)
//	  → HandleOrderPlaced (ship side effects + outbox.Stage join via
//	    JoiningUnitOfWork)
//	  → Record (duplicate → ErrAlreadyRecorded → whole tx rolls back)
//
// A rolled-back duplicate and a Seen fast-path both return nil so the
// broker acks: the event was already fully processed once.
func HandleOrderPlacedOnce(
	ctx context.Context,
	log logger.Logger,
	txMgr database.TxManager,
	ibx eventbus.Inbox,
	cmdBus *command.InMemoryBus,
	env eventbus.Envelope,
) error {
	key := eventbus.InboxKey{Consumer: ConsumerName, EventID: env.Event.EventID()}
	var skipped bool
	err := txMgr.WithinTx(ctx, func(txCtx context.Context) error {
		seen, err := ibx.Seen(txCtx, key)
		if err != nil {
			return err
		}
		if seen {
			skipped = true
			return nil
		}
		if err := HandleOrderPlaced(txCtx, log, cmdBus, env); err != nil {
			return err
		}
		return ibx.Record(txCtx, key)
	})
	if err != nil && errors.Is(err, eventbus.ErrAlreadyRecorded) {
		logger.Info(ctx, log, "duplicate delivery acked", logger.F("event_id", key.EventID))
		return nil
	}
	if err == nil && skipped {
		logger.Info(ctx, log, "already-seen delivery acked", logger.F("event_id", key.EventID))
	}
	return err
}
