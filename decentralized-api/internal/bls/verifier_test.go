package bls

import (
	"strings"
	"testing"

	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/assert"
)

// createMockCosmosClient creates a minimal mock cosmos client for testing
func createMockCosmosClient() cosmosclient.InferenceCosmosClient {
	return cosmosclient.InferenceCosmosClient{
		Address: "cosmos1testaddress",
	}
}

func TestNewBlsManager(t *testing.T) {
	// Test with mock client for basic construction
	blsManager := NewBlsManager(createMockCosmosClient())

	assert.NotNil(t, blsManager)
	assert.NotNil(t, blsManager.cache)                       // Should have cache initialized
	assert.Nil(t, blsManager.GetCurrentVerificationResult()) // Should be nil until verification starts
}

func TestVerificationResult(t *testing.T) {
	// Test VerificationResult structure
	result := &VerificationResult{
		EpochID:       1,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1}, // Slots 0 and 1
	}

	// Add some test aggregated shares
	share1 := fr.Element{}
	share1.SetUint64(123)
	share2 := fr.Element{}
	share2.SetUint64(456)

	result.AggregatedShares = []fr.Element{share1, share2}

	assert.Equal(t, uint64(1), result.EpochID)
	assert.Equal(t, types.DKGPhase_DKG_PHASE_VERIFYING, result.DkgPhase)
	assert.True(t, result.IsParticipant)
	assert.Equal(t, [2]uint32{0, 1}, result.SlotRange)
	assert.Len(t, result.AggregatedShares, 2)
	assert.Equal(t, share1.String(), result.AggregatedShares[0].String())
	assert.Equal(t, share2.String(), result.AggregatedShares[1].String())
}

