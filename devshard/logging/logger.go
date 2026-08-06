package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// Stage emits a log line via slog so TraceHandler can stamp trace_id/span_id
// from ctx.
//
// JSON mode uses structured attrs (stage + kv). Text mode keeps the legacy
// `request=… stage=… key=val` message shape so existing greps and
// WaitLokiSubstring assertions keep matching without duplicating those keys
// as attrs (TextHandler would reprint them).
func Stage(ctx context.Context, stage string, kv ...any) {
	if commonobs.IsJSONLogFormat() {
		args := make([]any, 0, 4+len(kv))
		if id, ok := RequestID(ctx); ok {
			args = append(args, "request", id)
		}
		args = append(args, "stage", stage)
		for i := 0; i < len(kv); i += 2 {
			key := fmt.Sprintf("field_%d", i)
			if s, ok := kv[i].(string); ok && s != "" {
				key = s
			}
			var value any = "<missing>"
			if i+1 < len(kv) {
				value = kv[i+1]
			}
			args = append(args, key, value)
		}
		slog.Log(ctx, slog.LevelInfo, stage, args...)
		return
	}
	slog.Log(ctx, slog.LevelInfo, formatLegacyStage(ctx, stage, kv))
}

func formatLegacyStage(ctx context.Context, stage string, kv []any) string {
	fields := make([]string, 0, 2+len(kv)/2)
	if id, ok := RequestID(ctx); ok {
		fields = append(fields, "request="+id)
	}
	fields = append(fields, "stage="+stage)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("field_%d", i)
		if s, ok := kv[i].(string); ok && s != "" {
			key = s
		}
		value := "<missing>"
		if i+1 < len(kv) {
			value = fmt.Sprint(kv[i+1])
		}
		fields = append(fields, key+"="+sanitize(value))
	}
	return strings.Join(fields, " ")
}

func sanitize(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\n\r\"") {
		return fmt.Sprintf("%q", v)
	}
	return v
}
