// Command worker subscribes to OrderPlaced events and dispatches the ship
// command. It demonstrates the event-driven handler chain: api publishes
// → worker consumes → publishes follow-up event for reader to project.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	orderdom "github.com/slam0504/go-ddd-adapters/examples/orders/domain/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/memrepo"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/otelexp"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
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

	brokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(brokers) == 0 || brokers[0] == "" {
		return errors.New("KAFKA_BROKERS is required")
	}
	otlpEndpoint := os.Getenv("OTEL_OTLP_ENDPOINT")

	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).With(logger.F("service", serviceName))

	prov, err := buildOTelProvider(ctx, otlpEndpoint)
	if err != nil {
		return fmt.Errorf("build otel: %w", err)
	}

	codec := eventcodec.New()
	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		return fmt.Errorf("build kafka publisher: %w", err)
	}
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       brokers,
		ConsumerGroup: consumerGroup,
		Codec:         codec,
	})
	if err != nil {
		return fmt.Errorf("build kafka subscriber: %w", err)
	}

	repo := memrepo.NewOrderRepository()
	cmdBus := command.NewInMemoryBus()
	command.Register[apporder.ShipOrderCommand, apporder.ShipOrderResult](
		cmdBus,
		apporder.NewShipOrderHandler(repo, pub, topicShipped, uuid.NewString),
	)

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		bootstrap.ModuleFunc{
			ModuleName: "otel",
			StopFn:     prov.Shutdown,
		},
		bootstrap.ModuleFunc{
			ModuleName: "kafka-pubsub",
			StopFn: func(_ context.Context) error {
				cancelConsumer()
				_ = sub.Close()
				return pub.Close()
			},
		},
		bootstrap.ModuleFunc{
			ModuleName: "consumer",
			StartFn: func(_ context.Context, _ *bootstrap.App) error {
				ch, err := sub.Subscribe(consumerCtx, topicPlaced)
				if err != nil {
					return fmt.Errorf("subscribe %s: %w", topicPlaced, err)
				}
				go consume(consumerCtx, log, cmdBus, repo, ch)
				log.Log(consumerCtx, logger.LevelInfo, "consumer started", logger.F("topic", topicPlaced))
				return nil
			},
		},
	)

	return app.Run(ctx)
}

func consume(
	ctx context.Context,
	log logger.Logger,
	cmdBus *command.InMemoryBus,
	repo orderdom.Repository,
	ch <-chan eventbus.Envelope,
) {
	for env := range ch {
		placed, ok := env.Event.(*orderdom.OrderPlaced)
		if !ok {
			log.Log(ctx, logger.LevelWarn, "unexpected event type", logger.F("event_name", env.Name))
			env.Nack()
			continue
		}

		// Hydrate the aggregate into the worker's local repo so the ship
		// handler can FindByID it. This is the example's per-process
		// in-mem workaround; with a shared DB the worker would just load.
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
			continue
		}

		_, err := cmdBus.Dispatch(ctx, apporder.ShipOrderCommand{
			OrderID: placed.AggregateID(),
			Carrier: defaultCarrier,
		})
		if err != nil {
			log.Log(ctx, logger.LevelError, "ship dispatch failed",
				logger.F("order_id", placed.AggregateID()), logger.F("err", err.Error()))
			env.Nack()
			continue
		}
		log.Log(ctx, logger.LevelInfo, "shipped",
			logger.F("order_id", placed.AggregateID()), logger.F("carrier", defaultCarrier))
		env.Ack()
	}
}

func buildOTelProvider(ctx context.Context, otlpEndpoint string) (*otelad.Provider, error) {
	cfg := otelad.Config{ServiceName: serviceName}
	if otlpEndpoint != "" {
		exp, err := otelexp.New(ctx, otlpEndpoint)
		if err != nil {
			return nil, err
		}
		cfg.SpanExporter = exp
	}
	return otelad.New(ctx, cfg)
}
