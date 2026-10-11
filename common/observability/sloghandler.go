package observability

import (
	"context"
	"fmt"
	"io"
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
		if attrs := safeContextAttrs(fn, ctx); len(attrs) > 0 {
			r.AddAttrs(attrs...)
		}
	}
	return h.inner.Handle(ctx, r)
}

// safeContextAttrs runs a context-field hook and recovers panics so a bad hook
// cannot take down request logging on the slog hot path. Reports to stderr
// (not slog) to avoid re-entering TraceHandler.
func safeContextAttrs(fn ContextFieldsFunc, ctx context.Context) (attrs []slog.Attr) {
	if fn == nil {
		return nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "observability: context field hook panicked: %v\n", rec)
			attrs = nil
		}
	}()
	return fn(ctx)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{inner: h.inner.WithGroup(name)}
}

var installedLogFormat atomic.Value // string: "json" or "text"

// LoggerOptions configures the process slog handler installed by
// InstallLoggerWithOptions.
type LoggerOptions struct {
	// Format is "json" or "text". Empty and unknown values keep text.
	Format string
	// Level is the minimum slog level. The zero value is Info.
	Level slog.Level
	// WrapText decorates the text handler (for example a version prefix).
	// Ignored in JSON mode.
	WrapText func(slog.Handler) slog.Handler
	// Attrs are added to every record (for example binary_version in JSON mode).
	Attrs []slog.Attr
	// Out is the log destination. Nil writes to stderr.
	Out io.Writer
}

// ParseLogLevel reads the devshard level grammar. debug, warn/warning and
// error select those levels; unset or unknown values stay at Info so a typo
// cannot start a Debug flood.
func ParseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// InstallLogger builds a JSON or text slog handler at Info, wraps it with
// TraceHandler, and installs it as the process default. format is "json" or
// "text" (default); empty / unknown values keep text so local-dev output stays
// unchanged.
func InstallLogger(format string) {
	InstallLoggerWithOptions(LoggerOptions{Format: format, Level: slog.LevelInfo})
}

// InstallLoggerWithOptions installs the handler described by opts as the
// process default. Text mode is TraceHandler(WrapText(TextHandler)) so trace
// fields are stamped and a prefix still applies to the message. JSON mode
// ignores WrapText.
func InstallLoggerWithOptions(opts LoggerOptions) {
	normalized := "text"
	if strings.EqualFold(strings.TrimSpace(opts.Format), "json") {
		normalized = "json"
	}
	installedLogFormat.Store(normalized)
	slog.SetDefault(slog.New(loggerHandler(opts, normalized)))
}

func loggerHandler(opts LoggerOptions, normalized string) slog.Handler {
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	hopts := &slog.HandlerOptions{Level: opts.Level}
	var inner slog.Handler
	if normalized == "json" {
		inner = slog.NewJSONHandler(out, hopts)
	} else {
		inner = slog.NewTextHandler(out, hopts)
		if opts.WrapText != nil {
			inner = opts.WrapText(inner)
		}
	}
	if len(opts.Attrs) > 0 {
		inner = inner.WithAttrs(opts.Attrs)
	}
	return NewTraceHandler(inner)
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
