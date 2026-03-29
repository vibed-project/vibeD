// Package tracing initializes OpenTelemetry distributed tracing for vibeD.
package tracing

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/vibed-project/vibeD/internal/config"
)

// Init initializes the OpenTelemetry TracerProvider.
// Returns a shutdown function that must be called on application exit.
// When tracing is disabled, sets the global tracer to a noop and returns a no-op shutdown.
func Init(cfg config.TracingConfig, logger *slog.Logger) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	ctx := context.Background()

	// Build exporter
	var exporter sdktrace.SpanExporter
	var err error

	if cfg.Endpoint != "" {
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP exporter: %w", err)
		}
		logger.Info("tracing enabled (OTLP)", "endpoint", cfg.Endpoint, "sampleRate", cfg.SampleRate)
	} else {
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("creating stdout exporter: %w", err)
		}
		logger.Info("tracing enabled (stdout)", "sampleRate", cfg.SampleRate)
	}

	// Build resource (avoid Merge with Default to prevent schema URL conflicts)
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName("vibed"),
		semconv.ServiceVersion("0.1.0"),
	)

	// Build sampler
	sampler := sdktrace.AlwaysSample()
	if cfg.SampleRate < 1.0 {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}

	// Build TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sampler)),
	)

	// Set globals
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tp.Shutdown, nil
}
