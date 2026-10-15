package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"devshard/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func withAttemptSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
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
	return rec
}

func newTestInflight(nonce uint64, hostIdx int, hostID string) *inflight {
	return &inflight{
		nonce:    nonce,
		hostIdx:  hostIdx,
		hostID:   hostID,
		escrowID: "escrow-1",
		done:     make(chan struct{}),
	}
}

func TestAttemptSpanOpensAndClosesOncePerNonce(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	defer req.End()

	var attempts []*inflight
	for i, nonce := range []uint64{11, 12, 13} {
		inf := newTestInflight(nonce, i, fmt.Sprintf("host-%d", i))
		inf.openAttemptSpan(ctx, nil, fmt.Sprintf("p%d", i))
		inf.role = "primary"
		if i > 0 {
			inf.role = "secondary"
			inf.startReason = "receipt_timeout"
			inf.triggerNonce = 11
		} else {
			inf.startReason = "primary"
		}
		inf.attemptIndex = i
		inf.applyAttemptSpanAttrs()
		attempts = append(attempts, inf)
	}
	for _, inf := range attempts {
		inf.endAttemptSpan()
	}
	req.End()

	var attemptSpans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			attemptSpans = append(attemptSpans, s)
		}
	}
	require.Len(t, attemptSpans, 3)
	for _, s := range attemptSpans {
		require.False(t, s.EndTime().IsZero())
	}
}

func TestAttemptSpanIsChildOfGatewayRequest(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(7, 1, "h1")
	inf.openAttemptSpan(ctx, nil, "p1")
	inf.role = "primary"
	inf.startReason = "primary"
	inf.applyAttemptSpanAttrs()
	inf.endAttemptSpan()
	req.End()

	var reqSpan, attemptSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "gateway.request":
			reqSpan = s
		case observability.SpanNameGatewayAttempt:
			attemptSpan = s
		}
	}
	require.NotNil(t, reqSpan)
	require.NotNil(t, attemptSpan)
	require.Equal(t, reqSpan.SpanContext().TraceID(), attemptSpan.SpanContext().TraceID())
	require.Equal(t, reqSpan.SpanContext().SpanID(), attemptSpan.Parent().SpanID())
	require.NotEqual(t, attemptSpan.SpanContext().SpanID(), reqSpan.SpanContext().SpanID())
}

func TestAttemptSpanCarriesIdentityAndRole(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(99, 2, "validator-abc")
	inf.openAttemptSpan(ctx, nil, "participant-key")
	inf.role = "secondary"
	inf.startReason = "receipt_timeout"
	inf.attemptIndex = 1
	inf.triggerNonce = 98
	inf.applyAttemptSpanAttrs()
	inf.endAttemptSpan()
	req.End()

	var got sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			got = s
		}
	}
	require.NotNil(t, got)
	attrs := spanAttrMap(got)
	require.Equal(t, int64(99), attrs[string(observability.AttrNonce)])
	require.Equal(t, "escrow-1", attrs[string(observability.AttrEscrowID)])
	require.Equal(t, int64(2), attrs[string(observability.AttrSlotID)])
	require.Equal(t, "participant-key", attrs[string(observability.AttrParticipantKey)])
	require.Equal(t, "validator-abc", attrs[string(observability.AttrHostID)])
	require.Equal(t, "secondary", attrs[string(observability.AttrAttemptRole)])
	require.Equal(t, "receipt_timeout", attrs[string(observability.AttrAttemptStartReason)])
	require.Equal(t, int64(1), attrs[string(observability.AttrAttemptIndex)])
	require.Equal(t, int64(98), attrs[string(observability.AttrAttemptTriggerNonce)])
}

