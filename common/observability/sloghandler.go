package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/trace"
)

// ContextFieldsFunc extracts slog attributes from a request context. Each
// service module can RegisterContextFields without common importing it.
// request_id is registered once in this package (see requestid_fields.go).
type ContextFieldsFunc func(context.Context) []slog.Attr

var (
	contextFieldMu    sync.RWMutex
	contextFieldHooks []ContextFieldsFunc
)

// RegisterContextFields appends a hook that TraceHandler runs on every record.
// Safe for concurrent use; intended to be called from package init.
func RegisterContextFields(fn ContextFieldsFunc) {
	if fn == nil {
		return
	}
	contextFieldMu.Lock()
	contextFieldHooks = append(contextFieldHooks, fn)
	contextFieldMu.Unlock()
}

// TraceHandler wraps a slog.Handler and stamps trace_id/span_id from the
// active span in ctx, plus any attributes from registered context-field hooks.
type TraceHandler struct {
	inner slog.Handler
}

// NewTraceHandler wraps inner. If inner is nil, a text handler on stderr is used.
func NewTraceHandler(inner slog.Handler) *TraceHandler {
	if inner == nil {
		inner = slog.NewTextHandler(os.Stderr, nil)
	}
	return &TraceHandler{inner: inner}
}

func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	contextFieldMu.RLock()
	hooks := contextFieldHooks
	contextFieldMu.RUnlock()
	for _, fn := range hooks {
		r.AddAttrs(fn(ctx)...)
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{inner: h.inner.WithGroup(name)}
}

var installedLogFormat atomic.Value // string: "json" or "text"

// InstallLogger builds a JSON or text slog handler, wraps it with TraceHandler,
// and installs it as the process default. format is "json" or "text" (default);
// empty / unknown values keep text so local-dev output stays unchanged.
func InstallLogger(format string) {
	normalized := "text"
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var inner slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		normalized = "json"
		inner = slog.NewJSONHandler(os.Stderr, opts)
	default:
		inner = slog.NewTextHandler(os.Stderr, opts)
	}
	installedLogFormat.Store(normalized)
	slog.SetDefault(slog.New(NewTraceHandler(inner)))
}

// LogFormat returns the format last installed by InstallLogger ("json" or
// "text"). Defaults to "text" when InstallLogger has not been called.
func LogFormat() string {
	if v, ok := installedLogFormat.Load().(string); ok && v != "" {
		return v
	}
	return "text"
}

// IsJSONLogFormat reports whether InstallLogger selected JSON output.
func IsJSONLogFormat() bool {
	return LogFormat() == "json"
}
