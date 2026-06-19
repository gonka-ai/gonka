// Package observability provides OpenTelemetry-based tracing and metrics for
// the decentralized-api process. It exposes a high-level façade (Service)
// rather than letting call sites touch OTel/Prometheus primitives directly,
// so call sites stay terse and the wiring stays in one place.
package observability

import "log/slog"

const (
	// ServiceName is the default OTel service.name and Prometheus label
	// namespace for this process.
	ServiceName = "decentralized-api"

	subsystem = "observability"
)

func eventArgs(event string, args []any) []any {
	out := make([]any, 0, len(args)+6)
	out = append(out, "service", ServiceName, "subsystem", subsystem, "event", event)
	out = append(out, args...)
	return out
}

func logObservabilityInfo(event, message string, args ...any) {
	slog.Info(message, eventArgs(event, args)...)
}

func logObservabilityWarn(event, message string, args ...any) {
	slog.Warn(message, eventArgs(event, args)...)
}

// logObservabilityError logs and returns the error so callers can chain it
// directly into a return statement.
func logObservabilityError(event, message string, err error, args ...any) error {
	if err != nil {
		args = append(args, "error", err)
	}
	slog.Error(message, eventArgs(event, args)...)
	return err
}
