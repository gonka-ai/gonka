package logging

import (
	"context"
	"log/slog"

	commonobs "common/observability"
)

func Info(msg string, keyvals ...any)  { slog.Info(msg, keyvals...) }
func Error(msg string, keyvals ...any) { slog.Error(msg, keyvals...) }
func Warn(msg string, keyvals ...any)  { slog.Warn(msg, keyvals...) }
func Debug(msg string, keyvals ...any) { slog.Debug(msg, keyvals...) }

// Ctx-aware variants forward the request context so TraceHandler can stamp
// trace_id/span_id (and registered context fields such as request_id).
func InfoCtx(ctx context.Context, msg string, keyvals ...any) {
	slog.InfoContext(ctx, msg, keyvals...)
}
func ErrorCtx(ctx context.Context, msg string, keyvals ...any) {
	slog.ErrorContext(ctx, msg, keyvals...)
}
func WarnCtx(ctx context.Context, msg string, keyvals ...any) {
	slog.WarnContext(ctx, msg, keyvals...)
}
func DebugCtx(ctx context.Context, msg string, keyvals ...any) {
	slog.DebugContext(ctx, msg, keyvals...)
}

// WithRequestID attaches a request ID to the context. Thin alias of
// common/observability.WithRequestID so existing call sites compile unchanged.
func WithRequestID(ctx context.Context, ids ...string) (context.Context, string) {
	return commonobs.WithRequestID(ctx, ids...)
}

// SetRequestID forces id onto ctx. Thin alias of common/observability.SetRequestID.
func SetRequestID(ctx context.Context, id string) context.Context {
	return commonobs.SetRequestID(ctx, id)
}

// RequestID returns the request ID stored on ctx. Thin alias of
// common/observability.RequestID.
func RequestID(ctx context.Context) (string, bool) {
	return commonobs.RequestID(ctx)
}

// PropagateRequestID copies the request ID from src into dst. Thin alias of
// common/observability.PropagateRequestID.
func PropagateRequestID(dst, src context.Context) context.Context {
	return commonobs.PropagateRequestID(dst, src)
}

// Stage emits a correlation stage line. Thin alias of common/observability.Stage
// so existing call sites compile unchanged.
func Stage(ctx context.Context, stage string, kv ...any) {
	commonobs.Stage(ctx, stage, kv...)
}
