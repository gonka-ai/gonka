package epochgroup

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"
)

func TestSelectRandomParticipant_Deterministic(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
		{Member: &group.Member{Address: "addr3", Weight: "300"}},
	}

	// Same inputs should always produce same output
	result1 := selectRandomParticipant(participants, 1, 100, "model1")
	result2 := selectRandomParticipant(participants, 1, 100, "model1")
	require.Equal(t, result1, result2, "Same inputs should produce deterministic results")
}

func TestSelectRandomParticipant_DifferentEpochsDifferentResults(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "100"}},
		{Member: &group.Member{Address: "addr3", Weight: "100"}},
		{Member: &group.Member{Address: "addr4", Weight: "100"}},
		{Member: &group.Member{Address: "addr5", Weight: "100"}},
	}

	// Different epoch indices should (likely) produce different results
	results := make(map[string]bool)
	for epoch := uint64(1); epoch <= 10; epoch++ {
		result := selectRandomParticipant(participants, epoch, 100, "model1")
		results[result] = true
	}

	// With 5 participants and 10 epochs, we should see variation
	require.Greater(t, len(results), 1, "Different epochs should produce different selections")
}

func TestSelectRandomParticipant_DifferentModelsDifferentResults(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "100"}},
		{Member: &group.Member{Address: "addr3", Weight: "100"}},
		{Member: &group.Member{Address: "addr4", Weight: "100"}},
		{Member: &group.Member{Address: "addr5", Weight: "100"}},
	}

	// Test multiple model pairs to verify different models produce different selections
	differentCount := 0
	for i := 0; i < 10; i++ {
		model1 := "modelA" + string(rune('0'+i))
		model2 := "modelB" + string(rune('0'+i))
		result1 := selectRandomParticipant(participants, 1, 100, model1)
		result2 := selectRandomParticipant(participants, 1, 100, model2)
		if result1 != result2 {
			differentCount++
		}
	}

	// With 5 participants, statistically most model pairs should produce different selections
	require.Greater(t, differentCount, 5, "Different models should produce different selections in most cases")
}

func TestSelectRandomParticipant_WeightedSelection(t *testing.T) {
	// Create participants with very skewed weights
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "heavy", Weight: "9999"}},
		{Member: &group.Member{Address: "light", Weight: "1"}},
	}

	heavyCount := 0
	lightCount := 0

	// Run selection many times with different epochs
	for epoch := uint64(1); epoch <= 1000; epoch++ {
		result := selectRandomParticipant(participants, epoch, 100, "model")
		if result == "heavy" {
			heavyCount++
		} else {
			lightCount++
		}
	}

	// Heavy participant should be selected much more often
	require.Greater(t, heavyCount, lightCount*10, "Weighted selection should favor heavier participants")
}

func TestSelectRandomParticipant_SingleParticipant(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "only_one", Weight: "100"}},
	}

	result := selectRandomParticipant(participants, 1, 100, "model")
	require.Equal(t, "only_one", result, "Single participant should always be selected")
}

func TestComputeSelectionSeed_Deterministic(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
	}

	seed1 := computeSelectionSeed(1, 100, "model1", participants)
	seed2 := computeSelectionSeed(1, 100, "model1", participants)

	require.Equal(t, seed1, seed2, "Same inputs should produce same seed")
}

func TestSelectRandomParticipant_ZeroWeights(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "0"}},
		{Member: &group.Member{Address: "addr2", Weight: "0"}},
	}

	// Should not panic with zero weights, returns first participant as fallback
	result := selectRandomParticipant(participants, 1, 100, "model")
	require.Equal(t, "addr1", result, "Zero weight fallback should return first participant")
}

func TestSelectRandomParticipant_InvalidWeights(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "invalid"}},
		{Member: &group.Member{Address: "addr2", Weight: "not-a-number"}},
	}

	// Should not panic with invalid weights, returns first participant as fallback
	result := selectRandomParticipant(participants, 1, 100, "model")
	require.Equal(t, "addr1", result, "Invalid weight fallback should return first participant")
}

func TestComputeSelectionSeed_OrderingDeterminism(t *testing.T) {
	// Same participants in different order should produce same seed
	participants1 := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
		{Member: &group.Member{Address: "addr3", Weight: "300"}},
	}

	participants2 := []*group.GroupMember{
		{Member: &group.Member{Address: "addr3", Weight: "300"}},
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
		{Member: &group.Member{Address: "addr2", Weight: "200"}},
	}

	seed1 := computeSelectionSeed(1, 100, "model1", participants1)
	seed2 := computeSelectionSeed(1, 100, "model1", participants2)

	require.Equal(t, seed1, seed2, "Same participants in different order should produce same seed for consensus")
}

func TestComputeSelectionSeed_DifferentInputsDifferentSeeds(t *testing.T) {
	participants := []*group.GroupMember{
		{Member: &group.Member{Address: "addr1", Weight: "100"}},
	}

	// Different epoch index
	seed1 := computeSelectionSeed(1, 100, "model1", participants)
	seed2 := computeSelectionSeed(2, 100, "model1", participants)
	require.NotEqual(t, seed1, seed2, "Different epoch should produce different seed")

	// Different block height
	seed3 := computeSelectionSeed(1, 101, "model1", participants)
	require.NotEqual(t, seed1, seed3, "Different height should produce different seed")

	// Different model
	seed4 := computeSelectionSeed(1, 100, "model2", participants)
	require.NotEqual(t, seed1, seed4, "Different model should produce different seed")

	// Different participants
	participants2 := []*group.GroupMember{
		{Member: &group.Member{Address: "addr2", Weight: "100"}},
	}
	seed5 := computeSelectionSeed(1, 100, "model1", participants2)
	require.NotEqual(t, seed1, seed5, "Different participants should produce different seed")
}
