package observability

import (
	"context"
	"log/slog"

	commonobs "common/observability"
)

func init() {
	commonobs.RegisterContextFields(func(ctx context.Context) []slog.Attr {
		if id, ok := commonobs.RequestID(ctx); ok {
			return []slog.Attr{slog.String("request_id", id)}
		}
		return nil
	})
}

// InstallLogger installs the shared TraceHandler as the process default slog
// handler. format is "json" or "text" (default); see common/observability.
func InstallLogger(format string) {
	commonobs.InstallLogger(format)
}