func TestCountTrueValues(t *testing.T) {
	tests := []struct {
		name     string
		input    []bool
		expected int
	}{
		{"empty slice", []bool{}, 0},
		{"all false", []bool{false, false, false}, 0},
		{"all true", []bool{true, true, true}, 3},
		{"mixed", []bool{true, false, true, false, true}, 3},
		{"single true", []bool{true}, 1},
		{"single false", []bool{false}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := countTrueValues(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVerificationCache(t *testing.T) {
	cache := NewVerificationCache()

	// Test empty cache
	assert.Nil(t, cache.Get(1))
	assert.Nil(t, cache.GetCurrent())
	assert.Empty(t, cache.GetCachedEpochs())

	// Create test verification results
	result1 := &VerificationResult{
		EpochID:       1,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}

	result2 := &VerificationResult{
		EpochID:       2,
		DkgPhase:      types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant: true,
		SlotRange:     [2]uint32{2, 3},
	}

	result3 := &VerificationResult{
		EpochID:       3,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{4, 5},
	}

	// Store first result
	cache.Store(result1)
	assert.Equal(t, result1, cache.Get(1))
	assert.Equal(t, result1, cache.GetCurrent())
	assert.Len(t, cache.GetCachedEpochs(), 1)

	// Store second result
	cache.Store(result2)
	assert.Equal(t, result1, cache.Get(1))
	assert.Equal(t, result2, cache.Get(2))
	assert.Equal(t, result2, cache.GetCurrent()) // Should return highest epoch
	assert.Len(t, cache.GetCachedEpochs(), 2)

	// Store third result - should remove epoch 1
	cache.Store(result3)
	assert.Nil(t, cache.Get(1))                  // Should be removed
	assert.Equal(t, result2, cache.Get(2))       // Should still exist
	assert.Equal(t, result3, cache.Get(3))       // Should exist
	assert.Equal(t, result3, cache.GetCurrent()) // Should return highest epoch
	assert.Len(t, cache.GetCachedEpochs(), 2)

	// Verify cached epochs
	epochs := cache.GetCachedEpochs()
	assert.Contains(t, epochs, uint64(2))
	assert.Contains(t, epochs, uint64(3))
	assert.NotContains(t, epochs, uint64(1))
}

func TestVerificationCacheEdgeCases(t *testing.T) {
	cache := NewVerificationCache()

	// Test storing nil result
	cache.Store(nil)
	assert.Empty(t, cache.GetCachedEpochs())

	// Test epoch 0
	result0 := &VerificationResult{
		EpochID:  0,
		DkgPhase: types.DKGPhase_DKG_PHASE_DEALING,
	}
	cache.Store(result0)
	assert.Equal(t, result0, cache.Get(0))

	// Test epoch 1
	result1 := &VerificationResult{
		EpochID:  1,
		DkgPhase: types.DKGPhase_DKG_PHASE_VERIFYING,
	}
	cache.Store(result1)
	assert.Equal(t, result0, cache.Get(0)) // Should still exist
	assert.Equal(t, result1, cache.Get(1)) // Should exist
	assert.Len(t, cache.GetCachedEpochs(), 2)

	// Both should still exist (no cleanup until epoch >= 2)
	assert.NotNil(t, cache.Get(0))
	assert.NotNil(t, cache.Get(1))
}

func TestVerifierCacheIntegration(t *testing.T) {
	blsManager := NewBlsManager(createMockCosmosClient())

	// Test initial state
	assert.NotNil(t, blsManager.cache)
	assert.Nil(t, blsManager.GetCurrentVerificationResult())
	assert.Empty(t, blsManager.GetCachedEpochs())

	// Manually create and store verification results
	result1 := &VerificationResult{
		EpochID:       1,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}

	result2 := &VerificationResult{
		EpochID:       2,
		DkgPhase:      types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant: true,
		SlotRange:     [2]uint32{2, 3},
	}

	blsManager.cache.Store(result1)
	blsManager.cache.Store(result2)

	// Test convenience methods
	assert.Equal(t, result1, blsManager.GetVerificationResult(1))
	assert.Equal(t, result2, blsManager.GetVerificationResult(2))
	assert.Equal(t, result2, blsManager.GetCurrentVerificationResult())

	epochs := blsManager.GetCachedEpochs()
	assert.Len(t, epochs, 2)
	assert.Contains(t, epochs, uint64(1))
	assert.Contains(t, epochs, uint64(2))
}

func TestStoreVerificationResult(t *testing.T) {
	blsManager := NewBlsManager(createMockCosmosClient())

	// Test storing a valid result
	result1 := &VerificationResult{
		EpochID:       1,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}

	blsManager.storeVerificationResult(result1)

	// Verify it was stored
	stored := blsManager.GetVerificationResult(1)
	assert.NotNil(t, stored)
	assert.Equal(t, uint64(1), stored.EpochID)
	assert.Equal(t, types.DKGPhase_DKG_PHASE_VERIFYING, stored.DkgPhase)
	assert.True(t, stored.IsParticipant)
	assert.Equal(t, [2]uint32{0, 1}, stored.SlotRange)

	// Test storing nil result - should not panic
	blsManager.storeVerificationResult(nil)

	// Cache should still only have one result
	assert.Len(t, blsManager.GetCachedEpochs(), 1)

	// Test storing another result
	result2 := &VerificationResult{
		EpochID:       2,
		DkgPhase:      types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant: false,
		SlotRange:     [2]uint32{2, 3},
	}

	blsManager.storeVerificationResult(result2)

	// Should have both results now
	assert.Len(t, blsManager.GetCachedEpochs(), 2)
	assert.NotNil(t, blsManager.GetVerificationResult(1))
	assert.NotNil(t, blsManager.GetVerificationResult(2))

	// Current should be the latest (highest epoch ID)
	current := blsManager.GetCurrentVerificationResult()
	assert.Equal(t, uint64(2), current.EpochID)
	assert.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, current.DkgPhase)
}

func TestProcessVerifyingPhaseStartedWithExistingResult(t *testing.T) {
	blsManager := NewBlsManager(createMockCosmosClient())

	// Mock event for epoch 5 with complete and proper mock data
	completeEpochData := `{
		"epoch_id": "5",
		"i_total_slots": "100",
		"t_slots_degree": "50",
		"dkg_phase": "DKG_PHASE_VERIFYING",
		"dealing_phase_deadline_block": "950",
		"verifying_phase_deadline_block": "1000",
		"participants": [
			{
				"address": "cosmos1test",
				"percentage_weight": "0.5",
				"secp256k1_public_key": "AqAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"slot_start_index": "0",
				"slot_end_index": "49"
			}
		],
		"dealer_parts": [],
		"verification_submissions": [],
		"group_public_key": "",
		"valid_dealers": []
	}`

	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference.bls.EventVerifyingPhaseStarted.epoch_id":                       {"5"},
				"inference.bls.EventVerifyingPhaseStarted.verifying_phase_deadline_block": {"1000"},
				"inference.bls.EventVerifyingPhaseStarted.epoch_data":                     {completeEpochData},
			},
		},
	}

	// Manually store a verification result for epoch 5 with VERIFYING phase
	result := &VerificationResult{
		EpochID:       5,
		DkgPhase:      types.DKGPhase_DKG_PHASE_VERIFYING,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}
	blsManager.cache.Store(result)

	// Test: Call should skip verification because we already have a VERIFYING result
	err := blsManager.ProcessVerifyingPhaseStarted(event)
	assert.NoError(t, err) // Should succeed without trying to verify again

	// Update the result to COMPLETED phase
	resultCompleted := &VerificationResult{
		EpochID:       5,
		DkgPhase:      types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}
	blsManager.cache.Store(resultCompleted)

	// Test: Call should also skip verification because we have a COMPLETED result
	err = blsManager.ProcessVerifyingPhaseStarted(event)
	assert.NoError(t, err) // Should succeed without trying to verify again
}

