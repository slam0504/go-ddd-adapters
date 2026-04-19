// Command api is the command-side HTTP service for the orders example.
// POST /orders accepts a place-order request, dispatches the command,
// persists to memrepo, and publishes OrderPlaced to Kafka.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	brokers, err := runtime.BrokersFromEnv()
	if err != nil {
		return err
	}

	prov, err := runtime.OTelProvider(ctx, serviceName, os.Getenv("OTEL_OTLP_ENDPOINT"))
	if err != nil {
		return err
	}

	pub, err := newPublisher(brokers)
	if err != nil {
		return err
	}

	cmdBus := registerCommands(memrepo.NewOrderRepository(), pub)

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		runtime.OTelModule(prov),
		closer("kafka-publisher", pub.Close),
		runtime.HTTPModule(runtime.EnvOr("HTTP_ADDR", defaultHTTPAddr), routes(cmdBus, log), log),
	)
	return app.Run(ctx)
}

func newPublisher(brokers []string) (*kafka.Publisher, error) {
	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                eventcodec.New(),
		PartitionByAggregate: true,
	})
	if err != nil {
		return nil, fmt.Errorf("kafka publisher: %w", err)
	}
	return pub, nil
}

func registerCommands(repo *memrepo.OrderRepository, pub eventbus.Publisher) *command.InMemoryBus {
	bus := command.NewInMemoryBus()
	command.Register[apporder.PlaceOrderCommand, apporder.PlaceOrderResult](
		bus,
		apporder.NewPlaceOrderHandler(repo, pub, topicPlaced, uuid.NewString),
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

func closer(name string, fn func() error) bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: name,
		StopFn:     func(_ context.Context) error { return fn() },
	}
}
