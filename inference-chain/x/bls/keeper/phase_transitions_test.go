package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestTransitionToVerifyingPhase_SufficientParticipation(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data with 3 participants, 100 total slots
	epochID := uint64(1)
	epochBLSData := createTestEpochBLSData(epochID, 3)

	// Mark first 2 participants as having submitted dealer parts (covers 60% of slots)
	epochBLSData.DealerParts[0].DealerAddress = "participant1"
	epochBLSData.DealerParts[1].DealerAddress = "participant2"

	// Store the epoch data
	k.SetEpochBLSData(ctx, epochBLSData)

	// Set current block height to trigger transition
	ctx = ctx.WithBlockHeight(epochBLSData.DealingPhaseDeadlineBlock)

	// Call the transition function
	err := k.TransitionToVerifyingPhase(ctx, &epochBLSData)
	require.NoError(t, err)

	// Verify the phase changed to VERIFYING
	require.Equal(t, types.DKGPhase_DKG_PHASE_VERIFYING, epochBLSData.DkgPhase)

	// Verify the verifying phase deadline was set
	require.Greater(t, epochBLSData.VerifyingPhaseDeadlineBlock, epochBLSData.DealingPhaseDeadlineBlock)

	// Verify epoch data was stored
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_VERIFYING, storedData.DkgPhase)
}

func TestTransitionToVerifyingPhase_InsufficientParticipation(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data with 3 participants, 100 total slots
	epochID := uint64(2)
	epochBLSData := createTestEpochBLSData(epochID, 3)

	// Mark only first participant as having submitted dealer parts (covers only 34% of slots)
	epochBLSData.DealerParts[0].DealerAddress = "participant1"

	// Store the epoch data
	k.SetEpochBLSData(ctx, epochBLSData)

	// Set current block height to trigger transition
	ctx = ctx.WithBlockHeight(epochBLSData.DealingPhaseDeadlineBlock)

	// Call the transition function
	err := k.TransitionToVerifyingPhase(ctx, &epochBLSData)
	require.NoError(t, err)

	// Verify the phase changed to FAILED
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, epochBLSData.DkgPhase)

	// Verify epoch data was stored
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, storedData.DkgPhase)
}

func TestTransitionToVerifyingPhase_WrongPhase(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data already in VERIFYING phase
	epochBLSData := createTestEpochBLSData(uint64(3), 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Call the transition function
	err := k.TransitionToVerifyingPhase(ctx, &epochBLSData)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in DEALING phase")
}

func TestCalculateSlotsWithDealerParts(t *testing.T) {
	k, _ := keepertest.BlsKeeper(t)

	// Create test epoch data with 3 participants
	epochBLSData := createTestEpochBLSData(uint64(4), 3)

	// Mark first 2 participants as having submitted dealer parts
	epochBLSData.DealerParts[0].DealerAddress = "participant1"
	epochBLSData.DealerParts[1].DealerAddress = "participant2"

	// Calculate slots with dealer parts
	slotsWithDealerParts := k.CalculateSlotsWithDealerParts(&epochBLSData)

	// Participant 1: slots 0-32 (33 slots)
	// Participant 2: slots 33-65 (33 slots)
	// Total: 66 slots
	expectedSlots := uint32(66)
	require.Equal(t, expectedSlots, slotsWithDealerParts)
}

func TestProcessDKGPhaseTransitionForEpoch_NotFound(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Try to process transition for non-existent epoch
	err := k.ProcessDKGPhaseTransitionForEpoch(ctx, uint64(999))
	require.Error(t, err)
	require.Contains(t, err.Error(), "EpochBLSData not found")
}

func TestProcessDKGPhaseTransitionForEpoch_CompletedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create completed epoch data
	epochID := uint64(5)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_COMPLETED
	k.SetEpochBLSData(ctx, epochBLSData)

	// Process transition - should do nothing
	err := k.ProcessDKGPhaseTransitionForEpoch(ctx, epochID)
	require.NoError(t, err)

	// Verify phase didn't change
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, storedData.DkgPhase)
}

