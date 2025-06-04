package keeper_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func setupMsgServerVerification(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.BlsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func TestSubmitVerificationVector_Success(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(100)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	// Create verification message from first participant
	participant := epochBLSData.Participants[0]
	dealerValidity := []bool{true, false, true} // Mark dealers 0 and 2 as valid

	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: dealerValidity,
	}

	// Submit verification vector
	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify epoch data was updated
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)

	// Verify successful submission
	submission := storedData.VerificationSubmissions[0] // Alice is at index 0
	require.Len(t, submission.DealerValidity, 3)        // Should have 3 dealer validity entries
	require.Equal(t, []bool{true, false, true}, submission.DealerValidity)

	// Verify other participants haven't submitted yet (empty DealerValidity)
	for i := 1; i < len(storedData.VerificationSubmissions); i++ {
		require.Len(t, storedData.VerificationSubmissions[i].DealerValidity, 0)
	}
}

func TestSubmitVerificationVector_EpochNotFound(t *testing.T) {
	_, msgServer, goCtx := setupMsgServerVerification(t)

	// Try to submit for non-existent epoch
	msg := &types.MsgSubmitVerificationVector{
		Creator:        "participant1",
		EpochId:        999,
		DealerValidity: []bool{true, false},
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.NotFound, st.Code())
	require.Contains(t, st.Message(), "no DKG data found for epoch 999")
}

func TestSubmitVerificationVector_WrongPhase(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in DEALING phase
	epochID := uint64(101)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	// Keep in DEALING phase
	k.SetEpochBLSData(ctx, epochBLSData)

	participant := epochBLSData.Participants[0]
	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: []bool{true, false, true},
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Contains(t, st.Message(), "expected VERIFYING")
}

func TestSubmitVerificationVector_DeadlinePassed(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase with deadline already passed
	epochID := uint64(102)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	// Set current block height past the verification deadline
	ctx = ctx.WithBlockHeight(epochBLSData.VerifyingPhaseDeadlineBlock + 1)
	goCtx = ctx

	participant := epochBLSData.Participants[0]
	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: []bool{true, false, true},
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.DeadlineExceeded, st.Code())
	require.Contains(t, st.Message(), "verification deadline passed")
}

func TestSubmitVerificationVector_NotParticipant(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(103)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	// Try to submit from non-participant address
	msg := &types.MsgSubmitVerificationVector{
		Creator:        "not_a_participant",
		EpochId:        epochID,
		DealerValidity: []bool{true, false, true},
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Contains(t, st.Message(), "not_a_participant is not a participant")
}

func TestSubmitVerificationVector_AlreadySubmitted(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(104)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)

	// Mark first participant as having already submitted (index-based)
	participant := epochBLSData.Participants[0]
	epochBLSData.VerificationSubmissions[0] = &types.VerificationVectorSubmission{
		DealerValidity: []bool{true, true, false},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	// Try to submit again from same participant
	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: []bool{false, true, true},
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.AlreadyExists, st.Code())
	require.Contains(t, st.Message(), "has already submitted verification vector")
}

func TestSubmitVerificationVector_WrongDealerValidityLength(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase with 3 participants
	epochID := uint64(105)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	participant := epochBLSData.Participants[0]
	// Provide wrong length dealer validity array (2 instead of 3)
	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: []bool{true, false}, // Wrong length
	}

	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.Error(t, err)
	require.Nil(t, resp)

	// Verify error details
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "dealer_validity length 2 does not match participants count 3")
}

func TestSubmitVerificationVector_EventEmission(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(106)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	participant := epochBLSData.Participants[0]
	msg := &types.MsgSubmitVerificationVector{
		Creator:        participant.Address,
		EpochId:        epochID,
		DealerValidity: []bool{true, false, true},
	}

	// Submit verification vector
	resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify event was emitted
	events := ctx.EventManager().ABCIEvents()
	require.Greater(t, len(events), 0)

	// Find our event by type
	found := false
	for _, event := range events {
		if event.Type == "inference.bls.EventVerificationVectorSubmitted" {
			found = true
			break
		}
	}
	require.True(t, found, "EventVerificationVectorSubmitted event should have been emitted")
}

func TestSubmitVerificationVector_MultipleParticipants(t *testing.T) {
	k, msgServer, goCtx := setupMsgServerVerification(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create test epoch data in VERIFYING phase with 3 participants
	epochID := uint64(107)
	epochBLSData := createTestEpochBLSDataInVerifyingPhase(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)

	// Submit verification vectors from all participants
	for i, participant := range epochBLSData.Participants {
		dealerValidity := make([]bool, len(epochBLSData.Participants))
		// Each participant marks different dealers as valid
		dealerValidity[i] = true
		dealerValidity[(i+1)%len(epochBLSData.Participants)] = true

		msg := &types.MsgSubmitVerificationVector{
			Creator:        participant.Address,
			EpochId:        epochID,
			DealerValidity: dealerValidity,
		}

		resp, err := msgServer.SubmitVerificationVector(goCtx, msg)
		require.NoError(t, err)
		require.NotNil(t, resp)
	}

	// Verify all submissions were stored
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Len(t, storedData.VerificationSubmissions, 3)

	// Verify each submission is stored at the correct participant index
	for i := range epochBLSData.Participants {
		submission := storedData.VerificationSubmissions[i]
		require.Len(t, submission.DealerValidity, 3)

		// Verify the specific dealer validity pattern we set
		expectedPattern := make([]bool, len(epochBLSData.Participants))
		expectedPattern[i] = true
		expectedPattern[(i+1)%len(epochBLSData.Participants)] = true
		require.Equal(t, expectedPattern, submission.DealerValidity)
	}
}

// Helper function to create test epoch BLS data in VERIFYING phase
func createTestEpochBLSDataInVerifyingPhase(epochID uint64, numParticipants int) types.EpochBLSData {
	epochData := createTestEpochBLSData(epochID, numParticipants)

	// Set phase to VERIFYING
	epochData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Set verification deadline in the future
	epochData.VerifyingPhaseDeadlineBlock = 200

	return epochData
}