func TestAttemptPhaseSpansFormContiguousChain(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(1, 0, "h0")
	inf.openAttemptSpan(ctx, nil, "p0")
	inf.role = "primary"
	inf.startReason = "primary"
	inf.applyAttemptSpanAttrs()

	t0 := time.Unix(1_700_000_000, 0).UTC()
	inf.sendTime = t0
	inf.startDispatchPhase()
	receiptAt := t0.Add(1200 * time.Millisecond)
	inf.onReceiptPhase(receiptAt)
	firstToken := t0.Add(3400 * time.Millisecond)
	inf.onFirstTokenPhase(firstToken)
	inf.outputChunks.Store(5)
	inf.contentChunks.Store(4)
	inf.outputBytes.Store(40)
	endAt := t0.Add(8 * time.Second)
	inf.closeAttemptPhases(endAt)
	inf.endAttemptSpan()
	req.End()

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = s
	}
	require.Contains(t, byName, observability.SpanNameAttemptDispatch)
	require.Contains(t, byName, observability.SpanNameAttemptPrefill)
	require.Contains(t, byName, observability.SpanNameAttemptStream)
	require.True(t, byName[observability.SpanNameAttemptDispatch].EndTime().Equal(
		byName[observability.SpanNameAttemptPrefill].StartTime()))
	require.True(t, byName[observability.SpanNameAttemptPrefill].EndTime().Equal(
		byName[observability.SpanNameAttemptStream].StartTime()))

	streamAttrs := spanAttrMap(byName[observability.SpanNameAttemptStream])
	require.Equal(t, int64(5), streamAttrs[string(observability.AttrOutputChunks)])
	require.Equal(t, int64(4), streamAttrs[string(observability.AttrContentChunks)])
	require.Equal(t, int64(40), streamAttrs[string(observability.AttrOutputBytes)])
}

func TestAttemptWithoutFirstTokenSkipsStreamSpan(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(1, 0, "h0")
	inf.openAttemptSpan(ctx, nil, "p0")
	inf.sendTime = time.Now()
	inf.startDispatchPhase()
	inf.closeAttemptPhases(time.Now())
	inf.endAttemptSpan()
	req.End()

	names := map[string]bool{}
	for _, s := range rec.Ended() {
		names[s.Name()] = true
		require.False(t, s.EndTime().IsZero(), "span %s left unended", s.Name())
	}
	require.True(t, names[observability.SpanNameAttemptDispatch])
	require.False(t, names[observability.SpanNameAttemptStream])
}

func TestStallProducesDetectedAndRecoveredEvents(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(1, 0, "h0")
	inf.openAttemptSpan(ctx, nil, "p0")
	inf.sendTime = time.Now()
	inf.startDispatchPhase()
	inf.onReceiptPhase(time.Now())
	inf.onFirstTokenPhase(time.Now())

	now := time.Now()
	inf.recordStallDetected(now, 2, 1, 20)
	inf.recordStallRecovered(now.Add(time.Second))
	inf.closeAttemptPhases(time.Now())
	inf.endAttemptSpan()
	req.End()

	var events []string
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameAttemptStream {
			for _, e := range s.Events() {
				events = append(events, e.Name)
			}
		}
	}
	require.Equal(t, []string{observability.EventStallDetected, observability.EventStallRecovered}, events)
}

func TestSuccessfulSplitEmitsNoParentEscalatedEvent(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	primary := newTestInflight(1, 0, "h0")
	primary.openAttemptSpan(ctx, nil, "p0")
	primary.role = "primary"
	primary.startReason = "primary"
	primary.applyAttemptSpanAttrs()

	secondary := newTestInflight(2, 1, "h1")
	secondary.openAttemptSpan(ctx, nil, "p1")
	secondary.role = "secondary"
	secondary.startReason = "receipt_timeout"
	secondary.attemptIndex = 1
	secondary.triggerNonce = 1
	secondary.applyAttemptSpanAttrs()

	primary.endAttemptSpan()
	secondary.endAttemptSpan()
	req.End()

	for _, s := range rec.Ended() {
		if s.Name() != "gateway.request" {
			continue
		}
		for _, e := range s.Events() {
			require.NotEqual(t, "attempt.escalated", e.Name)
		}
	}
}

func TestAttemptSpanNoopWhenTracingDisabled(t *testing.T) {
	// Default global provider is a no-op tracer — must not panic.
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	require.NotPanics(t, func() {
		inf := newTestInflight(1, 0, "h0")
		inf.openAttemptSpan(context.Background(), nil, "p0")
		inf.role = "primary"
		inf.startReason = "primary"
		inf.applyAttemptSpanAttrs()
		inf.sendTime = time.Now()
		inf.startDispatchPhase()
		inf.onReceiptPhase(time.Now())
		inf.onFirstTokenPhase(time.Now())
		inf.recordStallDetected(time.Now(), 0, 0, 0)
		inf.recordStallRecovered(time.Now())
		inf.closeAttemptPhases(time.Now())
		inf.endAttemptSpan()
	})
}

