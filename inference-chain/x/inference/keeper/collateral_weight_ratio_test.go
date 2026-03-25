package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAdjustWeightsByCollateral_RejectsInvalidBaseWeightRatio(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForIntegration(t)

	// Generate a valid bech32 address for the participant
	participantAddr := sample.AccAddress()

	params := types.DefaultParams()
	// Set grace period to 0 so collateral logic is active
	params.CollateralParams.GracePeriodEndEpoch = 0
	require.NoError(t, k.SetParams(ctx, params))

	// Setup epoch so grace period check passes
	k.SetEffectiveEpochIndex(ctx, 10)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 10}))

	// Mock collateral keeper — participant has no collateral
	mocks.CollateralKeeper.EXPECT().
		GetCollateral(gomock.Any(), gomock.Any()).
		Return(sdk.Coin{}, false).
		AnyTimes()

	participants := []*types.ActiveParticipant{
		{Index: participantAddr, Weight: 100},
	}

	tests := []struct {
		name  string
		ratio float64
		valid bool
	}{
		{"ratio 0.2 (default, valid)", 0.2, true},
		{"ratio 0.0 (edge, valid)", 0.0, true},
		{"ratio 0.99 (edge, valid)", 0.99, true},
		{"ratio 1.0 (invalid, breaks invariant)", 1.0, false},
		{"ratio 1.5 (invalid, inflates weights)", 1.5, false},
		{"ratio -0.1 (negative, invalid)", -0.1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(tt.ratio)
			require.NoError(t, k.SetParams(ctx, params))

			err := k.AdjustWeightsByCollateral(ctx, participants)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "baseWeightRatio=%v should be rejected", tt.ratio)
			}
		})
	}
}
