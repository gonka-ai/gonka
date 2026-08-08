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

	"common/observability/otelutil"
)

const (
	defaultEndpointEnv = "OTEL_ENDPOINT"
	defaultHeadersEnv  = "OTEL_HEADERS"
)

// Config is the process-level identity and env contract for OTel bootstrap.
// EnabledEnv is required and service-specific (DEVSHARD_OTEL_ENABLED,
// DAPI_OTEL_ENABLED, EDGE_API_OTEL_ENABLED, …).
type Config struct {
	ServiceName    string
	ServiceVersion string
	EnabledEnv     string
	// EndpointEnv defaults to OTEL_ENDPOINT when empty.
	EndpointEnv string
	// HeadersEnv defaults to OTEL_HEADERS when empty.
	HeadersEnv string
	// ExtraAttributes are merged into the resource alongside service.name/version.
	ExtraAttributes []attribute.KeyValue
	// BatchTimeout is passed to the span batcher; zero keeps the SDK default.
	BatchTimeout time.Duration
	// OnMalformedHeader reports a bad OTEL_HEADERS pair as (reason, key).
	// key is set only when the '=' separator was present and the key side is
	// non-empty; the raw segment and values are never passed (they may hold
	// secrets). nil ignores malformed pairs.
	OnMalformedHeader func(reason, key string)

	LogInfo  func(event, message string, args ...any)
	LogWarn  func(event, message string, args ...any)
	LogError func(event, message string, err error, args ...any) error
}

func noopShutdown(context.Context) error { return nil }

// InitResult describes the outcome of Init.
type InitResult struct {
	// Shutdown flushes pending spans; safe to defer even when !Ready.
	Shutdown func(context.Context) error
	// Ready is true when an OTLP exporter was wired (not merely the propagator).
	Ready bool
}

// Init installs the W3C TraceContext propagator and, when enabled, an OTLP
// gRPC tracer provider. Returns a result whose Shutdown is always non-nil.
func Init(ctx context.Context, cfg Config) (InitResult, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	enabledEnv := strings.TrimSpace(cfg.EnabledEnv)
	if !otelEnabled(cfg) {
		logInfo(cfg, "init.disabled", "OpenTelemetry disabled", "env", enabledEnv)
		return InitResult{Shutdown: noopShutdown}, nil
	}

	endpointEnv := valueOrDefault(strings.TrimSpace(cfg.EndpointEnv), defaultEndpointEnv)
	endpoint := strings.TrimSpace(os.Getenv(endpointEnv))
	if endpoint == "" {
		logWarn(cfg, "init.endpoint_missing",
			"OpenTelemetry enabled but endpoint is empty; observability will stay disabled",
			"env", endpointEnv)
		return InitResult{Shutdown: noopShutdown}, nil
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return InitResult{}, logError(cfg, "init.resource_failed", "Failed to build OTel resource", err, "endpoint", endpoint)
	}

	headersEnv := valueOrDefault(strings.TrimSpace(cfg.HeadersEnv), defaultHeadersEnv)
	headers := otelutil.ParseHeaders(os.Getenv(headersEnv), cfg.OnMalformedHeader)
	exporter, err := otlptracegrpc.New(ctx, traceExporterOptions(endpoint, headers)...)
	if err != nil {
		return InitResult{}, logError(cfg, "init.trace_exporter_failed", "Failed to create OTLP trace exporter", err, "endpoint", endpoint)
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}
	if cfg.BatchTimeout > 0 {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(cfg.BatchTimeout)))
	} else {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exporter))
	}
	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	logInfo(cfg, "init.ready", "OpenTelemetry initialized",
		"endpoint", endpoint,
		"headers_configured", len(headers) > 0,
		"service.name", valueOrDefault(cfg.ServiceName, "unknown"),
		"service.version", cfg.ServiceVersion,
	)

	return InitResult{
		Shutdown: func(shutdownCtx context.Context) error {
			err := errors.Join(tp.Shutdown(shutdownCtx))
			if err != nil {
				_ = logError(cfg, "shutdown.failed", "Failed to shutdown OTel tracer provider", err)
			}
			return err
		},
		Ready: true,
	}, nil
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", valueOrDefault(cfg.ServiceName, "unknown")),
		attribute.String("service.version", valueOrDefault(cfg.ServiceVersion, "unknown")),
	}
	attrs = append(attrs, cfg.ExtraAttributes...)
	return resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(attrs...),
	)
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func otelEnabled(cfg Config) bool {
	env := strings.TrimSpace(cfg.EnabledEnv)
	if env == "" {
		return false
	}
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		logWarn(cfg, "config.invalid_enabled",
			"Invalid OpenTelemetry enabled flag; observability will stay disabled",
			"env", env, "value", raw)
		return false
	}
	return enabled
}

func traceExporterOptions(endpoint string, headers map[string]string) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	return opts
}

func logInfo(cfg Config, event, message string, args ...any) {
	if cfg.LogInfo != nil {
		cfg.LogInfo(event, message, args...)
	}
}

func logWarn(cfg Config, event, message string, args ...any) {
	if cfg.LogWarn != nil {
		cfg.LogWarn(event, message, args...)
	}
}

func logError(cfg Config, event, message string, err error, args ...any) error {
	if cfg.LogError != nil {
		return cfg.LogError(event, message, err, args...)
	}
	return err
}
