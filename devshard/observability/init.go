package observability

import (
	"context"
	"time"

	commonobs "common/observability"
)

// Devshardd is a child process: many instances live behind versiond on the
// same host. We keep its OTel surface trace-only (metrics flow via Prometheus
// scrape on /metrics) to avoid metric churn when versions roll over.
const (
	envEnabled = "DEVSHARD_OTEL_ENABLED"
)

// Config is the process-level identity recorded on every span.
type Config struct {
	ServiceName    string
	ServiceVersion string
}

// Init wires the global OTel tracer provider for devshardd. Returns a shutdown
// callable that flushes pending spans; safe to defer even when disabled.
//
// W3C TraceContext propagator is installed in either case so trace ids flow
// through the binary even with the exporter disabled.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	serviceName := valueOrDefault(cfg.ServiceName, ServiceName)
	result, err := commonobs.Init(ctx, commonobs.Config{
		ServiceName:    serviceName,
		ServiceVersion: cfg.ServiceVersion,
		EnabledEnv:     envEnabled,
		BatchTimeout:   5 * time.Second,
		OnMalformedHeader: func(pair string) {
			logWarn("config.invalid_header", "Skipping malformed OTLP header", "raw", pair)
		},
		LogInfo:  logInfo,
		LogWarn:  logWarn,
		LogError: logError,
	})
	if err != nil {
		return nil, err
	}
	// Host lifecycle Prometheus series are only meaningful in devshardd.
	if result.Ready && serviceName == ServiceName {
		ensureMetrics()
		SetBuildInfo(serviceName, valueOrDefault(cfg.ServiceVersion, "unknown"), "")
	}
	return result.Shutdown, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