func TestActiveEpochTracking(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Initially no active epoch
	activeEpoch := k.GetActiveEpochID(ctx)
	require.Equal(t, uint64(0), activeEpoch)

	// Set an active epoch
	k.SetActiveEpochID(ctx, 123)
	activeEpoch = k.GetActiveEpochID(ctx)
	require.Equal(t, uint64(123), activeEpoch)

	// Clear active epoch
	k.SetActiveEpochID(ctx, 0)
	activeEpoch = k.GetActiveEpochID(ctx)
	require.Equal(t, uint64(0), activeEpoch)
}

func TestProcessDKGPhaseTransitions_NoActiveEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// No active epoch - should return without error
	err := k.ProcessDKGPhaseTransitions(ctx)
	require.NoError(t, err)
}

func TestProcessDKGPhaseTransitions_ActiveEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create and store epoch data
	epochID := uint64(10)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Set block height before deadline - should not transition
	ctx = ctx.WithBlockHeight(epochBLSData.DealingPhaseDeadlineBlock - 1)
	err := k.ProcessDKGPhaseTransitions(ctx)
	require.NoError(t, err)

	// Verify phase didn't change
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_DEALING, storedData.DkgPhase)
	require.Equal(t, epochID, k.GetActiveEpochID(ctx)) // Still active
}

func TestActiveEpochClearedOnFailure(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create epoch data with insufficient participation
	epochID := uint64(11)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	// Only mark first participant as having submitted (insufficient)
	epochBLSData.DealerParts[0].DealerAddress = "participant1"

	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Trigger transition at deadline
	ctx = ctx.WithBlockHeight(epochBLSData.DealingPhaseDeadlineBlock)
	err := k.TransitionToVerifyingPhase(ctx, &epochBLSData)
	require.NoError(t, err)

	// Verify DKG failed and active epoch was cleared
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, storedData.DkgPhase)
	require.Equal(t, uint64(0), k.GetActiveEpochID(ctx)) // Should be cleared
}

// Helper function to create test epoch BLS data
func createTestEpochBLSData(epochID uint64, numParticipants int) types.EpochBLSData {
	participants := make([]types.BLSParticipantInfo, numParticipants)
	dealerParts := make([]*types.DealerPartStorage, numParticipants)

	totalSlots := uint32(100)
	slotsPerParticipant := totalSlots / uint32(numParticipants)

	for i := 0; i < numParticipants; i++ {
		startIndex := uint32(i) * slotsPerParticipant
		var endIndex uint32
		if i == numParticipants-1 {
			// Last participant gets remaining slots
			endIndex = totalSlots - 1
		} else {
			endIndex = startIndex + slotsPerParticipant - 1
		}

		participants[i] = types.BLSParticipantInfo{
			Address:            "participant" + string(rune('1'+i)),
			PercentageWeight:   math.LegacyNewDecWithPrec(33, 2), // 33%
			Secp256K1PublicKey: []byte("pubkey" + string(rune('1'+i))),
			SlotStartIndex:     startIndex,
			SlotEndIndex:       endIndex,
		}

		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress:     "", // Will be set when participant "submits"
			Commitments:       [][]byte{},
			ParticipantShares: []*types.EncryptedSharesForParticipant{},
		}
	}

	// Initialize verification submissions array with correct size
	verificationSubmissions := make([]*types.VerificationVectorSubmission, numParticipants)
	for i := range verificationSubmissions {
		verificationSubmissions[i] = &types.VerificationVectorSubmission{
			DealerValidity: []bool{},
		}
	}

	return types.EpochBLSData{
		EpochId:                     epochID,
		ITotalSlots:                 totalSlots,
		TSlotsDegree:                50, // floor(100/2)
		Participants:                participants,
		DkgPhase:                    types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock:   100,
		VerifyingPhaseDeadlineBlock: 150,
		GroupPublicKey:              nil,
		DealerParts:                 dealerParts,
		VerificationSubmissions:     verificationSubmissions,
	}
}
