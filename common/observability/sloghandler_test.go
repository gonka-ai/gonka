package observability

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
