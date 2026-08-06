package observability_test

import (
	"testing"

	"devshard/accounting"
	"devshard/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

// C4 — span attribute values must be byte-identical to the Prometheus label
// values for the same dimension (observability-trace-correlation-plan §2 / §9).
func TestSpanAttrValuesMatchPrometheusLabelValues(t *testing.T) {
	t.Parallel()

	for _, d := range allDispositions {
		require.Equal(t, string(d), observability.DispositionString(d), "Disposition %q", d)
		kv := observability.AttrDisposition.String(observability.DispositionString(d))
		require.Equal(t, string(d), kv.Value.AsString())
	}
	for _, p := range allPhases {
		require.Equal(t, string(p), observability.PhaseString(p), "Phase %q", p)
	}
	for _, m := range allQuarantineModes {
		require.Equal(t, string(m), observability.QuarantineModeString(m), "QuarantineMode %q", m)
	}
	for _, r := range allNoSendReasons {
		require.Equal(t, string(r), observability.NoSendReasonString(r), "NoSendReason %q", r)
	}
	for _, o := range allFailureOrigins {
		require.Equal(t, string(o), observability.FailureOriginString(o), "FailureOrigin %q", o)
	}
	for _, k := range allTimeoutKinds {
		require.Equal(t, string(k), observability.TimeoutKindString(k), "TimeoutKind %q", k)
	}
	for _, o := range allTimeoutOutcomes {
		require.Equal(t, string(o), observability.TimeoutOutcomeString(o), "TimeoutOutcome %q", o)
	}
	for _, r := range allTimeoutReasons {
		require.Equal(t, string(r), observability.TimeoutReasonString(r), "TimeoutReason %q", r)
	}
	for _, k := range allProtocolKinds {
		require.Equal(t, string(k), observability.ProtocolKindString(k), "ProtocolKind %q", k)
	}

	// CounterKeyAttrs must emit the same strings Prometheus uses for labels.
	key := accounting.CounterKey{
		SlotID:                 1,
		Disposition:            accounting.DispositionUnfinishedRefused,
		DispatchPhase:          accounting.PhaseNormal,
		TimeoutEvaluationPhase: accounting.PhasePoC,
		QuarantineMode:         accounting.QuarantineShadow,
		NoSendReason:           accounting.NoSendParticipantThrottled,
		FailureOrigin:          accounting.FailureHostResponse,
		TimeoutKind:            accounting.TimeoutRefused,
		TimeoutOutcome:         accounting.TimeoutVoteCollectionFailed,
		TimeoutReason:          accounting.TimeoutPhaseTransitionAborted,
		DetailReason:           "host_503",
	}
	attrs := observability.CounterKeyAttrs(key)
	byKey := map[attribute.Key]string{}
	for _, a := range attrs {
		byKey[a.Key] = a.Value.AsString()
	}
	require.Equal(t, string(key.Disposition), byKey[observability.AttrDisposition])
	require.Equal(t, string(key.DispatchPhase), byKey[observability.AttrDispatchPhase])
	require.Equal(t, string(key.TimeoutEvaluationPhase), byKey[observability.AttrTimeoutEvaluationPhase])
	require.Equal(t, string(key.QuarantineMode), byKey[observability.AttrQuarantineMode])
	require.Equal(t, string(key.NoSendReason), byKey[observability.AttrNoSendReason])
	require.Equal(t, string(key.FailureOrigin), byKey[observability.AttrFailureOrigin])
	require.Equal(t, string(key.TimeoutKind), byKey[observability.AttrTimeoutKind])
	require.Equal(t, string(key.TimeoutOutcome), byKey[observability.AttrTimeoutOutcome])
	require.Equal(t, string(key.TimeoutReason), byKey[observability.AttrTimeoutReason])
	require.Equal(t, key.DetailReason, byKey[observability.AttrDetailReason])
}

func TestGatewaySpanNamesAreUnique(t *testing.T) {
	t.Parallel()
	names := []string{
		observability.SpanNameGatewayAttempt,
		observability.SpanNameAttemptDispatch,
		observability.SpanNameAttemptPrefill,
		observability.SpanNameAttemptStream,
		observability.SpanNameNonceDisposition,
		"gateway.request", // existing root; must not collide
	}
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		require.NotEmpty(t, n)
		_, dup := seen[n]
		require.False(t, dup, "duplicate span name %q", n)
		seen[n] = struct{}{}
	}
}

