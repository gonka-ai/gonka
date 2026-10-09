package logging

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	commonobs "common/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

type captureStageLogger struct {
	msgs []string
}

func (c *captureStageLogger) Info(msg string, _ ...any) { c.msgs = append(c.msgs, msg) }
func (c *captureStageLogger) Error(string, ...any)      {}
func (c *captureStageLogger) Warn(string, ...any)       {}
func (c *captureStageLogger) Debug(string, ...any)      {}

func TestStageJSONReachesSetLogger(t *testing.T) {
	capture := &captureStageLogger{}
	prev := current
	SetLogger(capture)
	structuredStages.Store(true)
	t.Cleanup(func() {
		SetLogger(prev)
		structuredStages.Store(false)
	})

	Stage(context.Background(), "send_completed", "host", "h1")
	require.Equal(t, []string{"send_completed"}, capture.msgs)
}

func TestStageDefaultLoggerStampsTraceID(t *testing.T) {
	var buf bytes.Buffer
	prevLogger := slog.Default()
	prevFormat := commonobs.LogFormat()
	prev := current
	SetLogger(&slogLogger{})
	commonobs.InstallLoggerWithOptions(commonobs.LoggerOptions{
		Format: "json",
		Level:  slog.LevelInfo,
		Out:    &buf,
	})
	t.Cleanup(func() {
		SetLogger(prev)
		commonobs.InstallLogger(prevFormat)
		slog.SetDefault(prevLogger)
	})

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	Stage(trace.ContextWithSpanContext(context.Background(), sc), "send_completed", "host", "h1")

	out := buf.String()
	require.Contains(t, out, sc.TraceID().String())
	require.Contains(t, out, "send_completed")
}
