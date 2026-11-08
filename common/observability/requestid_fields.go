package observability

import (
	"context"
	"log/slog"
)

func init() {
	// Stamp request_id on every slog record that carries one in ctx. Both
	// production dapi and mock-dapi (and any other common TraceHandler user)
	// get the same Loki join key without per-binary registration.
	RegisterContextFields(func(ctx context.Context) []slog.Attr {
		if id, ok := RequestID(ctx); ok {
			return []slog.Attr{slog.String("request_id", id)}
		}
		return nil
	})
}
