// Command worker subscribes to OrderPlaced events and dispatches the ship
// command. It demonstrates the event-driven handler chain: api publishes
// → worker consumes → publishes follow-up event for reader to project.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/memrepo"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
)

const (
	topicPlaced    = "orders.placed"
	topicShipped   = "orders.shipped"
	consumerGroup  = "orders-worker"
	serviceName    = "orders-worker"
	defaultCarrier = "FedEx"
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

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	codec := eventcodec.New()
	pub, err := newPublisher(brokers, codec)
	if err != nil {
		return err
	}
	sub, err := newSubscriber(brokers, codec)
	if err != nil {
		return err
	}

	repo := memrepo.NewOrderRepository()
	cmdBus := registerCommands(repo, pub)

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	handle := func(env eventbus.Envelope) {
		handleOrderPlaced(consumerCtx, log, cmdBus, repo, env)
	}

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		runtime.OTelModule(prov),
		bootstrap.ModuleFunc{
			ModuleName: "kafka-pubsub",
			StopFn: func(_ context.Context) error {
				cancelConsumer()
				_ = sub.Close()
				return pub.Close()
			},
		},
		runtime.ConsumerModule(consumerCtx, sub, topicPlaced, log, nil, handle),
	)
	return app.Run(ctx)
}

func newPublisher(brokers []string, codec eventbus.Codec) (*kafka.Publisher, error) {
	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		return nil, fmt.Errorf("kafka publisher: %w", err)
	}
	return pub, nil
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

func registerCommands(repo *memrepo.OrderRepository, pub eventbus.Publisher) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		bus,
		apporder.NewShipOrderHandler(repo, pub, topicShipped, uuid.NewString),
	)
	return bus
}

func handleOrderPlaced(
	ctx context.Context,
	log logger.Logger,
	cmdBus *command.InMemoryBus,
	repo orderdom.Repository,
	env eventbus.Envelope,
) {
	placed, ok := env.Event.(*orderdom.OrderPlaced)
	if !ok {
		log.Log(ctx, logger.LevelWarn, "unexpected event type", logger.F("event_name", env.Name))
		env.Nack()
		return
	}

	// Hydrate the aggregate into the worker's local repo so the ship
	// handler can FindByID it. Per-process in-mem workaround; goes away
	// with a shared DB.
	o := orderdom.Hydrate(
		orderdom.ID(placed.AggregateID()),
		placed.CustomerID,
		orderdom.StatusPlaced,
		placed.Version(),
		placed.TotalCents,
	)
	if err := repo.Save(ctx, o); err != nil {
		log.Log(ctx, logger.LevelError, "hydrate save failed",
			logger.F("order_id", placed.AggregateID()), logger.F("err", err.Error()))
		env.Nack()
		return
	}

	if _, err := cmdBus.Dispatch(ctx, apporder.ShipOrderCommand{
		OrderID: placed.AggregateID(),
		Carrier: defaultCarrier,
	}); err != nil {
		log.Log(ctx, logger.LevelError, "ship dispatch failed",
			logger.F("order_id", placed.AggregateID()), logger.F("err", err.Error()))
		env.Nack()
		return
	}

	log.Log(ctx, logger.LevelInfo, "shipped",
		logger.F("order_id", placed.AggregateID()), logger.F("carrier", defaultCarrier))
	env.Ack()
}
