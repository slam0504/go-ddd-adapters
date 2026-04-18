package otel

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/slam0504/go-ddd-core/ports/observability"
)

// Config drives Provider construction.
//
// ServiceName is required. Exporters and readers are optional: when omitted,
// the corresponding TracerProvider / MeterProvider is constructed without a
// batcher / reader, making span and metric emission a no-op. This is the
// expected mode in tests and local development where a collector is not
// available.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	SpanExporter sdktrace.SpanExporter
	MetricReader sdkmetric.Reader

	// Propagator overrides the default. When nil, a composite of W3C
	// TraceContext + Baggage is installed.
	Propagator propagation.TextMapPropagator

	// ExtraResource appends arbitrary attributes onto the OTel Resource.
	ExtraResource []attribute.KeyValue
}

// Provider implements observability.Provider over the OpenTelemetry Go SDK.
type Provider struct {
	tp   *sdktrace.TracerProvider
	mp   *sdkmetric.MeterProvider
	prop propagation.TextMapPropagator
}

// New builds a Provider from the supplied Config.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.ServiceName == "" {
		return nil, errors.New("otel: Config.ServiceName is required")
	}

	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironment(cfg.Environment))
	}
	attrs = append(attrs, cfg.ExtraResource...)

	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return nil, fmt.Errorf("otel: build resource: %w", err)
	}

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.SpanExporter != nil {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(cfg.SpanExporter))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)

	mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if cfg.MetricReader != nil {
		mpOpts = append(mpOpts, sdkmetric.WithReader(cfg.MetricReader))
	}
	mp := sdkmetric.NewMeterProvider(mpOpts...)

	prop := cfg.Propagator
	if prop == nil {
		prop = propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)
	}

	return &Provider{tp: tp, mp: mp, prop: prop}, nil
}

// Tracer returns a tracer scoped to name. opts are passed through verbatim.
func (p *Provider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return p.tp.Tracer(name, opts...)
}

// Meter returns a meter scoped to name.
func (p *Provider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return p.mp.Meter(name, opts...)
}

// TextMapPropagator returns the configured propagator.
func (p *Provider) TextMapPropagator() propagation.TextMapPropagator {
	return p.prop
}

// Shutdown flushes and shuts down the underlying TracerProvider and
// MeterProvider. Both are always attempted; the first error encountered is
// returned (subsequent errors are joined onto it via errors.Join).
func (p *Provider) Shutdown(ctx context.Context) error {
	tperr := p.tp.Shutdown(ctx)
	mperr := p.mp.Shutdown(ctx)
	return errors.Join(tperr, mperr)
}

var _ observability.Provider = (*Provider)(nil)
