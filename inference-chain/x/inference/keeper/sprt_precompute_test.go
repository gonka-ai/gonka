package keeper_test

import (
	"testing"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCalculateLogLLR(t *testing.T) {
	// Test cases for calculateLogLLR
	tests := []struct {
		name   string
		p1     decimal.Decimal
		p0     decimal.Decimal
		isFail bool
		want   string
	}{
		{
			name:   "fail: p1=0.2, p0=0.1",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromFloat(0.1),
			isFail: true,
			want:   "0.69314718056", // ln(0.2/0.1) = ln(2)
		},
		{
			name:   "pass: p1=0.2, p0=0.1",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromFloat(0.1),
			isFail: false,
			want:   "-0.117783035656", // ln(0.8/0.9)
		},
		{
			name:   "fail: p1=0, returns zero",
			p1:     decimal.Zero,
			p0:     decimal.NewFromFloat(0.1),
			isFail: true,
			want:   "0",
		},
		{
			name:   "fail: p0=0, returns zero",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.Zero,
			isFail: true,
			want:   "0",
		},
		{
			name:   "pass: p1=1, returns zero",
			p1:     decimal.NewFromInt(1),
			p0:     decimal.NewFromFloat(0.1),
			isFail: false,
			want:   "0",
		},
		{
			name:   "pass: p0=1, returns zero",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromInt(1),
			isFail: false,
			want:   "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keeper.CalculateLogLLR(tt.p1, tt.p0, tt.isFail)
			want, _ := decimal.NewFromString(tt.want)
			if !got.Equal(want) {
				t.Errorf("CalculateLogLLR() = %v, want %v", got, want)
			}
		})
	}
}

func TestPrecomputeSPRTValues(t *testing.T) {
	k, ctx, _ := testkeeper.InferenceKeeperReturningMocks(t)

	// Set custom params
	params := types.DefaultParams()
	params.ValidationParams = &types.ValidationParams{
		BadParticipantInvalidationRate: types.DecimalFromFloat(0.3),
		FalsePositiveRate:              types.DecimalFromFloat(0.05),
		DowntimeBadPercentage:          types.DecimalFromFloat(0.4),
		DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
	}
	k.SetParams(ctx, params)

	err := k.PrecomputeSPRTValues(ctx)
	require.NoError(t, err)

	precomputed := k.GetPrecomputedSPRTValues(ctx)

	// Expected values (manually calculated)
	// ln(0.3/0.05) = 1.791759469228
	// ln(0.7/0.95) = -0.305381649551
	// ln(0.4/0.1) = 1.38629436112
	// ln(0.6/0.9) = -0.405465108108

	expectedInvalidationLogFail, _ := decimal.NewFromString("1.791759469228")
	expectedInvalidationLogPass, _ := decimal.NewFromString("-0.305381649551")
	expectedInactiveLogFail, _ := decimal.NewFromString("1.38629436112")
	expectedInactiveLogPass, _ := decimal.NewFromString("-0.405465108108")

	require.True(t, precomputed.InvalidationLogFail.ToDecimal().Equal(expectedInvalidationLogFail))
	require.True(t, precomputed.InvalidationLogPass.ToDecimal().Equal(expectedInvalidationLogPass))
	require.True(t, precomputed.InactiveLogFail.ToDecimal().Equal(expectedInactiveLogFail))
	require.True(t, precomputed.InactiveLogPass.ToDecimal().Equal(expectedInactiveLogPass))
}
