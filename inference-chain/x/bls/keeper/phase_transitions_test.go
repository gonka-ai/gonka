package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"math/big"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
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

func TestProcessDKGPhaseTransitionForEpoch_SignedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create signed epoch data
	epochID := uint64(6)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	k.SetEpochBLSData(ctx, epochBLSData)

	// Process transition - should do nothing
	err := k.ProcessDKGPhaseTransitionForEpoch(ctx, epochID)
	require.NoError(t, err)

	// Verify phase didn't change
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_SIGNED, storedData.DkgPhase)
}

func TestActiveEpochTracking(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Initially no active epoch
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch)

	// Set an active epoch
	k.SetActiveEpochID(ctx, 123)
	activeEpoch, found = k.GetActiveEpochID(ctx)
	require.True(t, found)
	require.Equal(t, uint64(123), activeEpoch)

	// Clear active epoch
	k.ClearActiveEpochID(ctx)
	activeEpoch, found = k.GetActiveEpochID(ctx)
	require.False(t, found)
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
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.True(t, found)
	require.Equal(t, epochID, activeEpoch) // Still active
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
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch) // Should be cleared
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

// Helper function to create test G2 commitment (compressed format, 96 bytes)
func createTestG2Commitment() []byte {
	// Create a valid compressed G2 point for testing using gnark-crypto
	// We'll use the generator point and scalar multiply by a test value
	var g2Gen bls12381.G2Affine
	_, _, _, g2Gen = bls12381.Generators()

	// Scalar multiply by a test value (e.g., 2)
	var testScalar big.Int
	testScalar.SetInt64(2)

	var testPoint bls12381.G2Affine
	testPoint.ScalarMultiplication(&g2Gen, &testScalar)

	// Return the compressed bytes (96 bytes)
	bytes := testPoint.Bytes()
	return bytes[:]
}

// Tests for CompleteDKG functionality

func TestCompleteDKG_SufficientVerification(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(20)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Set up dealer parts with valid commitments for first 2 participants
	testCommitment := createTestG2Commitment()
	epochBLSData.DealerParts[0].DealerAddress = "participant1"
	epochBLSData.DealerParts[0].Commitments = [][]byte{testCommitment}
	epochBLSData.DealerParts[1].DealerAddress = "participant2"
	epochBLSData.DealerParts[1].Commitments = [][]byte{testCommitment}

	// Set up verification submissions for first 2 participants (sufficient >50%)
	// Both participants approve both dealers
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, true, false}
	epochBLSData.VerificationSubmissions[1].DealerValidity = []bool{true, true, false}

	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Call CompleteDKG
	err := k.CompleteDKG(ctx, &epochBLSData)
	require.NoError(t, err)

	// Verify DKG completed successfully
	require.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, epochBLSData.DkgPhase)
	require.NotNil(t, epochBLSData.GroupPublicKey)
	require.Equal(t, 96, len(epochBLSData.GroupPublicKey)) // Compressed G2 point (96 bytes)

	// Verify epoch data was stored
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, storedData.DkgPhase)
	require.NotNil(t, storedData.GroupPublicKey)
	require.Equal(t, 96, len(storedData.GroupPublicKey)) // Compressed G2 point (96 bytes)

	// Verify active epoch was cleared
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch)
}

func TestCompleteDKG_InsufficientVerification(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data in VERIFYING phase
	epochID := uint64(21)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Only set up verification submission for first participant (insufficient <50%)
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, true, false}

	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Call CompleteDKG
	err := k.CompleteDKG(ctx, &epochBLSData)
	require.NoError(t, err)

	// Verify DKG failed
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, epochBLSData.DkgPhase)
	require.Nil(t, epochBLSData.GroupPublicKey)

	// Verify epoch data was stored
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, storedData.DkgPhase)

	// Verify active epoch was cleared
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch)
}

func TestCompleteDKG_WrongPhase(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data in DEALING phase (wrong phase)
	epochBLSData := createTestEpochBLSData(uint64(22), 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_DEALING

	// Call CompleteDKG
	err := k.CompleteDKG(ctx, &epochBLSData)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in VERIFYING phase")
}

func TestDetermineValidDealersWithConsensus(t *testing.T) {
	k, _ := keepertest.BlsKeeper(t)

	// Create test epoch data with 5 participants
	epochBLSData := createTestEpochBLSData(uint64(23), 5)

	// Set up dealer parts for first 4 participants
	for i := 0; i < 4; i++ {
		epochBLSData.DealerParts[i].DealerAddress = "participant" + string(rune('1'+i))
		epochBLSData.DealerParts[i].Commitments = [][]byte{createTestG2Commitment()}
	}

	// Set up verification submissions with mixed opinions
	// Participant 1: approves dealers 0,1,2; rejects 3,4
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, true, true, false, false}
	// Participant 2: approves dealers 0,1; rejects 2,3,4
	epochBLSData.VerificationSubmissions[1].DealerValidity = []bool{true, true, false, false, false}
	// Participant 3: approves dealers 0,2,3; rejects 1,4
	epochBLSData.VerificationSubmissions[2].DealerValidity = []bool{true, false, true, true, false}

	validDealers, err := k.DetermineValidDealersWithConsensus(&epochBLSData)
	require.NoError(t, err)

	// Expected results:
	// Dealer 0: 3/3 votes (100%) -> VALID
	// Dealer 1: 2/3 votes (67%) -> VALID
	// Dealer 2: 2/3 votes (67%) -> VALID
	// Dealer 3: 1/3 votes (33%) -> INVALID (no submission anyway)
	// Dealer 4: 0/3 votes (0%) -> INVALID (no submission anyway)
	expectedValidDealers := []bool{true, true, true, false, false}
	require.Equal(t, expectedValidDealers, validDealers)
}

