package inference

import (
	"encoding/base64"
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

func TestBLSKeyGenerationIntegration(t *testing.T) {
	keeper, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Create codec for the test
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Create full participant objects and store them
	participants := []*types.Participant{
		{
			Index:           "cosmos1alice",
			Address:         "cosmos1alice",
			ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("alice_validator_key")), // Consensus key
			WorkerPublicKey: base64.StdEncoding.EncodeToString([]byte("alice_secp256k1_key")), // Worker key for BLS
			Weight:          50,
			Status:          types.ParticipantStatus_ACTIVE,
		},
		{
			Index:           "cosmos1bob",
			Address:         "cosmos1bob",
			ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("bob_validator_key")), // Consensus key
			WorkerPublicKey: base64.StdEncoding.EncodeToString([]byte("bob_secp256k1_key")), // Worker key for BLS
			Weight:          30,
			Status:          types.ParticipantStatus_ACTIVE,
		},
		{
			Index:           "cosmos1charlie",
			Address:         "cosmos1charlie",
			ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("charlie_validator_key")), // Consensus key
			WorkerPublicKey: base64.StdEncoding.EncodeToString([]byte("charlie_secp256k1_key")), // Worker key for BLS
			Weight:          20,
			Status:          types.ParticipantStatus_ACTIVE,
		},
	}

	// Store participants in the keeper
	for _, participant := range participants {
		keeper.SetParticipant(ctx, *participant)
	}

	// Create test active participants
	activeParticipants := []*types.ActiveParticipant{
		{
			Index:        "cosmos1alice",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("alice_validator_key")),
			Weight:       50,
		},
		{
			Index:        "cosmos1bob",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("bob_validator_key")),
			Weight:       30,
		},
		{
			Index:        "cosmos1charlie",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("charlie_validator_key")),
			Weight:       20,
		},
	}

	// Create app module instance
	appModule := NewAppModule(
		cdc,
		keeper,
		nil, // accountKeeper not needed for this test
		nil, // bankKeeper not needed for this test
		nil, // groupMsgServer not needed for this test
	)

	// Test the initiateBLSKeyGeneration function
	epochID := uint64(123)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	// Verify that EpochBLSData was created
	epochBLSData, found := keeper.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.True(t, found, "EpochBLSData should be created")
	require.Equal(t, epochID, epochBLSData.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData.DkgPhase)
	require.Len(t, epochBLSData.Participants, 3)

	// Verify DealerParts array is properly initialized for participant indexing
	require.Len(t, epochBLSData.DealerParts, 3, "DealerParts should have same length as participants for direct indexing")
	for i, dealerPart := range epochBLSData.DealerParts {
		require.NotNil(t, dealerPart, "DealerParts[%d] should not be nil", i)
		require.Empty(t, dealerPart.DealerAddress, "DealerParts[%d] should have empty address initially", i)
		require.Empty(t, dealerPart.Commitments, "DealerParts[%d] should have empty commitments initially", i)
		require.Empty(t, dealerPart.ParticipantShares, "DealerParts[%d] should have empty shares initially", i)
	}

	// Verify participant data conversion
	totalWeight := int64(100) // 50 + 30 + 20
	expectedWeights := []math.LegacyDec{
		math.LegacyNewDec(50).Quo(math.LegacyNewDec(totalWeight)).Mul(math.LegacyNewDec(100)), // 50%
		math.LegacyNewDec(30).Quo(math.LegacyNewDec(totalWeight)).Mul(math.LegacyNewDec(100)), // 30%
		math.LegacyNewDec(20).Quo(math.LegacyNewDec(totalWeight)).Mul(math.LegacyNewDec(100)), // 20%
	}

	for i, participant := range epochBLSData.Participants {
		require.Equal(t, activeParticipants[i].Index, participant.Address)
		require.True(t, expectedWeights[i].Equal(participant.PercentageWeight))
		require.NotEmpty(t, participant.Secp256K1PublicKey)
	}

	// Verify slot assignment
	require.Equal(t, uint32(0), epochBLSData.Participants[0].SlotStartIndex)
	require.True(t, epochBLSData.Participants[0].SlotEndIndex > epochBLSData.Participants[0].SlotStartIndex)

	// Verify slots are contiguous
	for i := 1; i < len(epochBLSData.Participants); i++ {
		require.Equal(t,
			epochBLSData.Participants[i-1].SlotEndIndex+1,
			epochBLSData.Participants[i].SlotStartIndex,
			"Slots should be contiguous")
	}
}

