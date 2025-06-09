package chainphase

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

var epochParams = types.EpochParams{
	EpochLength:           100,
	EpochShift:            10,
	EpochMultiplier:       1,
	PocStageDuration:      20,
	PocExchangeDuration:   1,
	PocValidationDelay:    1,
	PocValidationDuration: 10,
}

func TestCalculatePoCStartHeight(t *testing.T) {
	// Create a tracker without cosmos client for testing
	tracker := &ChainPhaseTracker{}

	// Test epoch params
	epochParams := &types.EpochParams{
		EpochLength:     100,
		EpochShift:      10,
		EpochMultiplier: 1,
	}

	testCases := []struct {
		name             string
		currentHeight    int64
		expectedPoCStart int64
	}{
		{
			name:             "At exact PoC start",
			currentHeight:    90, // This is block 90, which with shift 10 = 100, start of epoch 1
			expectedPoCStart: 90,
		},
		{
			name:             "In middle of PoC",
			currentHeight:    95, // In the middle of PoC stage
			expectedPoCStart: 90, // Should still calculate back to start of this epoch's PoC
		},
		{
			name:             "Near end of PoC",
			currentHeight:    99, // Near end of first PoC stage
			expectedPoCStart: 90, // Should still be start of this epoch's PoC
		},
		{
			name:             "In second epoch PoC",
			currentHeight:    190, // This is in epoch 2's PoC (shifted: 200)
			expectedPoCStart: 190, // Start of epoch 2's PoC
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tracker.calculatePoCStartHeight(tc.currentHeight, epochParams)
			assert.Equal(t, tc.expectedPoCStart, result,
				"For height %d, expected PoC start %d but got %d",
				tc.currentHeight, tc.expectedPoCStart, result)
		})
	}
}

func TestIsInPoCStage(t *testing.T) {
	tracker := &ChainPhaseTracker{}

	// Test epoch params where PoC lasts 20 blocks
	epochParams := &types.EpochParams{
		EpochLength:      100,
		EpochShift:       10,
		PocStageDuration: 20,
		EpochMultiplier:  1,
	}

	testCases := []struct {
		name     string
		height   int64
		expected bool
	}{
		{
			name:     "Before PoC",
			height:   89,
			expected: false,
		},
		{
			name:     "Start of PoC",
			height:   90, // Shifted: 100, position 0
			expected: true,
		},
		{
			name:     "Middle of PoC",
			height:   100, // Shifted: 110, position 10
			expected: true,
		},
		{
			name:     "End of PoC",
			height:   109, // Shifted: 119, position 19 (last block of PoC)
			expected: true,
		},
		{
			name:     "After PoC",
			height:   110, // Shifted: 120, position 20
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tracker.isInPoCStage(tc.height, epochParams)
			assert.Equal(t, tc.expected, result,
				"For height %d, expected isInPoCStage=%v but got %v",
				tc.height, tc.expected, result)
		})
	}
}

func Test(t *testing.T) {
	tracker := NewChainPhaseTracker()
	for i := 0; i < 10; i++ {
		// This is just a placeholder to ensure the package compiles
		// and can be used in tests.
	}
}
