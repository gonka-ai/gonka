package main

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/observability"
	"devshard/user"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAttemptSpanDispositionForWinnerAndLoser(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	e := &Redundancy{}

	winner := newTestInflight(10, 0, "h0")
	winner.openAttemptSpan(ctx, nil, "p0")
	loser := newTestInflight(11, 1, "h1")
	loser.openAttemptSpan(ctx, nil, "p1")
	failed := newTestInflight(12, 2, "h2")
	failed.err = io.EOF
	failed.openAttemptSpan(ctx, nil, "p2")

	e.recordGatewayAttemptTerminal(winner, user.InferenceParams{}, 10, true)
	e.recordGatewayAttemptTerminal(loser, user.InferenceParams{}, 10, true)
	e.recordGatewayAttemptTerminal(failed, user.InferenceParams{}, 10, false)
	winner.endAttemptSpan()
	loser.endAttemptSpan()
	failed.endAttemptSpan()
	req.End()

	byNonce := map[int64]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		if s.Name() != observability.SpanNameGatewayAttempt {
			continue
		}
		attrs := spanAttrMap(s)
		nonce, _ := attrs[string(observability.AttrNonce)].(int64)
		byNonce[nonce] = s
	}
	require.Len(t, byNonce, 3)

	winnerAttrs := spanAttrMap(byNonce[10])
	require.Equal(t, string(accounting.DispositionFinishedUsed), winnerAttrs[string(observability.AttrDisposition)])
	require.Nil(t, winnerAttrs[string(observability.AttrFailureOrigin)])

	loserAttrs := spanAttrMap(byNonce[11])
	require.Equal(t, string(accounting.DispositionFinishedUnused), loserAttrs[string(observability.AttrDisposition)])

	failedAttrs := spanAttrMap(byNonce[12])
	require.Equal(t, string(accounting.DispositionFinishedUnused), failedAttrs[string(observability.AttrDisposition)])
	require.Equal(t, string(accounting.FailureTransportUnknown), failedAttrs[string(observability.AttrFailureOrigin)])
	require.Equal(t, "eof_transport", failedAttrs[string(observability.AttrDetailReason)])
}

func TestAttemptSpanGhostDispositionAndNoSendReason(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	rec := withAttemptSpanRecorder(t)

	ctx, req := observability.StartGatewayRequest(context.Background())
	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(ctx, prepared, ghostThrottled, ghostThrottled.reason())
	req.End()

	var attempt sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			attempt = s
		}
	}
	require.NotNil(t, attempt)
	attrs := spanAttrMap(attempt)
	require.Equal(t, string(accounting.DispositionGhost), attrs[string(observability.AttrDisposition)])
	require.Equal(t, string(accounting.NoSendParticipantThrottled), attrs[string(observability.AttrNoSendReason)])
	require.Equal(t, int64(prepared.Nonce()), attrs[string(observability.AttrNonce)])
}

func TestAttemptSpanTimeoutAttributes(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		action  string
		reason  string
		detail  string
		outcome accounting.TimeoutOutcome
	}{
		{"skipped", "refused", "skipped", "phase_transition_aborted", "", accounting.TimeoutSkipped},
		{"applied", "execution", "completed", "none", "", accounting.TimeoutApplied},
		{"vote_collection_failed", "refused", "failed", "vote_collection_failed", "", accounting.TimeoutVoteCollectionFailed},
		{"insufficient_votes", "refused", "failed", "insufficient_votes", "", accounting.TimeoutInsufficientVotes},
		{"diff_send_failed", "execution", "failed", "diff_send_failed", "", accounting.TimeoutDiffSendFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := withAttemptSpanRecorder(t)
			ctx, req := observability.StartGatewayRequest(context.Background())
			e := &Redundancy{}
			inf := newTestInflight(1, 0, "h0")
			if tc.detail == "" && tc.reason == "phase_transition_aborted" {
				inf.phaseTransitionAborted = true
			}
			inf.openAttemptSpan(ctx, nil, "p0")
			e.recordGatewayTimeoutAction(inf, user.InferenceParams{}, tc.kind, tc.action, tc.reason)
			inf.endAttemptSpan()
			req.End()

			var attempt sdktrace.ReadOnlySpan
			for _, s := range rec.Ended() {
				if s.Name() == observability.SpanNameGatewayAttempt {
					attempt = s
				}
			}
			require.NotNil(t, attempt)
			attrs := spanAttrMap(attempt)
			require.Equal(t, tc.kind, attrs[string(observability.AttrTimeoutKind)])
			require.Equal(t, string(tc.outcome), attrs[string(observability.AttrTimeoutOutcome)])
		})
	}
}

func TestAttemptSpanAttributesMatchAccountingFacts(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	e := &Redundancy{}

	inf := newTestInflight(7, 3, "h3")
	inf.openAttemptSpan(ctx, nil, "participant-7")
	e.recordGatewayAttemptStarted(inf, user.InferenceParams{Model: "llama"})
	e.recordGatewayAttemptTerminal(inf, user.InferenceParams{Model: "llama"}, 7, true)
	inf.endAttemptSpan()
	req.End()

	key := accounting.CounterKey{
		SlotID:         3,
		Disposition:    accounting.DispositionFinishedUsed,
		DispatchPhase:  accounting.PhaseNormal,
		QuarantineMode: accounting.QuarantineNone,
	}
	want := map[string]any{}
	for _, a := range observability.CounterKeyAttrs(key) {
		want[string(a.Key)] = a.Value.AsInterface()
	}

	var attempt sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			attempt = s
		}
	}
	require.NotNil(t, attempt)
	got := spanAttrMap(attempt)
	for k, v := range want {
		require.Equal(t, v, got[k], "attr %s", k)
	}
}

