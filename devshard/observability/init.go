package observability

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"devshard/observability/otelutil"
)

// Devshardd is a child process: many instances live behind versiond on the
// same host. We keep its OTel surface trace-only (metrics flow via Prometheus
// scrape on /metrics) to avoid metric churn when versions roll over.
const (
	envEnabled  = "DEVSHARD_OTEL_ENABLED"
	envEndpoint = "OTEL_ENDPOINT"
	envHeaders  = "OTEL_HEADERS"
)

// Config is the process-level identity recorded on every span.
type Config struct {
	ServiceName    string
	ServiceVersion string
}

func noopShutdown(context.Context) error { return nil }

// Init wires the global OTel tracer provider for devshardd. Returns a shutdown
// callable that flushes pending spans; safe to defer even when disabled.
//
// W3C TraceContext propagator is installed in either case so trace ids flow
// through the binary even with the exporter disabled.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if !otelEnabled() {
		logInfo("init.disabled", "OpenTelemetry disabled", "env", envEnabled)
		return noopShutdown, nil
	}

	endpoint := otlpEndpoint()
	if endpoint == "" {
		logWarn("init.endpoint_missing",
			"OpenTelemetry enabled but endpoint is empty; observability will stay disabled",
			"env", envEndpoint)
		return noopShutdown, nil
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, logError("init.resource_failed", "Failed to build OTel resource", err, "endpoint", endpoint)
	}

	headers := otlpHeaders()
	exporter, err := otlptracegrpc.New(ctx, traceExporterOptions(endpoint, headers)...)
	if err != nil {
		return nil, logError("init.trace_exporter_failed", "Failed to create OTLP trace exporter", err, "endpoint", endpoint)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Pre-register lifecycle metrics so /metrics shows zero-valued series
	// even before the first event is emitted.
	ensureMetrics()
	SetBuildInfo("devshardd", valueOrDefault(cfg.ServiceVersion, "unknown"), "")

	logInfo("init.ready", "OpenTelemetry initialized",
		"endpoint", endpoint,
		"headers_configured", len(headers) > 0,
		"service.name", valueOrDefault(cfg.ServiceName, ServiceName),
		"service.version", cfg.ServiceVersion,
	)

	return func(shutdownCtx context.Context) error {
		err := errors.Join(tp.Shutdown(shutdownCtx))
		if err != nil {
			logError("shutdown.failed", "Failed to shutdown OTel tracer provider", err)
		}
		return err
	}, nil
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	return resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", valueOrDefault(cfg.ServiceName, ServiceName)),
			attribute.String("service.version", valueOrDefault(cfg.ServiceVersion, "unknown")),
		),
	)
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
	if err != nil {
		logWarn("config.invalid_enabled",
			"Invalid OpenTelemetry enabled flag; observability will stay disabled",
			"env", envEnabled, "value", raw)
		return false
	}
	return enabled
}

func otlpEndpoint() string {
	return strings.TrimSpace(os.Getenv(envEndpoint))
}

func otlpHeaders() map[string]string {
	return otelutil.ParseHeaders(os.Getenv(envHeaders), func(pair string) {
		logWarn("config.invalid_header", "Skipping malformed OTLP header", "raw", pair)
	})
}

func traceExporterOptions(endpoint string, headers map[string]string) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return opts
}
