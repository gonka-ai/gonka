package observability

import (
	"log/slog"

	commonobs "common/observability"
)

// LoggerOptions is the shared process-logger configuration.
type LoggerOptions = commonobs.LoggerOptions

// ParseLogLevel reads DEVSHARD_LOG_LEVEL. Unset or unknown values stay at Info.
func ParseLogLevel(raw string) slog.Level {
	return commonobs.ParseLogLevel(raw)
}

// InstallLogger installs the shared TraceHandler as the process default slog
// handler at Info. format is "json" or "text" (default); see common/observability.
// request_id stamping is registered once in common/observability.
func InstallLogger(format string) {
	commonobs.InstallLogger(format)
}

// InstallLoggerWithOptions installs the shared handler with an explicit level,
// text wrapper, and record attrs.
func InstallLoggerWithOptions(opts LoggerOptions) {
	commonobs.InstallLoggerWithOptions(opts)
}
