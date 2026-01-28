package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestParticipantAccess_SubmitNewParticipant_NewRegistrationClosed(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	params, _ := k.GetParams(sdkCtx)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		NewParticipantRegistrationStartHeight: 150, // closed until 150 (opens at 150)
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	_, err := ms.SubmitNewParticipant(sdkCtx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Executor,
		Url:     "url",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrNewParticipantRegistrationClosed)
}

// TestParticipantAccess_SubmitPocBatch_Deprecated verifies V1 PoC batch submission is deprecated when V2 is enabled
func TestParticipantAccess_SubmitPocBatch_Deprecated(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	// Enable V2 mode so V1 handlers return ErrDeprecated
	params, _ := k.GetParams(sdkCtx)
	params.PocParams.PocV2Enabled = true
	require.NoError(t, k.SetParams(sdkCtx, params))

	_, err := ms.SubmitPocBatch(sdkCtx, &types.MsgSubmitPocBatch{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 1,
		BatchId:                  "batch",
		Nonces:                   []int64{1},
		Dist:                     []float64{0.1},
		NodeId:                   "node1",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrDeprecated)
}

// TestParticipantAccess_SubmitPocValidation_Deprecated verifies V1 PoC validation submission is deprecated when V2 is enabled
func TestParticipantAccess_SubmitPocValidation_Deprecated(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	// Enable V2 mode so V1 handlers return ErrDeprecated
	params, _ := k.GetParams(sdkCtx)
	params.PocParams.PocV2Enabled = true
	require.NoError(t, k.SetParams(sdkCtx, params))

	_, err := ms.SubmitPocValidation(sdkCtx, &types.MsgSubmitPocValidation{
		Creator:                  testutil.Creator,
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 1,
		Nonces:                   []int64{1},
		Dist:                     []float64{0.1},
		ReceivedDist:             []float64{0.1},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrDeprecated)
}
