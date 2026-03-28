package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestStartInference_ReturnsError_WhenInjectParamsFails verifies that
// StartInference returns an error when InjectParamsIntoContext fails
// (e.g. corrupted params store), instead of silently continuing with
// stale/zero-valued params.
//
// Without fix: logs warning, continues with broken context
// With fix: returns "cannot start inference with stale params" error
func TestStartInference_ReturnsError_WhenInjectParamsFails(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Setup: epoch and participant so permission check passes
	addr := sample.AccAddress()
	k.SetEffectiveEpochIndex(ctx, 1)
	k.SetActiveParticipantsCache(ctx, types.ActiveParticipants{
		EpochId:      1,
		Participants: []*types.ActiveParticipant{{Index: addr}},
	})

	// Corrupt the params store AFTER setup so InjectParamsIntoContext fails on unmarshal
	keeper.CorruptParamsStore(k, ctx)

	msg := &types.MsgStartInference{
		Creator:     addr,
		RequestedBy: addr,
		InferenceId: "test-inference",
		Model:       "test-model",
	}

	_, err := ms.StartInference(ctx, msg)
	require.Error(t, err, "StartInference should return error when params are corrupted")
	require.Contains(t, err.Error(), "stale params")
}

// TestFinishInference_ReturnsError_WhenInjectParamsFails verifies the same
// for FinishInference.
func TestFinishInference_ReturnsError_WhenInjectParamsFails(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	addr := sample.AccAddress()
	k.SetEffectiveEpochIndex(ctx, 1)
	k.SetActiveParticipantsCache(ctx, types.ActiveParticipants{
		EpochId:      1,
		Participants: []*types.ActiveParticipant{{Index: addr}},
	})

	keeper.CorruptParamsStore(k, ctx)

	msg := &types.MsgFinishInference{
		Creator:     addr,
		ExecutedBy:  addr,
		InferenceId: "test-inference",
	}

	_, err := ms.FinishInference(ctx, msg)
	require.Error(t, err, "FinishInference should return error when params are corrupted")
	require.Contains(t, err.Error(), "stale params")
}
