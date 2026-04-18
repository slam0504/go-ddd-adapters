// Command api is the command-side HTTP service for the orders example.
// POST /orders accepts a place-order request, dispatches the command,
// persists to memrepo, and publishes OrderPlaced to Kafka.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/slam0504/go-ddd-core/application/command"
	"github.com/slam0504/go-ddd-core/bootstrap"
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

	brokers := strings.Split(os.Getenv("KAFKA_BROKERS"), ",")
	if len(brokers) == 0 || brokers[0] == "" {
		return errors.New("KAFKA_BROKERS is required")
	}
	httpAddr := envOr("HTTP_ADDR", defaultHTTPAddr)
	otlpEndpoint := os.Getenv("OTEL_OTLP_ENDPOINT")

	log := slogger.New(slogger.Config{Level: slog.LevelInfo}).With(logger.F("service", serviceName))

	prov, err := buildOTelProvider(ctx, otlpEndpoint)
	if err != nil {
		return fmt.Errorf("build otel: %w", err)
	}

	pub, err := kafka.NewPublisher(kafka.PublisherConfig{
		Brokers:              brokers,
		Codec:                eventcodec.New(),
		PartitionByAggregate: true,
	})
	if err != nil {
		return fmt.Errorf("build kafka publisher: %w", err)
	}

	repo := memrepo.NewOrderRepository()
	cmdBus := command.NewInMemoryBus()
	command.Register[apporder.PlaceOrderCommand, apporder.PlaceOrderResult](
		cmdBus,
		apporder.NewPlaceOrderHandler(repo, pub, topicPlaced, uuid.NewString),
	)

	server := &http.Server{
		Addr:              httpAddr,
		Handler:           routes(cmdBus, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		bootstrap.ModuleFunc{
			ModuleName: "otel",
			StopFn:     prov.Shutdown,
		},
		bootstrap.ModuleFunc{
			ModuleName: "kafka-publisher",
			StopFn:     func(_ context.Context) error { return pub.Close() },
		},
		bootstrap.ModuleFunc{
			ModuleName: "http",
			StartFn: func(ctx context.Context, _ *bootstrap.App) error {
				log.Log(ctx, logger.LevelInfo, "http listening", logger.F("addr", httpAddr))
				go func() {
					if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.Log(ctx, logger.LevelError, "http serve error", logger.F("err", err.Error()))
					}
				}()
				return nil
			},
			StopFn: server.Shutdown,
		},
	)

	return app.Run(ctx)
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
