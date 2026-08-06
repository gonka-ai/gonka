package main

import (
	"context"

	"devshard/accounting"
	"devshard/observability"

	"go.opentelemetry.io/otel/trace"
)

// dispositionSink turns a terminal accounting event into the two artefacts
// T3 promises: a structured nonce_disposition log line, and a
// devshard.nonce.disposition span. Both rebuild trace context from the stored
// TraceRef, so a verdict is reachable from the request that caused it.
type dispositionSink struct{}

func (s dispositionSink) OnDisposition(ev accounting.DispositionEvent) {
	s.log(ev)
	observability.EmitDispositionSpan(ev)
}

func (dispositionSink) log(ev accounting.DispositionEvent) {
	ctx := context.Background()
	fields := dispositionLogFields(ev)
	if origin, ok := observability.DispositionOrigin(ev.Trace); ok {
		ctx = trace.ContextWithRemoteSpanContext(ctx, origin)
	} else {
		fields = append(fields, "trace_id", "", "span_id", "")
	}
	logInferenceStage(ctx, ev.EscrowID, ev.Nonce, "nonce_disposition", fields...)
}

func dispositionLogFields(ev accounting.DispositionEvent) []any {
	key := ev.Key
	return []any{
		"disposition", observability.DispositionString(key.Disposition),
		"dispatch_phase", observability.PhaseString(key.DispatchPhase),
		"timeout_evaluation_phase", observability.PhaseString(key.TimeoutEvaluationPhase),
		"quarantine_mode", observability.QuarantineModeString(key.QuarantineMode),
		"no_send_reason", observability.NoSendReasonString(key.NoSendReason),
		"failure_origin", observability.FailureOriginString(key.FailureOrigin),
		"timeout_kind", observability.TimeoutKindString(key.TimeoutKind),
		"timeout_outcome", observability.TimeoutOutcomeString(key.TimeoutOutcome),
		"timeout_reason", observability.TimeoutReasonString(key.TimeoutReason),
		"detail_reason", key.DetailReason,
		"participant", ev.Participant,
		"model", ev.Model,
		"lag_ms", observability.DispositionLag(ev).Milliseconds(),
	}
}
