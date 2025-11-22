package keeper

import (
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/types"
)

// Example usage:
// To run all tests:
//   go test -v -run TestCalculateOptimalCapWrapper
//
// To test with custom values, use RunCapTest directly:
//   go test -v -run TestCustomCap
//
// Or write your own test case using the RunCapTest helper function

// TestCalculateOptimalCapWrapper provides a simple wrapper for testing CalculateOptimalCap
// with a list of integers and max percentage
func TestCalculateOptimalCapWrapper(t *testing.T) {
	tests := []struct {
		name          string
		weights       []int64
		maxPercentage float64
	}{
		{
			name:          "Example 1: Basic capping",
			weights:       []int64{100, 200, 300, 400},
			maxPercentage: 0.30,
		},
		{
			name:          "Example 2: High concentration",
			weights:       []int64{50, 50, 900},
			maxPercentage: 0.30,
		},
		{
			name:          "Example 3: No capping needed",
			weights:       []int64{100, 100, 100, 100},
			maxPercentage: 0.30,
		},
		{
			name:          "Example 4: Small network (2 participants)",
			weights:       []int64{600, 400},
			maxPercentage: 0.50,
		},
		{
			name:          "Example 5: Extreme concentration",
			weights:       []int64{10, 10, 10, 970},
			maxPercentage: 0.30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RunCapTest(tt.weights, tt.maxPercentage)
			t.Log(result)
		})
	}
}

// RunCapTest is a standalone function you can call directly with weights and max percentage
// Returns formatted string with results
func RunCapTest(weights []int64, maxPercentage float64) string {
	// Create participants from weights
	participants := make([]*types.ActiveParticipant, len(weights))
	totalPower := int64(0)

	for i, weight := range weights {
		participants[i] = &types.ActiveParticipant{
			Index:  fmt.Sprintf("participant_%d", i),
			Weight: weight,
		}
		totalPower += weight
	}

	// Convert max percentage to types.Decimal
	maxPercentageDecimal := types.DecimalFromFloat(maxPercentage)

	// Call the function
	cappedParticipants, newTotalPower, wasCapped := CalculateOptimalCap(participants, totalPower, maxPercentageDecimal)

	// Format results
	result := fmt.Sprintf("\n=== Power Capping Test ===\n")
	result += fmt.Sprintf("Input Weights: %v\n", weights)
	result += fmt.Sprintf("Max Percentage: %.2f%%\n", maxPercentage*100)
	result += fmt.Sprintf("Total Power (before): %d\n", totalPower)
	result += fmt.Sprintf("Was Capped: %v\n\n", wasCapped)

	if wasCapped {
		result += "Results:\n"
		for i, p := range cappedParticipants {
			originalWeight := weights[i]
			cappedWeight := p.Weight
			percentOfTotal := float64(cappedWeight) / float64(newTotalPower) * 100

			result += fmt.Sprintf("  Participant %d: %d -> %d", i, originalWeight, cappedWeight)
			if originalWeight != cappedWeight {
				result += fmt.Sprintf(" (CAPPED, %.2f%% of total)", percentOfTotal)
			} else {
				result += fmt.Sprintf(" (unchanged, %.2f%% of total)", percentOfTotal)
			}
			result += "\n"
		}
		result += fmt.Sprintf("\nTotal Power (after): %d\n", newTotalPower)

		// Calculate max percentage after capping
		maxCappedWeight := int64(0)
		for _, p := range cappedParticipants {
			if p.Weight > maxCappedWeight {
				maxCappedWeight = p.Weight
			}
		}
		actualMaxPercentage := float64(maxCappedWeight) / float64(newTotalPower) * 100
		result += fmt.Sprintf("Actual Max Percentage: %.2f%%\n", actualMaxPercentage)
	} else {
		result += "No capping applied - all participants within limits\n"
		for i, weight := range weights {
			percentOfTotal := float64(weight) / float64(totalPower) * 100
			result += fmt.Sprintf("  Participant %d: %d (%.2f%% of total)\n", i, weight, percentOfTotal)
		}
	}

	return result
}

// TestCustomCap - Example test where you can easily plug in your own values
// Modify the weights and maxPercentage variables below to test different scenarios
func TestCustomCap(t *testing.T) {
	// ===== MODIFY THESE VALUES TO TEST YOUR SCENARIO =====
	weights := []int64{100, 200, 300} // Your participant weights
	maxPercentage := 0.30             // Max percentage (e.g., 0.30 = 30%)
	// ======================================================

	result := RunCapTest(weights, maxPercentage)
	t.Log(result)
}

// TestEdgeCases tests various edge cases and special scenarios
func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		weights       []int64
		maxPercentage float64
	}{
		{
			name:          "Single participant",
			weights:       []int64{1000},
			maxPercentage: 0.30,
		},
		{
			name:          "Two equal participants",
			weights:       []int64{500, 500},
			maxPercentage: 0.30,
		},
		{
			name:          "Very small weights",
			weights:       []int64{1, 1, 1, 97},
			maxPercentage: 0.30,
		},
		{
			name:          "Large weights",
			weights:       []int64{1000000, 2000000, 3000000, 4000000},
			maxPercentage: 0.30,
		},
		{
			name:          "Many participants with one dominant",
			weights:       []int64{10, 10, 10, 10, 10, 10, 10, 10, 10, 900},
			maxPercentage: 0.30,
		},
		{
			name:          "50% cap (2 participants)",
			weights:       []int64{800, 200},
			maxPercentage: 0.50,
		},
		{
			name:          "40% cap (3 participants)",
			weights:       []int64{700, 200, 100},
			maxPercentage: 0.40,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RunCapTest(tt.weights, tt.maxPercentage)
			t.Log(result)
		})
	}
}

// BenchmarkCalculateOptimalCap provides performance benchmarks
func BenchmarkCalculateOptimalCap(b *testing.B) {
	weights := []int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	participants := make([]*types.ActiveParticipant, len(weights))
	totalPower := int64(0)

	for i, weight := range weights {
		participants[i] = &types.ActiveParticipant{
			Index:  fmt.Sprintf("participant_%d", i),
			Weight: weight,
		}
		totalPower += weight
	}

	maxPercentageDecimal := types.DecimalFromFloat(0.30)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalculateOptimalCap(participants, totalPower, maxPercentageDecimal)
	}
}
