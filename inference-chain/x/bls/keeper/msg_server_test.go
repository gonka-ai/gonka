package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func setupMsgServer(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.BlsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func TestMsgServer(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	require.NotNil(t, ms)
	require.NotNil(t, ctx)
	require.NotEmpty(t, k)
}

func TestSubmitGroupKeyValidationSignature_AlreadySigned(t *testing.T) {
	k, ms, goCtx := setupMsgServer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data for epoch 2 that is already signed
	epochID := uint64(2)
	epochBLSData := types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots:    4,
		GroupPublicKey: make([]byte, 96), // Valid length
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	// Create previous epoch data (epoch 1)
	previousEpochBLSData := types.EpochBLSData{
		EpochId:        1,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots:    4,
		GroupPublicKey: make([]byte, 96),
		Participants: []types.BLSParticipantInfo{
			{
				Address:        "test_address",
				SlotStartIndex: 0,
				SlotEndIndex:   1,
			},
		},
	}
	k.SetEpochBLSData(ctx, previousEpochBLSData)

	// Create message to submit validation signature
	msg := &types.MsgSubmitGroupKeyValidationSignature{
		Creator:          "test_address",
		NewEpochId:       epochID,
		SlotIndices:      []uint32{0, 1},
		PartialSignature: make([]byte, 48), // Valid signature length
	}

	// Submit the message - should succeed without error
	resp, err := ms.SubmitGroupKeyValidationSignature(goCtx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify epoch is still in SIGNED phase (unchanged)
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_SIGNED, storedData.DkgPhase)
}
