package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A reason the ledger cannot name reaches no counter, so the vocabulary is what decides whether a
// newly reported cause shows up at all.
func TestTimeoutReasonFromString_KeepsWhatItCanName(t *testing.T) {
	for _, reason := range []TimeoutReason{
		TimeoutPhaseTransitionAborted, TimeoutLongResponseAfterContent, TimeoutStateRootDiverged,
		TimeoutContextCanceled, TimeoutDiffDeliveryFailed, TimeoutNotApplied, TimeoutEscrowGone,
		TimeoutVerifierVersionUnsupported, TimeoutVerifierEscrowMissing, TimeoutVerifierInferenceMissing,
		TimeoutVerifierUnreachable, TimeoutVerifierQueueExpired, TimeoutVerifierRPCTimeout,
		TimeoutVoteWeightShort, TimeoutHostServedProbe,
	} {
		require.Equal(t, reason, TimeoutReasonFromString(TimeoutVoteCollectionFailed, string(reason)),
			"a named reason must survive whatever outcome carried it")
	}
}

// An outcome that ends a round without concluding it has no reason of its own.
func TestTimeoutReasonFromString_UnconcludedRoundsReadAsUnknown(t *testing.T) {
	for _, outcome := range []TimeoutOutcome{
		TimeoutSkipped, TimeoutVoteCollectionFailed, TimeoutInsufficientVotes, TimeoutDiffSendFailed,
	} {
		require.Equal(t, TimeoutReasonUnknown, TimeoutReasonFromString(outcome, "something we do not name"))
	}
}

// An applied timeout that carries no recognised reason reports none rather than inventing one.
func TestTimeoutReasonFromString_AnAppliedTimeoutReportsNoReasonItLacks(t *testing.T) {
	require.Empty(t, TimeoutReasonFromString(TimeoutApplied, "something we do not name"))
}

func TestFailureOriginFromDetail_AttributesNamedReasons(t *testing.T) {
	for detail, origin := range map[string]FailureOrigin{
		"context_canceled":            FailureClient,
		"phase_transition_aborted":    FailureGatewayPolicy,
		"long_response_after_content": FailureGatewayPolicy,
		"timeout_not_applied":         FailureGatewayPolicy,
		"nonce_already_finished":      FailureGatewayPolicy,
		"not_finished":                FailureHostResponse,
		"escrow_state_root_diverged":  FailureHostResponse,
	} {
		require.Equal(t, origin, FailureOriginFromDetail(detail), detail)
	}
}

// A verifier that could not answer blames no one on the serving side.
func TestFailureOriginFromDetail_VerifierFailuresAreNotTheExecutorsFault(t *testing.T) {
	for _, detail := range []string{
		"verifier_version_unsupported", "verifier_escrow_missing",
		"verifier_inference_missing", "verifier_unreachable",
		"verifier_queue_expired", "verifier_rpc_timeout",
	} {
		require.Equal(t, FailureTransportUnknown, FailureOriginFromDetail(detail), detail)
	}
}

// long_response_after_content contains "response", which the fragment rules would file as the host's
// fault. Exact names are settled first so ordering cannot decide it.
func TestFailureOriginFromDetail_ExactNamesOutrankFragments(t *testing.T) {
	require.Equal(t, FailureGatewayPolicy, FailureOriginFromDetail("long_response_after_content"))
	require.Equal(t, FailureHostResponse, FailureOriginFromDetail("empty_stream"))
	require.Equal(t, FailureClient, FailureOriginFromDetail("client_cancelled"))
	require.Equal(t, FailureTransportUnknown, FailureOriginFromDetail("nothing recognisable"))
}

// A delivery reason describes what the client got. The detail vocabulary also names things that
// happen before an answer exists, and accepting those here widened the counter key with combinations
// that never occur.
func TestNormalizeDeliveryReason_RejectsWhatCannotDescribeADelivery(t *testing.T) {
	for _, reason := range []string{
		"poc_unavailable_host", "timeout_not_applied", "participant_throttled_no_send",
		"escrow_state_root_diverged", "phase_transition_aborted",
	} {
		require.Equal(t, "unknown", normalizeDeliveryReason(reason),
			"%s happens before there is anything to deliver", reason)
		require.Equal(t, reason, normalizeDetailReason(reason),
			"%s is still a legitimate detail reason", reason)
	}
}

func TestNormalizeDeliveryReason_KeepsWhatADeliveryCanBe(t *testing.T) {
	for _, reason := range []string{
		"empty_stream", "model_burn_empty", "error_stream", "client_cancelled",
		"eof_transport", "http_not_found", DeliveryClientGone,
		DeliveryWarmupProbe, DeliveryThrottleProbe,
	} {
		require.Equal(t, reason, normalizeDeliveryReason(reason))
	}
	require.Empty(t, normalizeDeliveryReason("none"))
	require.Empty(t, normalizeDeliveryReason("  "))
}

// Every reason gatewayAttemptFailureReason can produce has to survive both vocabularies; a bound the
// gateway enforced is a concrete host fault, and collapsing it to unknown loses the per-host report.
func TestNormalizeReasons_KeepEveryBoundedResponseFailure(t *testing.T) {
	for _, reason := range []string{
		"sse_event_too_large", "response_body_too_large",
		"aggregate_response_too_large", "aggregate_fold_too_large",
	} {
		require.Equal(t, reason, normalizeDetailReason(reason))
		require.Equal(t, reason, normalizeDeliveryReason(reason))
	}
}

// The burn split is only visible in the ledger if the delivery vocabulary admits it.
func TestNormalizeDetailReason_AdmitsTheModelBurn(t *testing.T) {
	require.Equal(t, "model_burn_empty", normalizeDetailReason("model_burn_empty"))
}

// A named timeout reason travels as the detail reason as well, and that is a separate whitelist: a
// reason entered in one and not the other still lands in the ledger as "unknown".
func TestBothVocabulariesNameTheSameReasons(t *testing.T) {
	for _, reason := range []TimeoutReason{
		TimeoutPhaseTransitionAborted, TimeoutLongResponseAfterContent, TimeoutStateRootDiverged,
		TimeoutContextCanceled, TimeoutDiffDeliveryFailed, TimeoutNotApplied, TimeoutHostServedProbe,
		TimeoutVerifierQueueExpired, TimeoutVerifierRPCTimeout,
	} {
		require.Equal(t, string(reason), normalizeDetailReason(string(reason)),
			"a reason the timeout vocabulary names must survive the detail vocabulary too")
	}
}
