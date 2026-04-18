// Command reader subscribes to OrderPlaced + OrderShipped, builds an
// in-memory projection, and serves GET /orders/{id}. It's the query side
// of the example's CQRS slice.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/slam0504/go-ddd-core/application/query"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/cmd/internal/runtime"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/projection"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
)

const (
	defaultHTTPAddr = ":8081"
	topicPlaced     = "orders.placed"
	topicShipped    = "orders.shipped"
	consumerGroup   = "orders-reader"
	serviceName     = "orders-reader"
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
	sub, err := newSubscriber(brokers, codec)
	if err != nil {
		return err
	}

	store := projection.NewOrderViewStore()
	qryBus := registerQueries(store)

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	apply := func(env eventbus.Envelope) {
		store.Apply(env.Event)
		log.Log(consumerCtx, logger.LevelDebug, "projection updated",
			logger.F("event_name", env.Name))
		env.Ack()
	}

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		runtime.OTelModule(prov),
		bootstrap.ModuleFunc{
			ModuleName: "kafka-subscriber",
			StopFn: func(_ context.Context) error {
				cancelConsumer()
				err := sub.Close()
				wg.Wait()
				return err
			},
		},
		runtime.ConsumerModule(consumerCtx, sub, topicPlaced, log, &wg, apply),
		runtime.ConsumerModule(consumerCtx, sub, topicShipped, log, &wg, apply),
		runtime.HTTPModule(runtime.EnvOr("HTTP_ADDR", defaultHTTPAddr), routes(qryBus, log), log),
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

func registerQueries(store *projection.OrderViewStore) *query.InMemoryBus {
	bus := query.NewInMemoryBus()
	query.Register[apporder.GetOrderQuery, apporder.View](bus, apporder.NewGetOrderHandler(store))
	return bus
}

func routes(qryBus *query.InMemoryBus, log logger.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		view, err := qryBus.Dispatch(r.Context(), apporder.GetOrderQuery{OrderID: r.PathValue("id")})
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			log.Log(r.Context(), logger.LevelWarn, "get order failed", logger.F("err", err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	})
	return mux
}