func TestProcessGroupPublicKeyGeneratedWithExistingResult(t *testing.T) {
	blsManager := NewBlsManager(createMockCosmosClient())

	// Mock event for epoch 10
	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference.bls.EventGroupPublicKeyGenerated.epoch_id":         {"10"},
				"inference.bls.EventGroupPublicKeyGenerated.group_public_key": {"0123456789abcdef" + strings.Repeat("00", 88)}, // 96 bytes hex
			},
		},
	}

	// Store a COMPLETED result and verify it skips processing
	completedResult := &VerificationResult{
		EpochID:       10,
		DkgPhase:      types.DKGPhase_DKG_PHASE_COMPLETED,
		IsParticipant: true,
		SlotRange:     [2]uint32{0, 1},
	}
	blsManager.cache.Store(completedResult)

	// This should skip processing and return successfully
	err := blsManager.ProcessGroupPublicKeyGeneratedToVerify(event)
	assert.NoError(t, err) // Should succeed without trying to query chain

	// Verify the result is still COMPLETED and unchanged
	stored := blsManager.GetVerificationResult(10)
	assert.NotNil(t, stored)
	assert.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, stored.DkgPhase)
}

func TestProcessGroupPublicKeyGeneratedEventParsing(t *testing.T) {
	blsManager := NewBlsManager(createMockCosmosClient())

	// Test invalid event - missing epoch_id
	invalidEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference.bls.EventGroupPublicKeyGenerated.group_public_key": {"test"},
			},
		},
	}

	err := blsManager.ProcessGroupPublicKeyGeneratedToVerify(invalidEvent)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "epoch_id not found")

	// Test invalid epoch_id format
	invalidEpochEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference.bls.EventGroupPublicKeyGenerated.epoch_id": {"invalid"},
			},
		},
	}

	err = blsManager.ProcessGroupPublicKeyGeneratedToVerify(invalidEpochEvent)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse epoch_id")
}
