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
	"strings"
	"sync"
	"time"

	"github.com/slam0504/go-ddd-core/application/query"
	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/domain"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	apporder "github.com/slam0504/go-ddd-adapters/examples/orders/application/order"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/eventcodec"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/otelexp"
	"github.com/slam0504/go-ddd-adapters/examples/orders/projection"
	slogger "github.com/slam0504/go-ddd-adapters/logger/slogger"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
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

	codec := eventcodec.New()
	sub, err := kafka.NewSubscriber(kafka.SubscriberConfig{
		Brokers:       brokers,
		ConsumerGroup: consumerGroup,
		Codec:         codec,
	})
	if err != nil {
		return fmt.Errorf("build kafka subscriber: %w", err)
	}

	store := projection.NewOrderViewStore()
	qryBus := query.NewInMemoryBus()
	query.Register[apporder.GetOrderQuery, apporder.View](qryBus, apporder.NewGetOrderHandler(store))

	server := &http.Server{
		Addr:              httpAddr,
		Handler:           routes(qryBus, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	app := bootstrap.New(bootstrap.Options{Logger: log})
	app.Use(
		bootstrap.ModuleFunc{
			ModuleName: "otel",
			StopFn:     prov.Shutdown,
		},
		bootstrap.ModuleFunc{
			ModuleName: "kafka-subscriber",
			StopFn: func(_ context.Context) error {
				cancelConsumer()
				err := sub.Close()
				wg.Wait()
				return err
			},
		},
		bootstrap.ModuleFunc{
			ModuleName: "projection",
			StartFn: func(_ context.Context, _ *bootstrap.App) error {
				for _, topic := range []string{topicPlaced, topicShipped} {
					ch, err := sub.Subscribe(consumerCtx, topic)
					if err != nil {
						return fmt.Errorf("subscribe %s: %w", topic, err)
					}
					wg.Add(1)
					go func(t string, c <-chan eventbus.Envelope) {
						defer wg.Done()
						project(consumerCtx, log, store, t, c)
					}(topic, ch)
				}
				return nil
			},
		},
		bootstrap.ModuleFunc{
			ModuleName: "http",
			StartFn: func(_ context.Context, _ *bootstrap.App) error {
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

func project(
	ctx context.Context,
	log logger.Logger,
	store *projection.OrderViewStore,
	topic string,
	ch <-chan eventbus.Envelope,
) {
	for env := range ch {
		store.Apply(env.Event)
		log.Log(ctx, logger.LevelDebug, "projection updated",
			logger.F("topic", topic), logger.F("event_name", env.Name))
		env.Ack()
	}
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
