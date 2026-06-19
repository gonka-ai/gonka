// Package observability provides OpenTelemetry tracing and Prometheus metrics
// for the devshard host process (devshardd). It mirrors the design of the
// decentralized-api observability package: a small typed façade so call sites
// don't reach into OTel/Prometheus directly.
package observability

import "log/slog"

const (
	// ServiceName is the default OTel service.name and Prometheus label
	// namespace for the devshard host.
	ServiceName = "devshardd"

	subsystem = "observability"
)

func eventArgs(event string, args []any) []any {
	out := make([]any, 0, len(args)+6)
	out = append(out, "service", ServiceName, "subsystem", subsystem, "event", event)
	out = append(out, args...)
	return out
}

func logInfo(event, message string, args ...any) {
	slog.Info(message, eventArgs(event, args)...)
}

func logWarn(event, message string, args ...any) {
	slog.Warn(message, eventArgs(event, args)...)
}

func logError(event, message string, err error, args ...any) error {
	if err != nil {
		args = append(args, "error", err)
	}
	slog.Error(message, eventArgs(event, args)...)
	return err
}
