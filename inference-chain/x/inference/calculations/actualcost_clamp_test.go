package calculations

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActualCostClampedToEscrow_StartThenFinish is the regression guard for the
// "uncapped ActualCost" finding. It runs the real ProcessStartInference ->
// ProcessFinishInference flow with MaxTokens=1 (tiny escrow) and
// CompletionTokenCount=MaxAllowedTokens (huge raw cost) and asserts that the
// persisted ActualCost is clamped to the escrow actually collected, so it can never
// drive a payout/refund/work-share larger than the escrow.
func TestActualCostClampedToEscrow_StartThenFinish(t *testing.T) {
	logger := &MockInferenceLogger{}
	const price = int64(PerTokenCost)

	seed := &types.Inference{PerTokenPrice: uint64(price)}
	startMsg := &types.MsgStartInference{
		InferenceId:      "fix-inf-1",
		Creator:          "ta",
		RequestedBy:      "dev",
		AssignedTo:       "executor",
		Model:            "m",
		MaxTokens:        1,
		PromptTokenCount: 10,
	}
	inf, startPayments, err := ProcessStartInference(
		seed, startMsg, BlockContext{BlockHeight: 1, BlockTimestamp: 1}, logger)
	require.NoError(t, err)
	require.Greater(t, startPayments.EscrowAmount, int64(0))
	inf.EscrowAmount = startPayments.EscrowAmount

	finishMsg := &types.MsgFinishInference{
		InferenceId:          "fix-inf-1",
		Creator:              "executor",
		ExecutedBy:           "executor",
		RequestedBy:          "dev",
		TransferredBy:        "ta",
		Model:                "m",
		PromptTokenCount:     10,
		CompletionTokenCount: types.MaxAllowedTokens,
	}
	finalInf, finishPayments, err := ProcessFinishInference(
		inf, finishMsg, BlockContext{BlockHeight: 2, BlockTimestamp: 2}, logger)
	require.NoError(t, err)

	// Core invariant: ActualCost never exceeds the escrow collected.
	assert.LessOrEqual(t, finalInf.ActualCost, finalInf.EscrowAmount,
		"ActualCost must be clamped to EscrowAmount on the start-then-finish path")
	// With completion >> MaxTokens the clamp pins ActualCost to exactly the escrow.
	assert.Equal(t, finalInf.EscrowAmount, finalInf.ActualCost)
	// Executor is paid no more than escrow, and there is no spurious refund.
	assert.Equal(t, finalInf.EscrowAmount, finishPayments.ExecutorPayment)
	assert.Equal(t, int64(0), finishPayments.EscrowAmount, "no refund when raw cost >= escrow")
	// CappedActualCost agrees with the persisted value.
	assert.Equal(t, finalInf.ActualCost, finalInf.CappedActualCost())
}

// TestActualCostNotInflated_UnderEscrow ensures the fix does not change correct
// behavior when the raw cost is below escrow: ActualCost stays exact and the unused
// escrow is refunded (negative payments.EscrowAmount), matching the documented
// "refund the difference" behavior.
func TestActualCostNotInflated_UnderEscrow(t *testing.T) {
	logger := &MockInferenceLogger{}
	const price = int64(PerTokenCost)

	seed := &types.Inference{PerTokenPrice: uint64(price)}
	startMsg := &types.MsgStartInference{
		InferenceId:      "fix-inf-2",
		Creator:          "ta",
		RequestedBy:      "dev",
		AssignedTo:       "executor",
		Model:            "m",
		MaxTokens:        1000,
		PromptTokenCount: 10,
	}
	inf, startPayments, err := ProcessStartInference(
		seed, startMsg, BlockContext{BlockHeight: 1, BlockTimestamp: 1}, logger)
	require.NoError(t, err)
	inf.EscrowAmount = startPayments.EscrowAmount

	finishMsg := &types.MsgFinishInference{
		InferenceId:          "fix-inf-2",
		Creator:              "executor",
		ExecutedBy:           "executor",
		RequestedBy:          "dev",
		TransferredBy:        "ta",
		Model:                "m",
		PromptTokenCount:     10,
		CompletionTokenCount: 100, // well under MaxTokens
	}
	finalInf, finishPayments, err := ProcessFinishInference(
		inf, finishMsg, BlockContext{BlockHeight: 2, BlockTimestamp: 2}, logger)
	require.NoError(t, err)

	expectedActual := (int64(finishMsg.CompletionTokenCount) + int64(finishMsg.PromptTokenCount)) * price
	assert.Equal(t, expectedActual, finalInf.ActualCost, "under-escrow cost is unchanged by the clamp")
	assert.Less(t, finalInf.ActualCost, finalInf.EscrowAmount)
	assert.Equal(t, expectedActual, finishPayments.ExecutorPayment)
	assert.Equal(t, expectedActual-finalInf.EscrowAmount, finishPayments.EscrowAmount, "unused escrow refunded")
}

// TestCappedActualCost_Helper unit-tests the invariant helper directly.
func TestCappedActualCost_Helper(t *testing.T) {
	cases := []struct {
		name       string
		actual     int64
		escrow     int64
		wantCapped int64
	}{
		{"under escrow", 50, 100, 50},
		{"equal", 100, 100, 100},
		{"over escrow is clamped", 1_000_010_000, 11_000, 11_000},
		{"zero escrow clamps to zero", 1_000_000, 0, 0},
		{"negative actual floored", -5, 100, 0},
		{"negative escrow floored", 100, -5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inf := &types.Inference{ActualCost: tc.actual, EscrowAmount: tc.escrow}
			assert.Equal(t, tc.wantCapped, inf.CappedActualCost())
		})
	}
}
