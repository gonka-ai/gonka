package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

const ghostDeadlineRefusalTimeout = 60

func ghostNonceVerifier(t *testing.T, params user.InferenceParams) (*state.StateMachine, *host.InferencePayload, func(height uint64)) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	config.RefusalTimeout = ghostDeadlineRefusalTimeout
	sm := statetest.MustStateMachine(t, "escrow-1", config, group, 1_000_000, userKey.Address(), signing.NewSecp256k1Verifier())

	promptHash, err := devshard.CanonicalPromptHash(params.Prompt)
	require.NoError(t, err)
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, PromptHash: promptHash, Model: params.Model,
		InputLength: params.InputLength, MaxTokens: params.MaxTokens,
	}}}
	_, err = sm.ApplyDiff(testutil.SignDiff(t, userKey, "escrow-1", 1, []*types.DevshardTx{start}))
	require.NoError(t, err)

	floorAt := func(height uint64) {
		st := sm.ExportState()
		rec := st.Inferences[1]
		rec.StartedAtHeight, rec.StartedAtBlockHash, rec.StartedAtTimeMs = 1, []byte{0x01}, testutil.HeightClockBlockMs
		floor := heightsync.NewFloorIndex()
		floor.Observe(st.LatestNonce, []heightsync.FloorClaim{{Signer: 0, Height: height, Hash: []byte{byte(height)}}})
		require.NoError(t, sm.RestoreStateWithFloor(st, floor))
	}
	payload := &host.InferencePayload{
		Prompt: params.Prompt, Model: params.Model, InputLength: params.InputLength, MaxTokens: params.MaxTokens,
	}
	return sm, payload, floorAt
}

func verifyGhostRefusal(t *testing.T, sm *state.StateMachine, payload *host.InferencePayload) bool {
	t.Helper()
	st, rec, ok := sm.SnapshotWithInference(1)
	require.True(t, ok)
	accept, err := host.VerifyRefusedTimeout(context.Background(), st, 1, rec, payload, nil, nil, nil, sm)
	require.NoError(t, err)
	return accept
}

func TestGhostProbeParams_RefusalDeadlineCanActuallyPass(t *testing.T) {
	sm, payload, floorAt := ghostNonceVerifier(t, ghostProbeParams("llama"))
	floorAt(1 + ghostDeadlineRefusalTimeout)

	require.True(t, verifyGhostRefusal(t, sm, payload), "a burned nonce past its refusal window must be votable")
}

func TestGhostProbeParams_RefusalDeadlineHoldsUntilItPasses(t *testing.T) {
	sm, payload, floorAt := ghostNonceVerifier(t, ghostProbeParams("llama"))
	floorAt(ghostDeadlineRefusalTimeout)

	require.False(t, verifyGhostRefusal(t, sm, payload), "one block short of the window is not a refusal")
}

func TestGhostProbeParams_StampsStartedAtInSeconds(t *testing.T) {
	params := ghostProbeParams("llama")

	require.InDelta(t, time.Now().Unix(), params.StartedAt, 5, "StartedAt must be Unix seconds")
}
