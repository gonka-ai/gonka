package calculations

import (
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name      string
		inference *types.Inference
		expected  int64
	}{
		{
			name:      "Empty inference",
			inference: &types.Inference{},
			expected:  0,
		},
		{
			name: "Legacy pricing - inference with tokens",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			expected: 30 * PerTokenCost,
		},
		{
			name: "Dynamic pricing - inference with custom per-token price",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        500,          // Custom dynamic price
				Model:                "test-model", // Complete inference object
			},
			expected: 30 * 500, // Should use dynamic price instead of legacy PerTokenCost
		},
		{
			name: "Dynamic pricing - zero price (grace period)",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        0,            // Free inference during grace period
				Model:                "test-model", // Complete inference object
			},
			expected: 0, // Should be free
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := CalculateCost(tt.inference)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateEscrow(t *testing.T) {
	tests := []struct {
		name         string
		inference    *types.Inference
		promptTokens uint64
		expected     int64
	}{
		{
			name:         "Empty inference",
			inference:    &types.Inference{},
			promptTokens: 0,
			expected:     0,
		},
		{
			name: "Legacy pricing - inference with MaxTokens",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			promptTokens: 50,
			expected:     150 * PerTokenCost,
		},
		{
			name: "Dynamic pricing - inference with custom per-token price",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: 750,          // Custom dynamic price
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 50,
			expected:     150 * 750, // Should use dynamic price
		},
		{
			name: "Dynamic pricing - zero price (grace period)",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: 0,            // Free inference during grace period
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 50,
			expected:     0, // Should be free
		},
		{
			name: "Dynamic pricing - high utilization scenario",
			inference: &types.Inference{
				MaxTokens:     200,
				PerTokenPrice: 1200,         // High price due to network congestion
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 100,
			expected:     300 * 1200, // Should use high dynamic price
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := CalculateEscrow(tt.inference, tt.promptTokens)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateCost_Overflow(t *testing.T) {
	inference := &types.Inference{
		PromptTokenCount:     ^uint64(0),
		CompletionTokenCount: 0,
		PerTokenPrice:        2,
	}
	_, err := CalculateCost(inference)
	assert.Error(t, err)
	assert.True(t, errorsmod.IsOf(err, types.ErrArithmeticOverflow))
}

func TestCalculateEscrow_TokenCountOverflow(t *testing.T) {
	inference := &types.Inference{
		MaxTokens:     ^uint64(0),
		PerTokenPrice: 1,
	}
	_, err := CalculateEscrow(inference, 1)
	assert.Error(t, err)
	assert.True(t, errorsmod.IsOf(err, types.ErrTokenCountOutOfRange))
}
