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

	params := k.GetParams(sdkCtx)
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

func TestParticipantAccess_SubmitPocBatch_BlockedParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	params := k.GetParams(sdkCtx)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{testutil.Executor},
	}
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
	require.ErrorIs(t, err, types.ErrParticipantBlocked)
}

func TestParticipantAccess_SubmitPocValidation_BlockedValidatorOrParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	params := k.GetParams(sdkCtx)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{testutil.Creator}, // validator in this msg
	}
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
	require.ErrorIs(t, err, types.ErrParticipantBlocked)
}
