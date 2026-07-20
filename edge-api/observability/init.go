package observability

import (
	"context"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"common/observability/otelutil"
)

const (
	ServiceName = "edge-api"
	envEnabled  = "EDGE_API_OTEL_ENABLED"
	envEndpoint = "OTEL_ENDPOINT"
	envHeaders  = "OTEL_HEADERS"
)

// Config carries process-level identity for the OTel resource.
type Config struct {
	ServiceName    string
	ServiceVersion string
}

func noopShutdown(context.Context) error { return nil }

// Init wires the global OTel tracer provider. Returns a shutdown function.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if !otelEnabled() {
		return noopShutdown, nil
	}

	endpoint := strings.TrimSpace(os.Getenv(envEndpoint))
	if endpoint == "" {
		return noopShutdown, nil
	}

	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", valueOrDefault(cfg.ServiceName, ServiceName)),
			attribute.String("service.version", valueOrDefault(cfg.ServiceVersion, "unknown")),
		),
	)
	if err != nil {
		return nil, err
	}

	headers := otelutil.ParseHeaders(os.Getenv(envHeaders), nil)
	traceExp, err := otlptracegrpc.New(ctx, traceExporterOptions(endpoint, headers)...)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return func(shutdownCtx context.Context) error {
		return tp.Shutdown(shutdownCtx)
	}, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func otelEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(envEnabled))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

func traceExporterOptions(endpoint string, headers map[string]string) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return opts
}
