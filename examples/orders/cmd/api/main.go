// Command api is the command-side HTTP service for the orders example.
// POST /orders accepts a place-order request, dispatches the command,
// persists to Postgres, and stages OrderPlaced into the outbox in the
// same transaction. A separate cmd/relay drains the outbox to Kafka.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
	pgxdb "github.com/slam0504/go-ddd-adapters/ports/database/pgx"
)

const (
	defaultHTTPAddr = ":8080"
	topicPlaced     = "orders.placed"
	serviceName     = "orders-api"
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

	cmdBus := registerCommands(uow, repo, store)

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		otelad.Module(prov),
		runtime.HTTPModule(runtime.EnvOr("HTTP_ADDR", defaultHTTPAddr), routes(cmdBus, log), log),
	)
	return app.Run(ctx)
}

func registerCommands(uow application.UnitOfWork, repo orderdom.Repository, outbox eventbus.Outbox) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.PlaceOrderCommand, apporder.PlaceOrderResult](
		bus,
		apporder.NewPlaceOrderHandler(uow, repo, outbox, topicPlaced, uuid.NewString),
	)
	return bus
}

func routes(cmdBus *command.InMemoryBus, log logger.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrderID    string          `json:"order_id"`
			CustomerID string          `json:"customer_id"`
			Items      []orderdom.Item `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.OrderID == "" {
			body.OrderID = uuid.NewString()
		}
		ctx := kafka.WithCorrelationID(r.Context(), body.OrderID)
		res, err := cmdBus.Dispatch(ctx, apporder.PlaceOrderCommand{
			OrderID:    body.OrderID,
			CustomerID: body.CustomerID,
			Items:      body.Items,
		})
		if err != nil {
			log.Log(r.Context(), logger.LevelWarn, "place order failed", logger.F("err", err.Error()))
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
