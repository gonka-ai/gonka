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

// Environment variables consumed by Init. We keep our own DAPI_-prefixed
// enable flag so a process can opt in independently of the standard OTLP
// envs (which the OTel SDK also reads on its own).
const (
	envEnabled  = "DAPI_OTEL_ENABLED"
	envEndpoint = "OTEL_ENDPOINT"
	envHeaders  = "OTEL_HEADERS"
)

// Config carries process-level identity for the OTel resource.
type Config struct {
	ServiceName        string
	ServiceVersion     string
	ParticipantAddress string
}

// noopShutdown is returned when observability is disabled or misconfigured.
// Callers can always defer the returned shutdown without nil checks.
func noopShutdown(context.Context) error { return nil }

// Init wires global OTel providers (tracer + meter) for the process and sets
// the W3C TraceContext propagator. Returns a shutdown function that flushes
// pending data; safe to call even when observability is disabled.
//
// When DAPI_OTEL_ENABLED is unset/false, Init is a no-op: the W3C propagator
// is still installed (so trace context flows through the process even without
// an exporter) but providers stay at their default no-op implementation.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if !otelEnabled() {
		logObservabilityInfo("init.disabled", "OpenTelemetry disabled", "env", envEnabled)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return noopShutdown, nil
	}

	endpoint := otlpEndpoint()
	if endpoint == "" {
		logObservabilityWarn("init.endpoint_missing",
			"OpenTelemetry enabled but endpoint is empty; observability will stay disabled",
			"env", envEndpoint)
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return noopShutdown, nil
	}

	otel.SetTextMapPropagator(propagation.TraceContext{})

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, logObservabilityError("init.resource_failed", "Failed to build OTel resource", err, "endpoint", endpoint)
	}

	headers := otlpHeaders()
	traceExp, err := otlptracegrpc.New(ctx, traceExporterOptions(endpoint, headers)...)
	if err != nil {
		return nil, logObservabilityError("init.trace_exporter_failed", "Failed to create OTLP trace exporter", err, "endpoint", endpoint)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)

	logObservabilityInfo("init.ready", "OpenTelemetry initialized (traces only; metrics are exposed via Prometheus /metrics)",
		"endpoint", endpoint,
		"headers_configured", len(headers) > 0,
		"service.name", valueOrDefault(cfg.ServiceName, ServiceName),
	)

	return func(shutdownCtx context.Context) error {
		err := tracerProvider.Shutdown(shutdownCtx)
		if err != nil {
			logObservabilityError("shutdown.failed", "Failed to shutdown OTel providers", err)
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
			attribute.String("participant.address", cfg.ParticipantAddress),
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
		logObservabilityWarn("config.invalid_enabled",
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
		logObservabilityWarn("config.invalid_header", "Skipping malformed OTLP header", "raw", pair)
	})
}

func traceExporterOptions(endpoint string, headers map[string]string) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return opts
}
