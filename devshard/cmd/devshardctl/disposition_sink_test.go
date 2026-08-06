package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	commonobs "common/observability"
	"devshard/accounting"
	"devshard/observability"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func withDispositionLogCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	// Stage only emits structured JSON attrs when InstallLogger selected json.
	observability.InstallLogger("json")
	handler := commonobs.NewTraceHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		observability.InstallLogger("text")
	})
	return &buf
}

func TestDispositionLogLineCarriesTraceID(t *testing.T) {
	buf := withDispositionLogCapture(t)
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(rec),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})

	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	tracker.SetDispositionSink(dispositionLogSink{})

	registerGatewayAccountingTestEscrow(t, tracker, "e1", 1, "m")
	ctx, span := observability.StartGatewayAttempt(context.Background(), observability.AttemptIdentity{
		Nonce: 1, EscrowID: "e1", SlotID: 1,
	})
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.RealSend(ctx, "e1", 1, time.Now(), "none")
	recorder.Usage(ctx, "e1", 1, 1)
	require.NoError(t, tracker.RecordProtocol("e1", 1, 0, accounting.ProtocolFinishApplied, types.HostStats{}))
	span.End()
	tracker.FlushDispositions()

	sc := trace.SpanContextFromContext(ctx)
	require.True(t, sc.IsValid())
	out := buf.String()
	require.Contains(t, out, `"stage":"nonce_disposition"`)
	require.Contains(t, out, `"trace_id":"`+sc.TraceID().String()+`"`)
}

func TestDispositionLogLineFieldsMatchCounterKey(t *testing.T) {
	buf := withDispositionLogCapture(t)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	tracker.SetDispositionSink(dispositionLogSink{})
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 1, "llama")

	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordGhost(
		"e1", 1, accounting.PhaseNormal, accounting.QuarantineProbe,
		accounting.NoSendParticipantThrottled, "", accounting.TraceRef{},
	))
	tracker.FlushDispositions()

	out := buf.String()
	require.Contains(t, out, `"stage":"nonce_disposition"`)
	require.Contains(t, out, `"disposition":"ghost"`)
	require.Contains(t, out, `"no_send_reason":"participant_throttled_no_send"`)
	require.Contains(t, out, `"quarantine_mode":"probe"`)
	require.Contains(t, out, `"dispatch_phase":"normal"`)
	require.Contains(t, out, `"model":"llama"`)
	require.Contains(t, out, `"lag_ms":`)
}

func TestDispositionLogLineOrphanHasEmptyTraceID(t *testing.T) {
	buf := withDispositionLogCapture(t)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	tracker.SetDispositionSink(dispositionLogSink{})
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 1, "m")

	require.NoError(t, tracker.RecordDiff("e1", 1, false)) // protocol_only, zero TraceRef
	tracker.FlushDispositions()

	out := buf.String()
	require.Contains(t, out, `"stage":"nonce_disposition"`)
	require.Contains(t, out, `"disposition":"protocol_only"`)
	require.Contains(t, out, `"trace_id":""`)
	require.Contains(t, out, `"span_id":""`)
}
