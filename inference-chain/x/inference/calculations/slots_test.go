package calculations

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSlots_Determinism(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "1234567890"
	participant := "gonka100"
	nSlots := 64

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots1 := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
	slots2 := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

	require.Equal(t, slots1, slots2, "same inputs should produce same outputs")
}

func TestGetSlots_WeightDistribution(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "testhash"
	participant := "participant1"
	nSlots := 1000

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}

	// With weights 100:200:300, expected ratios are ~1:2:3
	// Allow 15% tolerance for statistical variance
	total := float64(nSlots)
	require.InDelta(t, 100.0/600.0, float64(counts["node1"])/total, 0.15)
	require.InDelta(t, 200.0/600.0, float64(counts["node2"])/total, 0.15)
	require.InDelta(t, 300.0/600.0, float64(counts["node3"])/total, 0.15)
}

func TestGetSlots_EmptyWeights(t *testing.T) {
	sortedEntries, totalWeight := PrepareSortedEntries(nil)
	require.Nil(t, sortedEntries)
	require.Equal(t, int64(0), totalWeight)

	sortedEntries, totalWeight = PrepareSortedEntries(map[string]int64{})
	require.Nil(t, sortedEntries)
	require.Equal(t, int64(0), totalWeight)
}

func TestGetSlots_ZeroSlots(t *testing.T) {
	weights := map[string]int64{"node1": 100}
	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted("hash", "participant", sortedEntries, totalWeight, 0)
	require.Nil(t, slots)
}

func TestGetSlots_ZeroTotalWeight(t *testing.T) {
	weights := map[string]int64{
		"node1": 0,
		"node2": 0,
	}
	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	require.Nil(t, sortedEntries)
	require.Equal(t, int64(0), totalWeight)
}

func TestGetSlot_SingleSlot(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "1234567890"
	participant := "gonka100"

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slot := GetSlotFromSorted(appHash, participant, sortedEntries, totalWeight, 0)
	require.NotEmpty(t, slot)
	require.Contains(t, []string{"node1", "node2", "node3"}, slot)
}

func TestGetSlot_MatchesGetSlots(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "testhash"
	participant := "participant1"
	nSlots := 10

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

	for i := 0; i < nSlots; i++ {
		singleSlot := GetSlotFromSorted(appHash, participant, sortedEntries, totalWeight, i)
		require.Equal(t, slots[i], singleSlot, "GetSlotFromSorted should match GetSlotsFromSorted at index %d", i)
	}
}

func TestGetSlots_DifferentParticipants(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "hash"
	nSlots := 64

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots1 := GetSlotsFromSorted(appHash, "participant1", sortedEntries, totalWeight, nSlots)
	slots2 := GetSlotsFromSorted(appHash, "participant2", sortedEntries, totalWeight, nSlots)

	require.NotEqual(t, slots1, slots2, "different participants should have different slots")
}

func TestGetSlots_SingleValidator(t *testing.T) {
	weights := map[string]int64{
		"only_node": 1000,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 10

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

	for _, slot := range slots {
		require.Equal(t, "only_node", slot)
	}
}

func TestGetSlots_NegativeWeightsSkipped(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": -50, // Should be skipped
		"node3": 200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 100

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
	require.NotNil(t, slots)
	require.Len(t, slots, nSlots)

	// Verify node2 never appears
	for _, slot := range slots {
		require.NotEqual(t, "node2", slot, "negative weight validator should not appear")
		require.Contains(t, []string{"node1", "node3"}, slot)
	}
}

func TestGetSlots_MixedWeights(t *testing.T) {
	weights := map[string]int64{
		"valid1":   100,
		"negative": -100,
		"zero":     0,
		"valid2":   200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 100

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
	require.NotNil(t, slots)

	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}

	// Only valid1 and valid2 should appear
	require.Equal(t, 0, counts["negative"])
	require.Equal(t, 0, counts["zero"])
	require.Greater(t, counts["valid1"], 0)
	require.Greater(t, counts["valid2"], 0)
}

func TestGetSlots_AllNegativeOrZeroWeights(t *testing.T) {
	weights := map[string]int64{
		"node1": -100,
		"node2": 0,
		"node3": -50,
	}
	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	require.Nil(t, sortedEntries)
	require.Equal(t, int64(0), totalWeight)
}

func TestGetSlots_MoreSlotsThanValidators(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 1000 // Many more slots than validators

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
	require.Len(t, slots, nSlots)

	// Algorithm is deterministic - assert exact counts
	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}
	require.Equal(t, 362, counts["node1"], "node1 count must be exact (deterministic)")
	require.Equal(t, 638, counts["node2"], "node2 count must be exact (deterministic)")
}