// attemptSpans returns every exported gateway.attempt span, keyed by nonce.
func attemptSpans(rec *tracetest.SpanRecorder) map[int64]sdktrace.ReadOnlySpan {
	out := map[int64]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		if s.Name() != observability.SpanNameGatewayAttempt {
			continue
		}
		nonce, _ := spanAttrMap(s)[string(observability.AttrNonce)].(int64)
		out[nonce] = s
	}
	return out
}

// lastPhaseEnd returns when the attempt's last phase child closed, which is the
// moment the race settled for that attempt.
func lastPhaseEnd(rec *tracetest.SpanRecorder, attempt sdktrace.ReadOnlySpan) time.Time {
	var last time.Time
	for _, s := range rec.Ended() {
		if s.Parent().SpanID() != attempt.SpanContext().SpanID() {
			continue
		}
		if s.EndTime().After(last) {
			last = s.EndTime()
		}
	}
	return last
}

// TestRunInferenceStampsDispositionOnEveryAttemptSpan drives the production
// path. The unit tests above call record-then-end in the order the helpers were
// designed for; only a full run proves finishRaceOutcome keeps the span open
// long enough for the terminal fact to land on it.
func TestRunInferenceStampsDispositionOnEveryAttemptSpan(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 3, nil, true)
	rec := withAttemptSpanRecorder(t)

	ctx, req := observability.StartGatewayRequest(context.Background())
	var buf bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil))
	// The request can return while speculative attempts are still settling.
	require.Eventually(t, func() bool {
		spans := attemptSpans(rec)
		if len(spans) == 0 {
			return false
		}
		for _, s := range spans {
			if spanAttrMap(s)[string(observability.AttrDisposition)] == nil {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond, "every attempt span must carry its disposition")
	req.End()

	for nonce, s := range attemptSpans(rec) {
		attrs := spanAttrMap(s)
		require.Contains(t, []any{
			string(accounting.DispositionFinishedUsed),
			string(accounting.DispositionFinishedUnused),
			string(accounting.DispositionFinishedUsageUnknown),
		}, attrs[string(observability.AttrDisposition)], "nonce %d", nonce)
		require.Equal(t, string(accounting.PhaseNormal), attrs[string(observability.AttrDispatchPhase)],
			"nonce %d must carry the phase stamped at real send", nonce)
		require.True(t, s.EndTime().Equal(lastPhaseEnd(rec, s)),
			"nonce %d: attempt span must end when the race settled", nonce)
	}
}

// TestRunInferenceStampsTimeoutOutcomeOnAttemptSpan covers the other half of
// T3.4: a failed attempt's span has to survive until timeout evaluation has
// recorded its outcome, while still reporting the race duration rather than
// the timeout duration.
func TestRunInferenceStampsTimeoutOutcomeOnAttemptSpan(t *testing.T) {
	zeroReceiptTimeout(t)
	oldBuffer := user.TimeoutBuffer
	user.TimeoutBuffer = 0
	t.Cleanup(func() { user.TimeoutBuffer = oldBuffer })

	env := setupTestProxy(t, 3, nil, true)
	env.killables[1].Kill()
	rec := withAttemptSpanRecorder(t)

	ctx, req := observability.StartGatewayRequest(context.Background())
	var buf bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil))

	var timedOut sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool {
		for _, s := range attemptSpans(rec) {
			if spanAttrMap(s)[string(observability.AttrTimeoutOutcome)] != nil {
				timedOut = s
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond,
		"timeout evaluation must be able to stamp the still-open attempt span")
	req.End()

	attrs := spanAttrMap(timedOut)
	require.NotNil(t, attrs[string(observability.AttrTimeoutKind)])
	require.NotNil(t, attrs[string(observability.AttrDisposition)],
		"the terminal disposition must survive alongside the timeout outcome")
	require.True(t, timedOut.EndTime().Equal(lastPhaseEnd(rec, timedOut)),
		"attempt duration must exclude timeout evaluation")
}

// TestAttemptSpanEndIsIdempotent guards the two call sites that can both reach
// a failed attempt (race loop and timeout loop).
func TestAttemptSpanEndIsIdempotent(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	inf := newTestInflight(5, 0, "h0")
	inf.openAttemptSpan(ctx, nil, "p0")
	at := time.Unix(1_700_000_000, 0).UTC()
	inf.closeAttemptPhases(at)
	endAttemptSpans([]*inflight{inf, inf})
	inf.endAttemptSpan()
	req.End()

	spans := attemptSpans(rec)
	require.Len(t, spans, 1)
	require.True(t, spans[5].EndTime().Equal(at), "span must end at the race outcome, not at the last call")
}

func TestAttemptSpanStartedCarriesDispatchPhase(t *testing.T) {
	rec := withAttemptSpanRecorder(t)
	ctx, req := observability.StartGatewayRequest(context.Background())
	e := &Redundancy{}
	inf := newTestInflight(1, 0, "h0")
	inf.sendTime = time.Now()
	inf.openAttemptSpan(ctx, nil, "p0")
	e.recordGatewayAttemptStarted(inf, user.InferenceParams{})
	inf.endAttemptSpan()
	req.End()

	var attempt sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == observability.SpanNameGatewayAttempt {
			attempt = s
		}
	}
	require.NotNil(t, attempt)
	attrs := spanAttrMap(attempt)
	require.Equal(t, string(accounting.PhaseNormal), attrs[string(observability.AttrDispatchPhase)])
	require.Equal(t, string(accounting.QuarantineNone), attrs[string(observability.AttrQuarantineMode)])
}
