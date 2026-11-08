package main

import (
	"context"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/observability"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// originTrace starts and ends a gateway.attempt span, returning the TraceRef
// the tracker would have captured from it.
func originTrace(t *testing.T) (accounting.TraceRef, trace.SpanContext) {
	t.Helper()
	ctx, span := observability.StartGatewayAttempt(context.Background(), observability.AttemptIdentity{
		Nonce: 7, EscrowID: "e1", SlotID: 2,
	})
	span.End()
	sc := trace.SpanContextFromContext(ctx)
	require.True(t, sc.IsValid())
	return accounting.TraceRefFromContext(ctx), sc
}

func dispositionSpans(rec *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameNonceDisposition {
			out = append(out, s)
		}
	}
	return out
}

func spanAttr(t *testing.T, s sdktrace.ReadOnlySpan, key string) (string, bool) {
	t.Helper()
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit(), true
		}
	}
	return "", false
}

func terminalEvent(ref accounting.TraceRef, lag time.Duration) accounting.DispositionEvent {
	sendAt := time.Now().Add(-lag)
	return accounting.DispositionEvent{
		EscrowID: "e1",
		Nonce:    7,
		Key: accounting.CounterKey{
			SlotID:                 2,
			Disposition:            accounting.DispositionUnfinishedRefused,
			DispatchPhase:          accounting.PhaseNormal,
			TimeoutEvaluationPhase: accounting.PhasePoC,
			QuarantineMode:         accounting.QuarantineProbe,
			NoSendReason:           accounting.NoSendParticipantThrottled,
			FailureOrigin:          accounting.FailureHostResponse,
			DetailReason:           "receipt_missing",
			TimeoutKind:            accounting.TimeoutRefused,
			TimeoutOutcome:         accounting.TimeoutApplied,
			TimeoutReason:          accounting.TimeoutNotApplied,
		},
		Trace:       ref,
		SendAt:      sendAt,
		ObservedAt:  sendAt.Add(lag),
		Participant: "gonka1abc",
		Model:       "llama",
	}
}

func TestEarlyDispositionSpanIsReparentedChild(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ref, origin := originTrace(t)

	dispositionSink{}.OnDisposition(terminalEvent(ref, time.Second))

	spans := dispositionSpans(rec)
	require.Len(t, spans, 1)
	require.Equal(t, origin.TraceID(), spans[0].SpanContext().TraceID())
	require.Equal(t, origin.SpanID(), spans[0].Parent().SpanID())
	require.Empty(t, spans[0].Links())
}

func TestLateDispositionSpanIsLinkedRootBeyondThreshold(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ref, origin := originTrace(t)

	dispositionSink{}.OnDisposition(terminalEvent(ref, observability.DispositionReparentWindow+time.Second))

	spans := dispositionSpans(rec)
	require.Len(t, spans, 1)
	require.False(t, spans[0].Parent().IsValid(), "late verdict must not claim a flushed parent")
	require.NotEqual(t, origin.TraceID(), spans[0].SpanContext().TraceID())

	links := spans[0].Links()
	require.Len(t, links, 1)
	require.Equal(t, origin.TraceID(), links[0].SpanContext.TraceID())
	require.Equal(t, origin.SpanID(), links[0].SpanContext.SpanID())

	got, ok := spanAttr(t, spans[0], string(observability.AttrOriginTraceID))
	require.True(t, ok)
	require.Equal(t, origin.TraceID().String(), got)
}

func TestDispositionSpanPreservesSamplingDecision(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ref, _ := originTrace(t)
	ref.Sampled = false

	dispositionSink{}.OnDisposition(terminalEvent(ref, time.Second))
	dispositionSink{}.OnDisposition(terminalEvent(ref, time.Hour))

	require.Empty(t, dispositionSpans(rec), "an unsampled request must not mint a disposition trace")
}

func TestDispositionSpanCarriesFullAttributeSet(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ref, _ := originTrace(t)
	ev := terminalEvent(ref, time.Hour)

	dispositionSink{}.OnDisposition(ev)

	spans := dispositionSpans(rec)
	require.Len(t, spans, 1)
	// Self-sufficiency: every dimension a TraceQL search may filter on has to
	// be on this span, never reached through a cross-span join.
	want := map[string]string{
		string(observability.AttrDisposition):            "unfinished_refused",
		string(observability.AttrDispatchPhase):          "normal",
		string(observability.AttrTimeoutEvaluationPhase): "poc",
		string(observability.AttrQuarantineMode):         "probe",
		string(observability.AttrNoSendReason):           "participant_throttled_no_send",
		string(observability.AttrFailureOrigin):          "host_response",
		string(observability.AttrTimeoutKind):            "refused",
		string(observability.AttrTimeoutOutcome):         "applied",
		string(observability.AttrTimeoutReason):          "timeout_not_applied",
		string(observability.AttrDetailReason):           "receipt_missing",
		string(observability.AttrSlotID):                 "2",
		string(observability.AttrNonce):                  "7",
		string(observability.AttrEscrowID):               "e1",
		string(observability.AttrParticipantKey):         "gonka1abc",
		string(observability.AttrModel):                  "llama",
	}
	for key, expected := range want {
		got, ok := spanAttr(t, spans[0], key)
		require.Truef(t, ok, "missing attribute %s", key)
		require.Equalf(t, expected, got, "attribute %s", key)
	}
}

func TestProtocolOnlyDispositionSpanHasNoLink(t *testing.T) {
	rec := withAttemptSpanRecorder(t)

	dispositionSink{}.OnDisposition(accounting.DispositionEvent{
		EscrowID:   "e1",
		Nonce:      9,
		Key:        accounting.CounterKey{SlotID: 1, Disposition: accounting.DispositionProtocolOnly},
		ObservedAt: time.Now(),
	})

	spans := dispositionSpans(rec)
	require.Len(t, spans, 1)
	require.False(t, spans[0].Parent().IsValid())
	require.Empty(t, spans[0].Links(), "an orphan has no origin to link to")
	_, ok := spanAttr(t, spans[0], string(observability.AttrOriginTraceID))
	require.False(t, ok)
}

// The span must be timestamped when the verdict was reached, not when the
// tracker's delivery goroutine happened to run it.
func TestDispositionSpanIsStampedAtObservationTime(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ref, _ := originTrace(t)
	ev := terminalEvent(ref, time.Hour)

	dispositionSink{}.OnDisposition(ev)

	spans := dispositionSpans(rec)
	require.Len(t, spans, 1)
	require.WithinDuration(t, ev.ObservedAt, spans[0].StartTime(), time.Millisecond)
	require.WithinDuration(t, ev.ObservedAt, spans[0].EndTime(), time.Millisecond)
}