func TestBLSKeyGenerationWithEmptyParticipants(t *testing.T) {
	keeper, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Create codec for the test
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Create app module instance
	appModule := NewAppModule(
		cdc,
		keeper,
		nil, // accountKeeper not needed for this test
		nil, // bankKeeper not needed for this test
		nil, // groupMsgServer not needed for this test
	)

	// Test with empty participants
	epochID := uint64(456)
	appModule.initiateBLSKeyGeneration(ctx, epochID, []*types.ActiveParticipant{})

	// Verify that no EpochBLSData was created
	_, found := keeper.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created for empty participants")
}

func TestBLSKeyGenerationWithInvalidKeys(t *testing.T) {
	keeper, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Create codec for the test
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Create participant with invalid WorkerPublicKey
	participant := types.Participant{
		Index:           "cosmos1alice",
		Address:         "cosmos1alice",
		ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("alice_validator_key")), // Valid consensus key
		WorkerPublicKey: "invalid_base64_key!@#",                                          // Invalid base64 worker key
		Weight:          100,
		Status:          types.ParticipantStatus_ACTIVE,
	}

	// Store participant in the keeper
	keeper.SetParticipant(ctx, participant)

	// Create test active participants
	activeParticipants := []*types.ActiveParticipant{
		{
			Index:        "cosmos1alice",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("alice_validator_key")), // This is ignored now
			Weight:       100,
		},
	}

	// Create app module instance
	appModule := NewAppModule(
		cdc,
		keeper,
		nil, // accountKeeper not needed for this test
		nil, // bankKeeper not needed for this test
		nil, // groupMsgServer not needed for this test
	)

	// Test the initiateBLSKeyGeneration function
	epochID := uint64(789)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	// Verify that no EpochBLSData was created due to invalid key
	_, found := keeper.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created when all participants have invalid keys")
}

