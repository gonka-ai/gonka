package logging

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync/atomic"

	commonobs "common/observability"
)

// Logger is the interface for structured logging in the devshard package.
// Callers pass subsystem as a keyval: Info("applied diff", "subsystem", "state", "nonce", 5).
// When dapi integrates, it calls SetLogger() with an adapter that routes to
// dapi's configured slog handler.
type Logger interface {
	Info(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Debug(msg string, keyvals ...any)
}

var current Logger = &slogLogger{}

var structuredStages atomic.Bool

func SetLogger(l Logger) { current = l }

func Info(msg string, keyvals ...any)  { current.Info(msg, keyvals...) }
func Error(msg string, keyvals ...any) { current.Error(msg, keyvals...) }
func Warn(msg string, keyvals ...any)  { current.Warn(msg, keyvals...) }
func Debug(msg string, keyvals ...any) { current.Debug(msg, keyvals...) }

// Ctx-aware variants forward the request context so TraceHandler can stamp
// trace_id/span_id (and registered context fields such as request_id).
// An installed Logger that does not implement the context method still
// receives the line through Info/Error/Warn/Debug.
func InfoCtx(ctx context.Context, msg string, keyvals ...any) {
	if l, ok := current.(interface {
		InfoContext(context.Context, string, ...any)
	}); ok {
		l.InfoContext(ctx, msg, keyvals...)
		return
	}
	current.Info(msg, keyvals...)
}
func ErrorCtx(ctx context.Context, msg string, keyvals ...any) {
	if l, ok := current.(interface {
		ErrorContext(context.Context, string, ...any)
	}); ok {
		l.ErrorContext(ctx, msg, keyvals...)
		return
	}
	current.Error(msg, keyvals...)
}
func WarnCtx(ctx context.Context, msg string, keyvals ...any) {
	if l, ok := current.(interface {
		WarnContext(context.Context, string, ...any)
	}); ok {
		l.WarnContext(ctx, msg, keyvals...)
		return
	}
	current.Warn(msg, keyvals...)
}
func DebugCtx(ctx context.Context, msg string, keyvals ...any) {
	if l, ok := current.(interface {
		DebugContext(context.Context, string, ...any)
	}); ok {
		l.DebugContext(ctx, msg, keyvals...)
		return
	}
	current.Debug(msg, keyvals...)
}

type slogLogger struct{}

func (s *slogLogger) Info(msg string, kv ...any)  { slog.Info(msg, kv...) }
func (s *slogLogger) Error(msg string, kv ...any) { slog.Error(msg, kv...) }
func (s *slogLogger) Warn(msg string, kv ...any)  { slog.Warn(msg, kv...) }
func (s *slogLogger) Debug(msg string, kv ...any) { slog.Debug(msg, kv...) }
func (s *slogLogger) InfoContext(ctx context.Context, msg string, kv ...any) {
	slog.InfoContext(ctx, msg, kv...)
}
func (s *slogLogger) ErrorContext(ctx context.Context, msg string, kv ...any) {
	slog.ErrorContext(ctx, msg, kv...)
}
func (s *slogLogger) WarnContext(ctx context.Context, msg string, kv ...any) {
	slog.WarnContext(ctx, msg, kv...)
}
func (s *slogLogger) DebugContext(ctx context.Context, msg string, kv ...any) {
	slog.DebugContext(ctx, msg, kv...)
}

// NewSlogAdapter returns a Logger that routes to the default slog handler and
// prefixes every record with the given keyvals. Intended for embedders (e.g.
// the dapi binary) that want devshard logs to land in their slog output with
// a fixed marker like "subsystem=devshardd".
func NewSlogAdapter(prefixKV ...any) Logger {
	dup := make([]any, len(prefixKV))
	copy(dup, prefixKV)
	return &prefixedSlogLogger{prefix: dup}
}

type prefixedSlogLogger struct {
	prefix []any
}

func (p *prefixedSlogLogger) merge(kv []any) []any {
	out := make([]any, 0, len(p.prefix)+len(kv))
	out = append(out, p.prefix...)
	out = append(out, kv...)
	return out
}

func (p *prefixedSlogLogger) Info(msg string, kv ...any)  { slog.Info(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Error(msg string, kv ...any) { slog.Error(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Warn(msg string, kv ...any)  { slog.Warn(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) Debug(msg string, kv ...any) { slog.Debug(msg, p.merge(kv)...) }
func (p *prefixedSlogLogger) InfoContext(ctx context.Context, msg string, kv ...any) {
	slog.InfoContext(ctx, msg, p.merge(kv)...)
}
func (p *prefixedSlogLogger) ErrorContext(ctx context.Context, msg string, kv ...any) {
	slog.ErrorContext(ctx, msg, p.merge(kv)...)
}
func (p *prefixedSlogLogger) WarnContext(ctx context.Context, msg string, kv ...any) {
	slog.WarnContext(ctx, msg, p.merge(kv)...)
}
func (p *prefixedSlogLogger) DebugContext(ctx context.Context, msg string, kv ...any) {
	slog.DebugContext(ctx, msg, p.merge(kv)...)
}

// ContextLogger is the optional Stage path that keeps the request context, so a
// handler can stamp trace_id. Loggers that only implement Logger still receive
// the stage line through Info.
type ContextLogger interface {
	InfoContext(ctx context.Context, msg string, kv ...any)
}

// WithRequestID attaches a request ID to the context. If one already exists
// it is preserved. Optional ids[0] supplies an explicit ID (e.g. validate-*).
// Returns the (possibly new) context and the request ID.
func WithRequestID(ctx context.Context, ids ...string) (context.Context, string) {
	return commonobs.WithRequestID(ctx, ids...)
}

// SetRequestID forces id onto ctx, replacing any existing request ID.
func SetRequestID(ctx context.Context, id string) context.Context {
	return commonobs.SetRequestID(ctx, id)
}

// RequestID returns the request ID stored on ctx, if any.
func RequestID(ctx context.Context) (string, bool) {
	return commonobs.RequestID(ctx)
}

// PropagateRequestID copies the request ID from src into dst.
// Returns dst unchanged if src has no request ID.
func PropagateRequestID(dst, src context.Context) context.Context {
	return commonobs.PropagateRequestID(dst, src)
}

// Stage emits a log line in the canonical format:
//
//	request=req-... stage=some_stage key1=val1 key2=val2
//
// JSON mode (InstallLogger) emits structured slog attrs so
// TraceHandler can stamp trace_id. Text mode keeps the legacy log.Print line.
func Stage(ctx context.Context, stage string, kv ...any) {
	if structuredStages.Load() || commonobs.IsJSONLogFormat() {
		fields := stageFields(ctx, stage, kv)
		if l, ok := current.(ContextLogger); ok {
			l.InfoContext(ctx, stage, fields...)
			return
		}
		if _, isDefault := current.(*slogLogger); !isDefault {
			current.Info(stage, fields...)
			return
		}
		slog.InfoContext(ctx, stage, fields...)
		return
	}
	fields := make([]string, 0, 2+len(kv)/2)
	if id, ok := RequestID(ctx); ok {
		fields = append(fields, "request="+id)
	}
	fields = append(fields, "stage="+stage)
	for i := 0; i < len(kv); i += 2 {
		fields = append(fields, stageKey(kv, i)+"="+sanitize(stageValue(kv, i)))
	}
	log.Print(strings.Join(fields, " "))
}

func stageFields(ctx context.Context, stage string, kv []any) []any {
	fields := make([]any, 0, 4+len(kv))
	if id, ok := RequestID(ctx); ok {
		fields = append(fields, "request", id)
	}
	fields = append(fields, "stage", stage)
	for i := 0; i < len(kv); i += 2 {
		fields = append(fields, stageKey(kv, i), stageValue(kv, i))
	}
	return fields
}

func stageKey(kv []any, i int) string {
	if s, ok := kv[i].(string); ok && s != "" {
		return s
	}
	return fmt.Sprintf("field_%d", i)
}

func stageValue(kv []any, i int) string {
	if i+1 >= len(kv) {
		return "<missing>"
	}
	return fmt.Sprint(kv[i+1])
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
