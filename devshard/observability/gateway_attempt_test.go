package observability_test

import (
	"context"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func withSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
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

func TestStartGatewayAttemptIsChildOfRequest(t *testing.T) {
	rec := withSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	defer req.End()

	_, attempt := observability.StartGatewayAttempt(ctx, observability.AttemptIdentity{
		Nonce: 7, EscrowID: "e1", SlotID: 1, ParticipantKey: "p1",
		HostID: "h1", Model: "m", Role: "primary", StartReason: "primary",
	})
	attempt.End()
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
	require.NotEqual(t, reqSpan.SpanContext().SpanID(), attemptSpan.SpanContext().SpanID())
}

func TestSetAttemptRoleReason(t *testing.T) {
	rec := withSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	defer req.End()
	_, attempt := observability.StartGatewayAttempt(ctx, observability.AttemptIdentity{Nonce: 1})
	observability.SetAttemptRoleReason(attempt, "secondary", "receipt_timeout", 1, 42)
	attempt.End()
	req.End()

	var got sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			got = s
		}
	}
	require.NotNil(t, got)
	attrs := attrMap(got)
	require.Equal(t, "secondary", attrs[string(observability.AttrAttemptRole)])
	require.Equal(t, "receipt_timeout", attrs[string(observability.AttrAttemptStartReason)])
	require.Equal(t, int64(1), attrs[string(observability.AttrAttemptIndex)])
	require.Equal(t, int64(42), attrs[string(observability.AttrAttemptTriggerNonce)])
}

func TestPhaseSpansContiguous(t *testing.T) {
	rec := withSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	defer req.End()
	ctx, attempt := observability.StartGatewayAttempt(ctx, observability.AttemptIdentity{Nonce: 1})
	defer attempt.End()

	t0 := time.Unix(100, 0).UTC()
	t1 := t0.Add(1200 * time.Millisecond)
	t2 := t0.Add(3400 * time.Millisecond)
	t3 := t0.Add(10 * time.Second)

	_, dispatch := observability.StartAttemptDispatch(ctx, t0)
	observability.EndSpan(dispatch, trace.WithTimestamp(t1))
	_, prefill := observability.StartAttemptPrefill(ctx, t1)
	observability.EndSpan(prefill, trace.WithTimestamp(t2))
	_, stream := observability.StartAttemptStream(ctx, t2)
	observability.FinishAttemptStream(stream, t3, 10, 8, 100, 1)
	attempt.End()
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

	streamAttrs := attrMap(byName[observability.SpanNameAttemptStream])
	require.Equal(t, int64(10), streamAttrs[string(observability.AttrOutputChunks)])
	require.Equal(t, int64(8), streamAttrs[string(observability.AttrContentChunks)])
	require.Equal(t, int64(100), streamAttrs[string(observability.AttrOutputBytes)])
	require.Equal(t, int64(1), streamAttrs[string(observability.AttrStallCount)])
}

func TestStallEventsAndParentNoSplitEvents(t *testing.T) {
	rec := withSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	_, attempt := observability.StartGatewayAttempt(ctx, observability.AttemptIdentity{Nonce: 1})
	now := time.Now()
	observability.AddStallDetected(attempt, now, 3, 2, 50)
	observability.AddStallRecovered(attempt, now.Add(time.Second))
	attempt.End()

	observability.AddPickerExhausted(ctx, "receipt_timeout")
	observability.AddEscalationSkipped(ctx, "receipt_timeout")
	req.End()

	var attemptEvents, parentEvents []string
	for _, s := range rec.Ended() {
		for _, e := range s.Events() {
			switch s.Name() {
			case observability.SpanNameGatewayAttempt:
				attemptEvents = append(attemptEvents, e.Name)
			case "gateway.request":
				parentEvents = append(parentEvents, e.Name)
			}
		}
	}
	require.Equal(t, []string{observability.EventStallDetected, observability.EventStallRecovered}, attemptEvents)
	require.Contains(t, parentEvents, observability.EventPickerExhausted)
	require.Contains(t, parentEvents, observability.EventEscalationSkipped)
	require.NotContains(t, parentEvents, "attempt.escalated")
}

func TestEndSpanNoopWhenNil(t *testing.T) {
	require.NotPanics(t, func() {
		observability.EndSpan(nil)
		observability.SetAttemptRoleReason(nil, "primary", "primary", 0, 0)
		observability.SetAttemptCounterKeyAttrs(nil, accounting.CounterKey{})
		observability.FinishAttemptStream(nil, time.Time{}, 0, 0, 0, 0)
		observability.AddStallDetected(nil, time.Now(), 0, 0, 0)
	})
}

func attrMap(s sdktrace.ReadOnlySpan) map[string]any {
	out := make(map[string]any)
	for _, a := range s.Attributes() {
		out[string(a.Key)] = a.Value.AsInterface()
	}
	return out
}
