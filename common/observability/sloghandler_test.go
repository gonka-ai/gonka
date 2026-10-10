package observability

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func withContextFieldHooks(t *testing.T, hooks ...ContextFieldsFunc) {
	t.Helper()
	contextFieldMu.Lock()
	prev := contextFieldHooks
	contextFieldHooks = append([]ContextFieldsFunc(nil), hooks...)
	contextFieldMu.Unlock()
	t.Cleanup(func() {
		contextFieldMu.Lock()
		contextFieldHooks = prev
		contextFieldMu.Unlock()
	})
}

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func TestTraceHandler_RecoversPanickingContextFieldHook(t *testing.T) {
	inner := &captureHandler{}
	withContextFieldHooks(t,
		func(context.Context) []slog.Attr {
			panic("boom from hook")
		},
		func(context.Context) []slog.Attr {
			return []slog.Attr{slog.String("kept", "yes")}
		},
	)

	h := NewTraceHandler(inner)
	err := h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0))
	require.NoError(t, err)
	require.Len(t, inner.records, 1)

	attrs := map[string]string{}
	inner.records[0].Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	require.Equal(t, "yes", attrs["kept"], "later hooks must still run after a panic")
	require.Equal(t, "hello", inner.records[0].Message)
}

func TestSafeContextAttrs_NilHook(t *testing.T) {
	require.Nil(t, safeContextAttrs(nil, context.Background()))
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"trace", slog.LevelInfo},
		{" debug ", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"ERROR", slog.LevelError},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, ParseLogLevel(tc.raw), "raw %q", tc.raw)
	}
}

type messagePrefixHandler struct {
	prefix string
	inner  slog.Handler
}

func (h *messagePrefixHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *messagePrefixHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Message = "[" + h.prefix + "] " + r.Message
	return h.inner.Handle(ctx, r)
}

func (h *messagePrefixHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &messagePrefixHandler{prefix: h.prefix, inner: h.inner.WithAttrs(attrs)}
}

func (h *messagePrefixHandler) WithGroup(name string) slog.Handler {
	return &messagePrefixHandler{prefix: h.prefix, inner: h.inner.WithGroup(name)}
}

func restoreInstalledLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	prevFmt := LogFormat()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		installedLogFormat.Store(prevFmt)
	})
}

func TestInstallLoggerWithOptions_WarnDropsInfo(t *testing.T) {
	restoreInstalledLogger(t)
	var buf bytes.Buffer
	InstallLoggerWithOptions(LoggerOptions{Format: "text", Level: slog.LevelWarn, Out: &buf})

	slog.Info("hidden-info")
	slog.Warn("visible-warn")

	require.NotContains(t, buf.String(), "hidden-info")
	require.Contains(t, buf.String(), "visible-warn")
}

func TestInstallLoggerWithOptions_JSONAttrsIgnoreWrapText(t *testing.T) {
	restoreInstalledLogger(t)
	var buf bytes.Buffer
	InstallLoggerWithOptions(LoggerOptions{
		Format: "json",
		Level:  slog.LevelInfo,
		Out:    &buf,
		Attrs:  []slog.Attr{slog.String("binary_version", "0.2.13")},
		WrapText: func(slog.Handler) slog.Handler {
			t.Fatal("WrapText must be ignored in JSON mode")
			return nil
		},
	})

	slog.Info("boot")

	require.Contains(t, buf.String(), `"binary_version":"0.2.13"`)
	require.Contains(t, buf.String(), `"msg":"boot"`)
	require.NotContains(t, buf.String(), "[0.2.13]")
}

func TestInstallLoggerWithOptions_TextPrefixStampsTraceID(t *testing.T) {
	restoreInstalledLogger(t)
	var buf bytes.Buffer
	InstallLoggerWithOptions(LoggerOptions{
		Format: "text",
		Level:  slog.LevelInfo,
		Out:    &buf,
		WrapText: func(inner slog.Handler) slog.Handler {
			return &messagePrefixHandler{prefix: "v1", inner: inner}
		},
	})

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	slog.InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "started")

	out := buf.String()
	require.Contains(t, out, "[v1] started")
	require.Contains(t, out, "trace_id="+sc.TraceID().String())
}

func TestTraceHandler_HealthyHookStampsAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	withContextFieldHooks(t, func(context.Context) []slog.Attr {
		return []slog.Attr{slog.String("request_id", "req-1")}
	})

	h := NewTraceHandler(inner)
	require.NoError(t, h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "ok", 0)))
	require.Contains(t, buf.String(), "request_id=req-1")
}
