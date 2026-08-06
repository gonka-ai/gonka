package main

import (
	"context"

	"devshard/accounting"
	"devshard/observability"

	"go.opentelemetry.io/otel/trace"
)

// dispositionLogSink emits one structured nonce_disposition log line per
// terminal accounting event (T3.7). Trace context is rebuilt from the stored
// TraceRef so TraceHandler stamps trace_id/span_id; orphans emit empty ids.
type dispositionLogSink struct{}

func (dispositionLogSink) OnDisposition(ev accounting.DispositionEvent) {
	ctx := context.Background()
	fields := dispositionLogFields(ev)
	if ev.Trace.IsZero() {
		fields = append(fields, "trace_id", "", "span_id", "")
	} else {
		flags := trace.TraceFlags(0)
		if ev.Trace.Sampled {
			flags = trace.FlagsSampled
		}
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    ev.Trace.TraceID,
			SpanID:     ev.Trace.SpanID,
			TraceFlags: flags,
			Remote:     true,
		})
		ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
	}
	logInferenceStage(ctx, ev.EscrowID, ev.Nonce, "nonce_disposition", fields...)
}

func dispositionLogFields(ev accounting.DispositionEvent) []any {
	key := ev.Key
	lagMS := int64(0)
	if !ev.SendAt.IsZero() && !ev.ObservedAt.IsZero() {
		lagMS = ev.ObservedAt.Sub(ev.SendAt).Milliseconds()
	}
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
		"lag_ms", lagMS,
	}
}
