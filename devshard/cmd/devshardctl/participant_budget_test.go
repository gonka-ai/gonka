package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A host that refuses inference drains its own budget, and the timeout vote that would record the
// refusal travels to the same participant. Charging both to one budget lets misbehaviour suppress
// its own accounting.
func TestParticipantBudget_ExhaustedByInferenceStillCarriesTimeoutVotes(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 1)

	limiter.ObserveResult("refusing-host", "/sessions/12/chat/completions", 503)

	require.Error(t, limiter.AllowRequest("refusing-host", "/sessions/12/chat/completions"))
	require.NoError(t, limiter.AllowRequest("refusing-host", "/sessions/12/verify-timeout"))
	require.NoError(t, limiter.AllowRequest("refusing-host", "/sessions/12/challenge-receipt"))
}

// The model-scoped entry point is the one the transport actually calls.
func TestParticipantBudget_ModelScopedEntryPointExemptsTimeoutVotes(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 1)

	limiter.ObserveResultForModel("refusing-host", "Kimi", "/sessions/12/chat/completions", 503)

	require.Error(t, limiter.AllowRequestForModel("refusing-host", "Kimi", "/sessions/12/chat/completions"))
	require.NoError(t, limiter.AllowRequestForModel("refusing-host", "Kimi", "/sessions/12/verify-timeout"))
}

// Gossip and queries are ordinary traffic: only accountability paths are exempt.
func TestParticipantBudget_ExemptsOnlyAccountabilityPaths(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 1)

	limiter.ObserveResult("refusing-host", "/sessions/12/chat/completions", 503)

	require.Error(t, limiter.AllowRequest("refusing-host", "/sessions/12/gossip/txs"))
	require.Error(t, limiter.AllowRequest("refusing-host", "/sessions/12/diffs"))
	require.Error(t, limiter.AllowRequest("refusing-host", ""))
}
