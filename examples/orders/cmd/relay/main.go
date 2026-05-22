// Command relay drains the pgx outbox to Kafka. It composes existing
// adapter components (pgxoutbox.Store + outbox.Relay + kafka.Publisher
// + kafka.RestoreCoreHeaders) into a standalone bootstrap binary —
// no new generic relay package is introduced.
//
// Topology invariants: relay is the only binary that publishes to
// Kafka, and the only binary that calls Outbox MarkSent / MarkFailed /
// Terminate. api and worker only Stage rows into outbox_records.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/eventbus/outbox"
	pgxoutbox "github.com/slam0504/go-ddd-adapters/eventbus/outbox/pgx"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
)

const serviceName = "orders-relay"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).
		With(logger.F("service", serviceName))

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

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                codec,
		PartitionByAggregate: true,
	})
	if err != nil {
		return fmt.Errorf("kafka publisher: %w", err)
	}

	relay, err := outbox.NewRelay(
		outbox.RelayConfig{Store: store, Publisher: pub, Codec: codec, Logger: log},
		outbox.WithHeaderRestorer(kafka.RestoreCoreHeaders),
	)
	if err != nil {
		return fmt.Errorf("new relay: %w", err)
	}

	// Stop order (reverse Use): RelayModule stops the polling loop ->
	// PublisherModule closes the watermill publisher -> otelad flushes
	// + shuts down.
	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		kafka.PublisherModule(pub),
		outbox.RelayModule(relay, log),
	)
	return app.Run(ctx)
}