func TestDetermineValidDealersWithConsensus_TieVotes(t *testing.T) {
	k, _ := keepertest.BlsKeeper(t)

	// Create test epoch data with 2 participants
	epochBLSData := createTestEpochBLSData(uint64(24), 2)

	// Set up dealer parts for both participants
	epochBLSData.DealerParts[0].DealerAddress = "participant1"
	epochBLSData.DealerParts[0].Commitments = [][]byte{createTestG2Commitment()}
	epochBLSData.DealerParts[1].DealerAddress = "participant2"
	epochBLSData.DealerParts[1].Commitments = [][]byte{createTestG2Commitment()}

	// Set up verification submissions with tie votes (1/2 each)
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, false}
	epochBLSData.VerificationSubmissions[1].DealerValidity = []bool{false, true}

	validDealers, err := k.DetermineValidDealersWithConsensus(&epochBLSData)
	require.NoError(t, err)

	// With tie votes (1/2 = 50%), dealers should be INVALID (need >50%)
	expectedValidDealers := []bool{false, false}
	require.Equal(t, expectedValidDealers, validDealers)
}

func TestCalculateSlotsWithVerificationVectors(t *testing.T) {
	k, _ := keepertest.BlsKeeper(t)

	// Create test epoch data with 3 participants
	epochBLSData := createTestEpochBLSData(uint64(25), 3)

	// Set up verification submissions for first 2 participants
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, false, true}
	epochBLSData.VerificationSubmissions[1].DealerValidity = []bool{false, true, true}
	// Third participant has no verification submission (empty array)

	slotsWithVerification := k.CalculateSlotsWithVerificationVectors(&epochBLSData)

	// Participant 1: slots 0-32 (33 slots)
	// Participant 2: slots 33-65 (33 slots)
	// Total: 66 slots
	expectedSlots := uint32(66)
	require.Equal(t, expectedSlots, slotsWithVerification)
}

func TestProcessDKGPhaseTransitionForEpoch_VerifyingToCompleted(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data in VERIFYING phase with sufficient verification
	epochID := uint64(26)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Set up dealer parts and verification for sufficient participation
	testCommitment := createTestG2Commitment()
	epochBLSData.DealerParts[0].DealerAddress = "participant1"
	epochBLSData.DealerParts[0].Commitments = [][]byte{testCommitment}
	epochBLSData.DealerParts[1].DealerAddress = "participant2"
	epochBLSData.DealerParts[1].Commitments = [][]byte{testCommitment}

	// Set up verification submissions for first 2 participants (sufficient)
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, true, false}
	epochBLSData.VerificationSubmissions[1].DealerValidity = []bool{true, true, false}

	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Set block height at verifying deadline
	ctx = ctx.WithBlockHeight(epochBLSData.VerifyingPhaseDeadlineBlock)

	// Process transition
	err := k.ProcessDKGPhaseTransitionForEpoch(ctx, epochID)
	require.NoError(t, err)

	// Verify DKG completed
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, storedData.DkgPhase)
	require.NotNil(t, storedData.GroupPublicKey)
	require.Equal(t, 96, len(storedData.GroupPublicKey)) // Compressed G2 point (96 bytes)

	// Verify active epoch was cleared
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch)
}

func TestProcessDKGPhaseTransitionForEpoch_VerifyingToFailed(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create test epoch data in VERIFYING phase with insufficient verification
	epochID := uint64(27)
	epochBLSData := createTestEpochBLSData(epochID, 3)
	epochBLSData.DkgPhase = types.DKGPhase_DKG_PHASE_VERIFYING

	// Set up verification submission for only one participant (insufficient)
	epochBLSData.VerificationSubmissions[0].DealerValidity = []bool{true, false, false}

	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, epochID)

	// Set block height at verifying deadline
	ctx = ctx.WithBlockHeight(epochBLSData.VerifyingPhaseDeadlineBlock)

	// Process transition
	err := k.ProcessDKGPhaseTransitionForEpoch(ctx, epochID)
	require.NoError(t, err)

	// Verify DKG failed
	storedData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, storedData.DkgPhase)
	require.Nil(t, storedData.GroupPublicKey)

	// Verify active epoch was cleared
	activeEpoch, found := k.GetActiveEpochID(ctx)
	require.False(t, found)
	require.Equal(t, uint64(0), activeEpoch)
}
