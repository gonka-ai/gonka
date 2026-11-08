package observability

import (
	"devshard/accounting"

	"go.opentelemetry.io/otel/attribute"
)

// Span attribute keys used by gateway/host telemetry. Values for taxonomy
// dimensions must be byte-identical to the Prometheus label values for the
// same dimension (see attrs_contract_test.go / C4).
const (
	AttrEscrowID               = attribute.Key("escrow.id")
	AttrNonce                  = attribute.Key("devshard.nonce")
	AttrSlotID                 = attribute.Key("devshard.slot_id")
	AttrParticipantKey         = attribute.Key("participant.key")
	AttrModel                  = attribute.Key("model")
	AttrDisposition            = attribute.Key("devshard.disposition")
	AttrDispatchPhase          = attribute.Key("devshard.dispatch_phase")
	AttrTimeoutEvaluationPhase = attribute.Key("devshard.timeout_evaluation_phase")
	AttrQuarantineMode         = attribute.Key("devshard.quarantine_mode")
	AttrNoSendReason           = attribute.Key("devshard.no_send_reason")
	AttrFailureOrigin          = attribute.Key("devshard.failure_origin")
	AttrTimeoutKind            = attribute.Key("devshard.timeout_kind")
	AttrTimeoutOutcome         = attribute.Key("devshard.timeout_outcome")
	AttrTimeoutReason          = attribute.Key("devshard.timeout_reason")
	AttrDetailReason           = attribute.Key("devshard.detail_reason")
	AttrProtocolKind           = attribute.Key("devshard.protocol_kind")
	AttrOriginTraceID          = attribute.Key("devshard.origin_trace_id")
	AttrStream                 = attribute.Key("devshard.stream")
	AttrOutputChunks           = attribute.Key("devshard.output_chunks")
	AttrContentChunks          = attribute.Key("devshard.content_chunks")
	AttrOutputBytes            = attribute.Key("devshard.output_bytes")
	AttrStallCount             = attribute.Key("devshard.stall_count")
	AttrAttemptRole            = attribute.Key("devshard.attempt.role")
	AttrAttemptStartReason     = attribute.Key("devshard.attempt.start_reason")
	AttrAttemptIndex           = attribute.Key("devshard.attempt.index")
	AttrAttemptTriggerNonce    = attribute.Key("devshard.attempt.trigger_nonce")
	AttrHostID                 = attribute.Key("devshard.host.id")
	AttrMLNodeID               = attribute.Key("mlnode.node.id")
	AttrMLNodeEndpoint         = attribute.Key("mlnode.endpoint")
	AttrMLNodeLockID           = attribute.Key("mlnode.lock_id")
)

// DispositionString returns the canonical string form of a disposition for
// span attributes and Prometheus labels. Identity mapping — kept as a helper
// so call sites never invent a second spelling.
func DispositionString(d accounting.Disposition) string { return string(d) }

// PhaseString returns the canonical string form of a dispatch/timeout phase.
func PhaseString(p accounting.Phase) string { return string(p) }

// QuarantineModeString returns the canonical quarantine mode string.
func QuarantineModeString(m accounting.QuarantineMode) string { return string(m) }

// NoSendReasonString returns the canonical no-send reason string.
func NoSendReasonString(r accounting.NoSendReason) string { return string(r) }

// FailureOriginString returns the canonical failure origin string.
func FailureOriginString(o accounting.FailureOrigin) string { return string(o) }

// TimeoutKindString returns the canonical timeout kind string.
func TimeoutKindString(k accounting.TimeoutKind) string { return string(k) }

// TimeoutOutcomeString returns the canonical timeout outcome string.
func TimeoutOutcomeString(o accounting.TimeoutOutcome) string { return string(o) }

// TimeoutReasonString returns the canonical timeout reason string.
func TimeoutReasonString(r accounting.TimeoutReason) string { return string(r) }

// ProtocolKindString returns the canonical protocol kind string.
func ProtocolKindString(k accounting.ProtocolKind) string { return string(k) }

// CounterKeyAttrs builds the taxonomy attribute set from an accounting
// CounterKey. Empty optional fields are omitted so TraceQL filters stay clean.
func CounterKeyAttrs(key accounting.CounterKey) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 11)
	if key.Disposition != "" {
		attrs = append(attrs, AttrDisposition.String(DispositionString(key.Disposition)))
	}
	if key.DispatchPhase != "" {
		attrs = append(attrs, AttrDispatchPhase.String(PhaseString(key.DispatchPhase)))
	}
	if key.TimeoutEvaluationPhase != "" {
		attrs = append(attrs, AttrTimeoutEvaluationPhase.String(PhaseString(key.TimeoutEvaluationPhase)))
	}
	if key.QuarantineMode != "" {
		attrs = append(attrs, AttrQuarantineMode.String(QuarantineModeString(key.QuarantineMode)))
	}
	if key.NoSendReason != "" {
		attrs = append(attrs, AttrNoSendReason.String(NoSendReasonString(key.NoSendReason)))
	}
	if key.FailureOrigin != "" {
		attrs = append(attrs, AttrFailureOrigin.String(FailureOriginString(key.FailureOrigin)))
	}
	if key.TimeoutKind != "" {
		attrs = append(attrs, AttrTimeoutKind.String(TimeoutKindString(key.TimeoutKind)))
	}
	if key.TimeoutOutcome != "" {
		attrs = append(attrs, AttrTimeoutOutcome.String(TimeoutOutcomeString(key.TimeoutOutcome)))
	}
	if key.TimeoutReason != "" {
		attrs = append(attrs, AttrTimeoutReason.String(TimeoutReasonString(key.TimeoutReason)))
	}
	if key.DetailReason != "" {
		attrs = append(attrs, AttrDetailReason.String(key.DetailReason))
	}
	attrs = append(attrs, AttrSlotID.Int(int(key.SlotID)))
	return attrs
}