func TestAttrKeysAreUnique(t *testing.T) {
	t.Parallel()
	keys := []attribute.Key{
		observability.AttrEscrowID,
		observability.AttrNonce,
		observability.AttrSlotID,
		observability.AttrParticipantKey,
		observability.AttrModel,
		observability.AttrDisposition,
		observability.AttrDispatchPhase,
		observability.AttrTimeoutEvaluationPhase,
		observability.AttrQuarantineMode,
		observability.AttrNoSendReason,
		observability.AttrFailureOrigin,
		observability.AttrTimeoutKind,
		observability.AttrTimeoutOutcome,
		observability.AttrTimeoutReason,
		observability.AttrDetailReason,
		observability.AttrProtocolKind,
		observability.AttrOriginTraceID,
		observability.AttrStream,
		observability.AttrOutputChunks,
		observability.AttrContentChunks,
		observability.AttrOutputBytes,
		observability.AttrStallCount,
		observability.AttrAttemptRole,
		observability.AttrAttemptStartReason,
		observability.AttrAttemptIndex,
		observability.AttrAttemptTriggerNonce,
		observability.AttrHostID,
	}
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s := string(k)
		require.NotEmpty(t, s)
		_, dup := seen[s]
		require.False(t, dup, "duplicate attribute key %q", s)
		seen[s] = struct{}{}
	}
}

// Exhaustive enum lists — adding a constant to accounting/types.go without
// extending the matching slice here fails coverage of C4 for that value.
// Keep in sync with accounting/types.go:13-107.

var allDispositions = []accounting.Disposition{
	accounting.DispositionProtocolOnly,
	accounting.DispositionGhost,
	accounting.DispositionFinishedUsed,
	accounting.DispositionFinishedUnused,
	accounting.DispositionFinishedUsageUnknown,
	accounting.DispositionUnfinishedRefused,
	accounting.DispositionUnfinishedExecution,
}

var allPhases = []accounting.Phase{
	accounting.PhaseNormal,
	accounting.PhasePoC,
	accounting.PhaseConfirmationPoC,
}

var allQuarantineModes = []accounting.QuarantineMode{
	accounting.QuarantineNone,
	accounting.QuarantineProbe,
	accounting.QuarantineShadow,
	accounting.QuarantineProbation,
}

var allNoSendReasons = []accounting.NoSendReason{
	accounting.NoSendPoCUnavailable,
	accounting.NoSendParticipantThrottled,
	accounting.NoSendParticipantCapability,
	accounting.NoSendNoCompatibleAfterStale,
	accounting.NoSendUnknown,
}

var allFailureOrigins = []accounting.FailureOrigin{
	accounting.FailureHostResponse,
	accounting.FailureGatewayPolicy,
	accounting.FailureClient,
	accounting.FailureTransportUnknown,
}

var allTimeoutKinds = []accounting.TimeoutKind{
	accounting.TimeoutRefused,
	accounting.TimeoutExecution,
}

var allTimeoutOutcomes = []accounting.TimeoutOutcome{
	accounting.TimeoutSkipped,
	accounting.TimeoutVoteCollectionFailed,
	accounting.TimeoutInsufficientVotes,
	accounting.TimeoutDiffSendFailed,
	accounting.TimeoutApplied,
}

var allTimeoutReasons = []accounting.TimeoutReason{
	accounting.TimeoutPhaseTransitionAborted,
	accounting.TimeoutLongResponseAfterContent,
	accounting.TimeoutStateRootDiverged,
	accounting.TimeoutContextCanceled,
	accounting.TimeoutDiffDeliveryFailed,
	accounting.TimeoutNotApplied,
	accounting.TimeoutReasonUnknown,
}

var allProtocolKinds = []accounting.ProtocolKind{
	accounting.ProtocolReceiptApplied,
	accounting.ProtocolFinishApplied,
	accounting.ProtocolTimeoutApplied,
	accounting.ProtocolChallenged,
	accounting.ProtocolValidated,
	accounting.ProtocolInvalidated,
}