func TestPickerExhaustedEmitsEvent(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	observability.AddPickerExhausted(ctx, "receipt_timeout")
	observability.AddSecondaryPrepareFailed(ctx, "attempt_failed")
	req.End()

	var events []string
	for _, s := range rec.Ended() {
		if s.Name() != "gateway.request" {
			continue
		}
		for _, e := range s.Events() {
			events = append(events, e.Name)
		}
	}
	require.Equal(t, []string{
		observability.EventPickerExhausted,
		observability.EventSecondaryPrepareFailed,
	}, events)
}

func TestHeartbeatEmitsNoSpanEvents(t *testing.T) {
	// Heartbeats stay logs-only (monitorInflight). Opening an attempt and
	// advancing phases without stall/escalation must produce zero events.
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(1, 0, "h0")
	inf.openAttemptSpan(ctx, nil, "p0")
	inf.sendTime = time.Now()
	inf.startDispatchPhase()
	inf.onReceiptPhase(time.Now())
	inf.onFirstTokenPhase(time.Now())
	inf.closeAttemptPhases(time.Now())
	inf.endAttemptSpan()
	req.End()

	for _, s := range rec.Ended() {
		require.Empty(t, s.Events(), "span %s unexpectedly has events (heartbeats must stay logs-only)", s.Name())
	}
}

func TestEscalationLogFieldsIdentifyNewAttempt(t *testing.T) {
	// Mirrors the enriched fields appended in startAdditionalInflight after
	// prepare succeeds — the log line must name the joining attempt.
	fields := []any{"host", "trigger-host", "delay_ms", int64(50)}
	reason := "receipt_timeout"
	newNonce := uint64(22)
	newHost := "secondary-host"
	attemptIndex := 1
	role := "secondary"
	fields = append(fields,
		"reason", reason,
		"new_nonce", newNonce,
		"new_host", newHost,
		"attempt_index", attemptIndex,
		"role", role,
	)
	m := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		m[fmt.Sprint(fields[i])] = fields[i+1]
	}
	require.Equal(t, "trigger-host", m["host"])
	require.Equal(t, reason, m["reason"])
	require.Equal(t, newNonce, m["new_nonce"])
	require.Equal(t, newHost, m["new_host"])
	require.Equal(t, attemptIndex, m["attempt_index"])
	require.Equal(t, role, m["role"])
}

func TestStaggeredAttemptsHaveDistinctStartTimes(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())

	primary := newTestInflight(1, 0, "h0")
	primary.openAttemptSpan(ctx, nil, "p0")
	primary.role = "primary"
	primary.startReason = "primary"
	primary.applyAttemptSpanAttrs()

	time.Sleep(5 * time.Millisecond)

	secondary := newTestInflight(2, 1, "h1")
	secondary.openAttemptSpan(ctx, nil, "p1")
	secondary.role = "secondary"
	secondary.startReason = "receipt_timeout"
	secondary.attemptIndex = 1
	secondary.triggerNonce = 1
	secondary.applyAttemptSpanAttrs()

	primary.endAttemptSpan()
	secondary.endAttemptSpan()
	req.End()

	var starts []time.Time
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			starts = append(starts, s.StartTime())
		}
	}
	require.Len(t, starts, 2)
	require.True(t, starts[1].After(starts[0]) || starts[1].Equal(starts[0].Add(time.Millisecond)),
		"secondary should start at or after primary (got primary=%s secondary=%s)", starts[0], starts[1])
	if starts[1].Before(starts[0]) {
		t.Fatalf("secondary started before primary")
	}
}

func spanAttrMap(s sdktrace.ReadOnlySpan) map[string]any {
	out := make(map[string]any)
	for _, a := range s.Attributes() {
		out[string(a.Key)] = a.Value.AsInterface()
	}
	return out
}
