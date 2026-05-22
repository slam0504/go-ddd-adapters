// Command worker subscribes to OrderPlaced events and dispatches the ship
// command. The ship command loads the aggregate from the shared Postgres
// (api's commit is durable by the time relay publishes the event),
// transitions it to shipped, and stages OrderShipped via the outbox in
// the same transaction.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/application"
	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/pgxrepo"
	"github.com/slam0504/go-ddd-adapters/examples/orders/workerflow"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

const (
	topicPlaced   = "orders.placed"
	topicShipped  = "orders.shipped"
	consumerGroup = "orders-worker"
	serviceName   = "orders-worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).With(logger.F("service", serviceName))

	brokers, err := runtime.BrokersFromEnv()
	if err != nil {
		return err
	}

	dbURL, err := runtime.RequiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()

	codec := eventcodec.New()
	store, err := pgxoutbox.NewStore(pgxoutbox.Config{Pool: pool, Codec: codec})
	if err != nil {
		return fmt.Errorf("pgxoutbox store: %w", err)
	}

	txMgr := pgxdb.NewTxManager(pool)
	uow := application.UnitOfWorkFromTxManager(txMgr)
	repo := pgxrepo.New(pool)

	sub, err := newSubscriber(brokers, codec)
	if err != nil {
		return err
	}

	cmdBus := registerCommands(uow, repo, store)

	// kafka.ConsumerModule's handle returns error — nil means Ack,
	// non-nil means Nack. Do NOT call env.Ack/Nack manually; the
	// adapter owns ack lifecycle based on the return value.
	handle := func(hctx context.Context, env eventbus.Envelope) error {
		return workerflow.HandleOrderPlaced(hctx, log, cmdBus, env)
	}

	// Stop order (reverse Use): ConsumerModule drains in-flight handlers
	// -> SubscriberModule closes the watermill subscriber -> otelad
	// flushes + shuts down.
	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		kafka.SubscriberModule(sub),
		kafka.ConsumerModule(sub, topicPlaced, log, handle),
	)
	return app.Run(ctx)
}

func newSubscriber(brokers []string, codec eventbus.Codec) (*kafka.Subscriber, error) {
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       brokers,
		ConsumerGroup: consumerGroup,
		Codec:         codec,
	})
	if err != nil {
		return nil, fmt.Errorf("kafka subscriber: %w", err)
	}
	return sub, nil
}

func registerCommands(uow application.UnitOfWork, repo orderdom.Repository, outbox eventbus.Outbox) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(uow, repo, outbox, topicShipped, uuid.NewString),
	)
	return bus
}