// TestBLSKeyGenerationWorkerKeyValidation tests that WorkerPublicKey is used instead of ValidatorKey
func TestBLSKeyGenerationWorkerKeyValidation(t *testing.T) {
	keeper, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Create codec for the test
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Create full participant objects and store them with DIFFERENT ValidatorKey and WorkerPublicKey
	participants := []*types.Participant{
		{
			Index:           "cosmos1alice",
			Address:         "cosmos1alice",
			ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("alice_validator_ed25519_key")),        // Consensus key (Ed25519)
			WorkerPublicKey: base64.StdEncoding.EncodeToString([]byte("alice_secp256k1_worker_key_33bytes")), // Worker key for BLS (secp256k1)
			Weight:          50,
			Status:          types.ParticipantStatus_ACTIVE,
		},
		{
			Index:           "cosmos1bob",
			Address:         "cosmos1bob",
			ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("bob_validator_ed25519_key__")),       // Consensus key (Ed25519)
			WorkerPublicKey: base64.StdEncoding.EncodeToString([]byte("bob_secp256k1_worker_key_33bytes_")), // Worker key for BLS (secp256k1)
			Weight:          30,
			Status:          types.ParticipantStatus_ACTIVE,
		},
	}

	// Store participants in the keeper
	for _, participant := range participants {
		keeper.SetParticipant(ctx, *participant)
	}

	// Create test active participants
	activeParticipants := []*types.ActiveParticipant{
		{
			Index:        "cosmos1alice",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("alice_validator_ed25519_key")),
			Weight:       50,
		},
		{
			Index:        "cosmos1bob",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("bob_validator_ed25519_key__")),
			Weight:       30,
		},
	}

	// Create app module instance
	appModule := NewAppModule(
		cdc,
		keeper,
		nil, // accountKeeper not needed for this test
		nil, // bankKeeper not needed for this test
		nil, // groupMsgServer not needed for this test
	)

	// Test the initiateBLSKeyGeneration function
	epochID := uint64(123)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	// Verify that EpochBLSData was created
	epochBLSData, found := keeper.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.True(t, found, "EpochBLSData should be created")
	require.Equal(t, epochID, epochBLSData.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData.DkgPhase)
	require.Len(t, epochBLSData.Participants, 2)

	// Verify participants have correct data and were converted from WorkerPublicKey (NOT ValidatorKey)
	for i, blsParticipant := range epochBLSData.Participants {
		// Find corresponding original participant
		var originalParticipant *types.Participant
		for _, p := range participants {
			if p.Address == blsParticipant.Address {
				originalParticipant = p
				break
			}
		}
		require.NotNil(t, originalParticipant, "BLS participant should correspond to original participant")

		// CRITICAL TEST: Verify secp256k1 key was properly converted from WorkerPublicKey (not ValidatorKey)
		expectedKeyBytes, err := base64.StdEncoding.DecodeString(originalParticipant.WorkerPublicKey)
		require.NoError(t, err, "Original WorkerPublicKey should be valid base64")
		require.Equal(t, expectedKeyBytes, blsParticipant.Secp256K1PublicKey, "Secp256k1 key should match WorkerPublicKey")

		// CRITICAL TEST: Verify it's NOT using ValidatorKey (this is the key test for our WorkerPublicKey fix)
		validatorKeyBytes, err := base64.StdEncoding.DecodeString(originalParticipant.ValidatorKey)
		require.NoError(t, err, "ValidatorKey should also be valid base64")
		require.NotEqual(t, validatorKeyBytes, blsParticipant.Secp256K1PublicKey, "Should NOT use ValidatorKey - should use WorkerPublicKey")

		// Verify slot assignment
		require.True(t, blsParticipant.SlotEndIndex >= blsParticipant.SlotStartIndex, "Slot end should be >= slot start")

		// Verify slots are assigned (not default values)
		if i == 0 {
			require.Equal(t, uint32(0), blsParticipant.SlotStartIndex, "First participant should start at slot 0")
		} else {
			// Verify slots are contiguous
			prevParticipant := epochBLSData.Participants[i-1]
			require.Equal(t, prevParticipant.SlotEndIndex+1, blsParticipant.SlotStartIndex, "Slots should be contiguous")
		}
	}

	// Verify DealerParts array is properly initialized for future dealing phase
	require.Len(t, epochBLSData.DealerParts, len(participants), "DealerParts should have same length as participants")
	for i, dealerPart := range epochBLSData.DealerParts {
		require.NotNil(t, dealerPart, "DealerParts[%d] should not be nil", i)
		require.Empty(t, dealerPart.DealerAddress, "DealerParts[%d] should have empty address initially", i)
		require.Empty(t, dealerPart.Commitments, "DealerParts[%d] should have empty commitments initially", i)
		require.Empty(t, dealerPart.ParticipantShares, "DealerParts[%d] should have empty shares initially", i)
	}
}

// TestBLSKeyGenerationWithMissingParticipants tests error handling when participants are missing from store
func TestBLSKeyGenerationWithMissingParticipants(t *testing.T) {
	keeper, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Create codec for the test
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Create ActiveParticipants but DON'T store the corresponding Participants in keeper
	activeParticipants := []*types.ActiveParticipant{
		{
			Index:        "cosmos1missing",
			ValidatorKey: base64.StdEncoding.EncodeToString([]byte("missing_key")),
			Weight:       100,
		},
	}

	// Create app module instance
	appModule := NewAppModule(
		cdc,
		keeper,
		nil, // accountKeeper not needed for this test
		nil, // bankKeeper not needed for this test
		nil, // groupMsgServer not needed for this test
	)

	// Test the initiateBLSKeyGeneration function - should handle missing participants gracefully
	epochID := uint64(456)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	// Verify no BLS data was created due to missing participants
	_, found := keeper.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created when participants are missing from store")
}
