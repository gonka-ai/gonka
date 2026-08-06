package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"

	commonobs "common/observability"
)

// Environment variables consumed by Init. We keep our own DAPI_-prefixed
// enable flag so a process can opt in independently of the standard OTLP
// envs (which the OTel SDK also reads on its own).
const envEnabled = "DAPI_OTEL_ENABLED"

// Config carries process-level identity for the OTel resource.
type Config struct {
	ServiceName        string
	ServiceVersion     string
	ParticipantAddress string
}

// Init wires global OTel providers (tracer) for the process and sets the W3C
// TraceContext propagator. Returns a shutdown function that flushes pending
// data; safe to call even when observability is disabled.
//
// When DAPI_OTEL_ENABLED is unset/false, Init is a no-op: the W3C propagator
// is still installed (so trace context flows through the process even without
// an exporter) but providers stay at their default no-op implementation.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	extra := []attribute.KeyValue{}
	if cfg.ParticipantAddress != "" {
		extra = append(extra, attribute.String("participant.address", cfg.ParticipantAddress))
	}
	result, err := commonobs.Init(ctx, commonobs.Config{
		ServiceName:     valueOrDefault(cfg.ServiceName, ServiceName),
		ServiceVersion:  cfg.ServiceVersion,
		EnabledEnv:      envEnabled,
		ExtraAttributes: extra,
		OnMalformedHeader: func(pair string) {
			logObservabilityWarn("config.invalid_header", "Skipping malformed OTLP header", "raw", pair)
		},
		LogInfo:  logObservabilityInfo,
		LogWarn:  logObservabilityWarn,
		LogError: logObservabilityError,
	})
	if err != nil {
		return nil, err
	}
	return result.Shutdown, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// InstallLogger installs the shared TraceHandler as the process default slog
// handler. format is "json" or "text" (default); see common/observability.
func InstallLogger(format string) {
	commonobs.InstallLogger(format)
}
