package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func TestRespondDealerComplaints_Success(t *testing.T) {
	k, ms, goCtx := setupMsgServer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(103)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_DISPUTING
	epochBLSData.DisputingPhaseDeadlineBlock = 200
	epochBLSData.CandidateValidDealers = []bool{true, true, true}
	epochBLSData.DealerComplaints = []types.DealerComplaint{
		{
			DealerIndex:             1,
			ComplainerIndex:         0,
			DisputedSlotIndex:       33,
			DisputedCiphertextIndex: 0,
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	ctx = ctx.WithBlockHeight(150)
	responseShare := make([]byte, 32)
	responseShare[0] = 1
	responseOpening := make([]byte, 32)
	responseOpening[0] = 2
	_, err := ms.RespondDealerComplaints(sdk.WrapSDKContext(ctx), &types.MsgRespondDealerComplaints{
		Creator:     "participant2", // dealer index 1
		EpochId:     epochID,
		DealerIndex: 1,
		Responses: []types.DealerComplaintResponse{
			{
				ComplainerIndex:         0,
				ResponseShareBytes:      responseShare,
				ResponseOpeningMaterial: responseOpening,
			},
		},
	})
	require.NoError(t, err)

	storedData, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, storedData.DealerComplaints, 1)
	require.True(t, storedData.DealerComplaints[0].ResponseSubmitted)
	require.Equal(t, responseShare, storedData.DealerComplaints[0].ResponseShareBytes)
	require.Equal(t, responseOpening, storedData.DealerComplaints[0].ResponseOpeningMaterial)
}

func TestRespondDealerComplaints_OnlyDealerCanRespond(t *testing.T) {
	k, ms, goCtx := setupMsgServer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(104)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_DISPUTING
	epochBLSData.DisputingPhaseDeadlineBlock = 200
	epochBLSData.CandidateValidDealers = []bool{true, true, true}
	epochBLSData.DealerComplaints = []types.DealerComplaint{
		{
			DealerIndex:             1,
			ComplainerIndex:         0,
			DisputedSlotIndex:       33,
			DisputedCiphertextIndex: 0,
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	ctx = ctx.WithBlockHeight(150)
	_, err := ms.RespondDealerComplaints(sdk.WrapSDKContext(ctx), &types.MsgRespondDealerComplaints{
		Creator:     "participant3", // not dealer index 1
		EpochId:     epochID,
		DealerIndex: 1,
		Responses: []types.DealerComplaintResponse{
			{
				ComplainerIndex:         0,
				ResponseShareBytes:      make([]byte, 32),
				ResponseOpeningMaterial: make([]byte, 32),
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not dealer index")
}
