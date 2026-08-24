package mockdapi_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	nodemanager "common/nodemanager"
	"common/nodemanager/gen"
	commonobs "common/observability"
	devshardobs "devshard/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// stageCapture records the structured fields of every log line, which is what
// Alloy ships to Loki and what the citests join on.
type stageCapture struct {
	mu      sync.Mutex
	records []map[string]string
}

func (h *stageCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *stageCapture) Handle(_ context.Context, r slog.Record) error {
	fields := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, fields)
	h.mu.Unlock()
	return nil
}

func (h *stageCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *stageCapture) WithGroup(string) slog.Handler      { return h }

func (h *stageCapture) firstWithStage(stage string) (map[string]string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec["stage"] == stage {
			return rec, true
		}
	}
	return nil, false
}

// TestMockDapiNodeManagerJoinsCallerTrace is C8 without Docker: a NodeManager
// client call must arrive on the caller's trace and land that trace on
// mock-dapi's acquire/release log lines. It covers the two pieces of wiring the
// citest cannot see separately — the server interceptor registration and the
// ctx-aware params logging.
func TestMockDapiNodeManagerJoinsCallerTrace(t *testing.T) {
	prevProp := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevProp)
		otel.SetTracerProvider(prevTP)
	})

	prevLogger := slog.Default()
	prevFormat := commonobs.LogFormat()
	devshardobs.InstallLogger("json")
	capture := &stageCapture{}
	slog.SetDefault(slog.New(commonobs.NewTraceHandler(capture)))
	t.Cleanup(func() {
		devshardobs.InstallLogger(prevFormat)
		slog.SetDefault(prevLogger)
	})

	bed := startBed(t)
	defer bed.cleanup()

	client, err := nodemanager.NewClient(bed.grpcAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	ctx = commonobs.SetRequestID(ctx, "req-mockdapi")
	traceID := span.SpanContext().TraceID().String()

	acq, err := client.Acquire(ctx, "Qwen/Test", nil, "12")
	require.NoError(t, err)
	require.NotEmpty(t, acq.NodeId)
	require.NoError(t, client.Release(ctx, acq.LockId, gen.ReleaseOutcome_SUCCESS))
	span.End()

	acquire, ok := capture.firstWithStage("mlnode_acquire")
	require.True(t, ok, "mock-dapi did not log mlnode_acquire")
	require.Equal(t, traceID, acquire["trace_id"])
	require.Equal(t, "req-mockdapi", acquire["request_id"])
	require.Equal(t, acq.NodeId, acquire["node_id"])

	release, ok := capture.firstWithStage("mlnode_release")
	require.True(t, ok, "mock-dapi did not log mlnode_release")
	require.Equal(t, traceID, release["trace_id"])
	require.Equal(t, "req-mockdapi", release["request_id"])
	require.Equal(t, acq.LockId, release["lock_id"])
}
