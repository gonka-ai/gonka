package observability

import (
	"context"

	commonobs "common/observability"
)

const (
	ServiceName = "edge-api"
	envEnabled  = "EDGE_API_OTEL_ENABLED"
)

// Config carries process-level identity for the OTel resource.
type Config struct {
	ServiceName    string
	ServiceVersion string
}

// Init wires the global OTel tracer provider. Returns a shutdown function.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	result, err := commonobs.Init(ctx, commonobs.Config{
		ServiceName:    valueOrDefault(cfg.ServiceName, ServiceName),
		ServiceVersion: cfg.ServiceVersion,
		EnabledEnv:     envEnabled,
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
