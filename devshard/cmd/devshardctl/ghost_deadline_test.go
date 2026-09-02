package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/types"
	"devshard/user"
)

const ghostDeadlineRefusalTimeout = 60

// escrowHoldingGhostNonce builds the state a verifier reconstructs from catch-up diffs for a nonce
// the gateway burned: a pending record composed from the probe's own params.
func escrowHoldingGhostNonce(t *testing.T, params user.InferenceParams) (types.EscrowState, *host.InferencePayload) {
	t.Helper()
	promptHash, err := devshard.CanonicalPromptHash(params.Prompt)
	require.NoError(t, err)
	state := types.EscrowState{
		Config: types.SessionConfig{RefusalTimeout: ghostDeadlineRefusalTimeout},
		Inferences: map[uint64]*types.InferenceRecord{
			1: {
				Status:      types.StatusPending,
				PromptHash:  promptHash,
				Model:       params.Model,
				InputLength: params.InputLength,
				MaxTokens:   params.MaxTokens,
				StartedAt:   params.StartedAt,
			},
		},
	}
	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}
	return state, payload
}

// A verifier measures the refusal deadline as now-in-seconds minus the record's StartedAt. Stamping
// the probe in milliseconds makes that difference permanently negative, so the deadline never passes
// and every accountability vote comes back rejected. The test proxy's verifiers accept without
// running this check, which is why the defect survived them.
func TestGhostProbeParams_RefusalDeadlineCanActuallyPass(t *testing.T) {
	params := ghostProbeParams("llama")
	state, payload := escrowHoldingGhostNonce(t, params)

	// The verifier reads its own clock, so the deadline has to come from one too.
	pastDeadline := time.Now().Unix() + ghostDeadlineRefusalTimeout + 1
	accept, err := host.VerifyRefusedTimeout(
		context.Background(), state, 1, payload, nil, nil, nil, state.Config, pastDeadline,
	)

	require.NoError(t, err)
	require.True(t, accept, "a burned nonce past its refusal deadline must be votable")
}

// Before the deadline the verifier is right to refuse, and the ghost path must not shortcut that.
func TestGhostProbeParams_RefusalDeadlineHoldsUntilItPasses(t *testing.T) {
	params := ghostProbeParams("llama")
	state, payload := escrowHoldingGhostNonce(t, params)

	accept, err := host.VerifyRefusedTimeout(
		context.Background(), state, 1, payload, nil, nil, nil, state.Config, time.Now().Unix(),
	)

	require.NoError(t, err)
	require.False(t, accept, "one second in is not a refusal")
}

// The unit is the whole defect, so it is asserted directly as well.
func TestGhostProbeParams_StampsStartedAtInSeconds(t *testing.T) {
	params := ghostProbeParams("llama")

	require.InDelta(t, time.Now().Unix(), params.StartedAt, 5,
		"StartedAt must be Unix seconds, matching what a verifier compares it against")
}