func TestGetSlots_LargeWeightDisparity(t *testing.T) {
	weights := map[string]int64{
		"whale": 9900,
		"small": 100,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 1000

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

	// Algorithm is deterministic - assert exact counts
	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}
	require.Equal(t, 988, counts["whale"], "whale count must be exact (deterministic)")
	require.Equal(t, 12, counts["small"], "small count must be exact (deterministic)")
}

func TestGetSlots_DifferentAppHash(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	participant := "participant"
	nSlots := 64

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots1 := GetSlotsFromSorted("appHash1", participant, sortedEntries, totalWeight, nSlots)
	slots2 := GetSlotsFromSorted("appHash2", participant, sortedEntries, totalWeight, nSlots)

	require.NotEqual(t, slots1, slots2, "different appHash should produce different slots")
}

func TestGetSlot_NegativeWeightsSkipped(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": -50,
		"node3": 200,
	}

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	// Run multiple slot indices to verify negative weight is never selected
	for i := 0; i < 100; i++ {
		slot := GetSlotFromSorted("hash", "participant", sortedEntries, totalWeight, i)
		require.NotEqual(t, "node2", slot, "negative weight validator should not be returned")
	}
}

func TestGetSlot_AllNegativeOrZeroWeights(t *testing.T) {
	weights := map[string]int64{
		"node1": -100,
		"node2": 0,
	}
	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	require.Nil(t, sortedEntries)
	require.Equal(t, int64(0), totalWeight)
	slot := GetSlotFromSorted("hash", "participant", nil, 0, 0)
	require.Empty(t, slot)
}

func TestGetSlots_LargeNSlots(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 10000

	sortedEntries, totalWeight := PrepareSortedEntries(weights)
	slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
	require.Len(t, slots, nSlots)

	// Verify distribution is still proportional
	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}
	total := float64(nSlots)
	require.InDelta(t, 100.0/600.0, float64(counts["node1"])/total, 0.05)
	require.InDelta(t, 200.0/600.0, float64(counts["node2"])/total, 0.05)
	require.InDelta(t, 300.0/600.0, float64(counts["node3"])/total, 0.05)
}

func TestGetSlots_OrderIndependentOfMapIteration(t *testing.T) {
	// Test that results are deterministic regardless of Go's map iteration order
	// by creating the same weights map multiple times and verifying same results
	appHash := "hash"
	participant := "participant"
	nSlots := 64

	var firstResult []string
	for iteration := 0; iteration < 10; iteration++ {
		// Create fresh map each time (Go may use different iteration order)
		weights := map[string]int64{
			"zebra":  100,
			"alpha":  200,
			"middle": 300,
			"beta":   150,
			"zulu":   250,
		}
		sortedEntries, totalWeight := PrepareSortedEntries(weights)
		slots := GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)

		if firstResult == nil {
			firstResult = slots
		} else {
			require.Equal(t, firstResult, slots, "results should be deterministic across map iterations")
		}
	}
}


func BenchmarkGetSlots_Current(b *testing.B) {
	weights := make(map[string]int64, 100)
	for i := 0; i < 100; i++ {
		weights[fmt.Sprintf("validator%d", i)] = int64(100 + i*10)
	}
	appHash := "benchmarkhash"
	nSlots := 128
	nParticipants := 1000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < nParticipants; j++ {
			participant := fmt.Sprintf("participant%d", j)
			sortedEntries, totalWeight := PrepareSortedEntries(weights)
			_ = GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
		}
	}
}

func BenchmarkGetSlots_Optimized(b *testing.B) {
	weights := make(map[string]int64, 100)
	for i := 0; i < 100; i++ {
		weights[fmt.Sprintf("validator%d", i)] = int64(100 + i*10)
	}
	appHash := "benchmarkhash"
	nSlots := 128
	nParticipants := 1000

	sortedEntries, totalWeight := PrepareSortedEntries(weights)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < nParticipants; j++ {
			participant := fmt.Sprintf("participant%d", j)
			_ = GetSlotsFromSorted(appHash, participant, sortedEntries, totalWeight, nSlots)
		}
	}
}
