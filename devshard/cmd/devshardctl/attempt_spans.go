package main

import (
	"context"
	"time"

	"devshard/accounting"
	"devshard/observability"

	"go.opentelemetry.io/otel/trace"
)

// Phase / attempt span state on inflight. Guarded by phaseMu because receipt
// and first-token can race the send goroutine.
func (inf *inflight) openAttemptSpan(ctx context.Context, e *Redundancy, participantKey string) {
	if inf == nil {
		return
	}
	model := ""
	quarantine := "none"
	if e != nil {
		model = e.model
		quarantine = e.quarantineModeForParticipant(participantKey)
	}
	spanCtx, span := observability.StartGatewayAttempt(ctx, observability.AttemptIdentity{
		Nonce:          inf.nonce,
		EscrowID:       inf.escrowID,
		SlotID:         uint32(inf.hostIdx),
		ParticipantKey: participantKey,
		HostID:         inf.hostID,
		Model:          model,
		QuarantineMode: quarantine,
	})
	inf.spanCtx = spanCtx
	inf.span = span
}

func (inf *inflight) applyAttemptSpanAttrs() {
	if inf == nil {
		return
	}
	observability.SetAttemptRoleReason(inf.span, inf.role, inf.startReason, inf.attemptIndex, inf.triggerNonce)
}

// applyCounterKeyAttrs stamps one accounting fact's dimensions onto the attempt
// span. Keys absent from the partial key are left untouched, so each fact
// contributes only what it actually recorded.
func (inf *inflight) applyCounterKeyAttrs(key accounting.CounterKey) {
	if inf == nil {
		return
	}
	observability.SetAttemptCounterKeyAttrs(inf.span, key)
}

// closeAttemptPhases ends the open phase children at the race outcome. The
// attempt span itself stays open: timeout evaluation still has its disposition
// to record, and endAttemptSpan replays this timestamp so the span's duration
// remains the race duration rather than the timeout duration.
func (inf *inflight) closeAttemptPhases(at time.Time) {
	if inf == nil {
		return
	}
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	inf.endOpenPhaseSpansLocked(at)
	inf.spanEndAt = at
}

// endAttemptSpan ends the attempt span. Idempotent: the second call is a no-op.
func (inf *inflight) endAttemptSpan() {
	if inf == nil {
		return
	}
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	if inf.span == nil {
		return
	}
	at := inf.spanEndAt
	if at.IsZero() {
		at = time.Now()
	}
	inf.endOpenPhaseSpansLocked(at)
	observability.EndSpan(inf.span, trace.WithTimestamp(at))
	inf.span = nil
}

func (inf *inflight) startDispatchPhase() {
	if inf == nil {
		return
	}
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	if inf.dispatchSpan != nil || inf.prefillSpan != nil || inf.streamSpan != nil {
		return
	}
	_, inf.dispatchSpan = observability.StartAttemptDispatch(inf.phaseCtxLocked(), inf.sendTime)
}

func (inf *inflight) onReceiptPhase(at time.Time) {
	if inf == nil {
		return
	}
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	if inf.dispatchSpan != nil {
		observability.EndSpan(inf.dispatchSpan, trace.WithTimestamp(at))
		inf.dispatchSpan = nil
	}
	if inf.prefillSpan != nil || inf.streamSpan != nil {
		return
	}
	_, inf.prefillSpan = observability.StartAttemptPrefill(inf.phaseCtxLocked(), at)
}

func (inf *inflight) onFirstTokenPhase(at time.Time) {
	if inf == nil {
		return
	}
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	if inf.dispatchSpan != nil {
		// Receipt never arrived (or raced); close dispatch at first token.
		observability.EndSpan(inf.dispatchSpan, trace.WithTimestamp(at))
		inf.dispatchSpan = nil
	}
	if inf.prefillSpan != nil {
		observability.EndSpan(inf.prefillSpan, trace.WithTimestamp(at))
		inf.prefillSpan = nil
	}
	if inf.streamSpan != nil {
		return
	}
	_, inf.streamSpan = observability.StartAttemptStream(inf.phaseCtxLocked(), at)
}

func (inf *inflight) recordStallDetected(at time.Time, outputChunks, contentChunks, outputBytes int64) {
	if inf == nil {
		return
	}
	observability.AddStallDetected(inf.eventSpan(), at, outputChunks, contentChunks, outputBytes)
}

func (inf *inflight) recordStallRecovered(at time.Time) {
	if inf == nil {
		return
	}
	observability.AddStallRecovered(inf.eventSpan(), at)
}

// eventSpan returns the innermost open span, so a stall lands on the phase it
// actually interrupted.
func (inf *inflight) eventSpan() trace.Span {
	inf.phaseMu.Lock()
	defer inf.phaseMu.Unlock()
	if inf.streamSpan != nil {
		return inf.streamSpan
	}
	if inf.prefillSpan != nil {
		return inf.prefillSpan
	}
	if inf.dispatchSpan != nil {
		return inf.dispatchSpan
	}
	return inf.span
}

func (inf *inflight) phaseCtxLocked() context.Context {
	if inf.spanCtx == nil {
		return context.Background()
	}
	return inf.spanCtx
}

func (inf *inflight) endOpenPhaseSpansLocked(at time.Time) {
	if inf.streamSpan != nil {
		observability.FinishAttemptStream(
			inf.streamSpan,
			at,
			inf.outputChunks.Load(),
			inf.contentChunks.Load(),
			inf.outputBytes.Load(),
			inf.stallCount(),
		)
		inf.streamSpan = nil
	}
	if inf.prefillSpan != nil {
		observability.EndSpan(inf.prefillSpan, trace.WithTimestamp(at))
		inf.prefillSpan = nil
	}
	if inf.dispatchSpan != nil {
		observability.EndSpan(inf.dispatchSpan, trace.WithTimestamp(at))
		inf.dispatchSpan = nil
	}
}
