// Package runtime holds the wiring helpers shared across the three cmd
// binaries. Living under cmd/internal restricts visibility to siblings,
// so the helpers can stay opinionated for the example without becoming
// part of any public surface.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	rt "runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/slam0504/go-ddd-core/bootstrap"
	"github.com/slam0504/go-ddd-core/eventbus"
	"github.com/slam0504/go-ddd-core/ports/logger"

	"github.com/slam0504/go-ddd-adapters/eventbus/kafka"
	"github.com/slam0504/go-ddd-adapters/examples/orders/infra/otelexp"
	otelad "github.com/slam0504/go-ddd-adapters/observability/otel"
)

// Recover is a defer-able panic guard for goroutines. Without it a panic
// inside a long-running consumer or HTTP loop crashes the whole process,
// which is rarely what you want.
//
//	go func() {
//	    defer runtime.Recover(ctx, log, "consumer:orders.placed")
//	    ...
//	}()
func Recover(ctx context.Context, log logger.Logger, where string) {
	r := recover()
	if r == nil {
		return
	}
	log.Log(ctx, logger.LevelError, "panic recovered",
		logger.F("where", where),
		logger.F("recover", fmt.Sprint(r)),
		logger.F("stack", string(rt.Stack())))
}

// RequiredEnv reads key from the environment and returns an error when
// it is empty. Use for configuration without which the cmd cannot start.
func RequiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("env %s is required", key)
	}
	return v, nil
}

// EnvOr returns os.Getenv(key) or fallback when empty.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// BrokersFromEnv reads KAFKA_BROKERS as a comma-separated list. Empty
// value yields an error so callers fail fast at startup.
func BrokersFromEnv() ([]string, error) {
	v, err := RequiredEnv("KAFKA_BROKERS")
	if err != nil {
		return nil, err
	}
	return strings.Split(v, ","), nil
}

// OTelProvider builds an observability/otel Provider with an optional
// OTLP/gRPC span exporter. Empty otlpEndpoint produces a no-op provider,
// useful for local runs without a collector.
func OTelProvider(ctx context.Context, serviceName, otlpEndpoint string) (*otelad.Provider, error) {
	cfg := otelad.Config{ServiceName: serviceName}
	if otlpEndpoint != "" {
		exp, err := otelexp.New(ctx, otlpEndpoint)
		if err != nil {
			return nil, fmt.Errorf("otel exporter: %w", err)
		}
		cfg.SpanExporter = exp
	}
	prov, err := otelad.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("otel provider: %w", err)
	}
	return prov, nil
}

// OTelModule wraps prov.Shutdown into a bootstrap module so the lifecycle
// flushes and closes both tracer and meter providers on stop.
func OTelModule(prov *otelad.Provider) bootstrap.ModuleFunc {
	return bootstrap.ModuleFunc{
		ModuleName: "otel",
		StopFn:     prov.Shutdown,
	}
}

// HTTPModule wraps an http.Server in a bootstrap module. The server runs
// in a goroutine on Start (with panic recovery) and gracefully shuts down
// on Stop. The module name embeds addr so concurrent servers are
// distinguishable in logs.
func HTTPModule(addr string, handler http.Handler, log logger.Logger) bootstrap.ModuleFunc {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	name := "http:" + addr
	return bootstrap.ModuleFunc{
		ModuleName: name,
		StartFn: func(ctx context.Context, _ *bootstrap.App) error {
			log.Log(ctx, logger.LevelInfo, "http listening", logger.F("addr", addr))
			go func() {
				defer Recover(ctx, log, name)
				if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Log(ctx, logger.LevelError, "http serve error", logger.F("err", err.Error()))
				}
			}()
			return nil
		},
		StopFn: server.Shutdown,
	}
}

// ConsumerModule subscribes to one topic and runs handle() on each
// envelope. The goroutine exits when the subscriber channel closes —
// typically on Subscriber.Close or ctx cancel. When wg is non-nil the
// helper coordinates Add/Done so the caller can wait on Stop.
func ConsumerModule(
	ctx context.Context,
	sub *kafka.Subscriber,
	topic string,
	log logger.Logger,
	wg *sync.WaitGroup,
	handle func(env eventbus.Envelope),
) bootstrap.ModuleFunc {
	name := "consumer:" + topic
	return bootstrap.ModuleFunc{
		ModuleName: name,
		StartFn: func(_ context.Context, _ *bootstrap.App) error {
			ch, err := sub.Subscribe(ctx, topic)
			if err != nil {
				return fmt.Errorf("subscribe %s: %w", topic, err)
			}
			log.Log(ctx, logger.LevelInfo, "consumer started", logger.F("topic", topic))
			if wg != nil {
				wg.Add(1)
			}
			go func() {
				if wg != nil {
					defer wg.Done()
				}
				defer Recover(ctx, log, name)
				for env := range ch {
					handle(env)
				}
			}()
			return nil
		},
	}
}
