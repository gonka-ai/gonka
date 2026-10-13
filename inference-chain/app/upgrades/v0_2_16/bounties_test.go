package v0_2_16

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

func TestBountyRewards(t *testing.T) {
	keepertest.InferenceKeeper(t)
	require.Len(t, bountyRewards, 16)
	_, err := sdk.AccAddressFromBech32(BountyCommunitySaleContractAddress)
	require.NoError(t, err)

	var total int64
	byRecipient := make(map[string]int64)
	for _, bounty := range bountyRewards {
		_, err := sdk.AccAddressFromBech32(bounty.Address)
		require.NoError(t, err, "invalid bounty recipient %s", bounty.Address)
		require.Positive(t, bounty.Amount)
		total += bounty.Amount
		byRecipient[bounty.Address] += bounty.Amount
	}

	// Spreadsheet totals in micro-USDT (1 USDT = 1,000,000 micro-USDT).
	require.Equal(t, int64(104_150_000_000), total)
	require.Equal(t, map[string]int64{
		"gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp": 59_600_000_000,
		"gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8": 500_000_000,
		"gonka1yhdhp4vwsvdsplv4acksntx0zxh8saueq6lj9m": 18_000_000_000,
		"gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe": 8_000_000_000,
		"gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm": 6_000_000_000,
		"gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57": 1_000_000_000,
		"gonka1s8zggm642e3kncy48c7vxwmeclt2wxyyd8qtdt": 8_050_000_000,
		"gonka15u0r3mf6t7zsfuslusnyt7hsjrq357yumpe8st": 3_000_000_000,
	}, byRecipient)
}

func TestDistributeBountyRewardsInsufficientBalance(t *testing.T) {
	for _, available := range []int64{0, 104_150_000_000 - 1} {
		k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
		communitySaleAddr, err := sdk.AccAddressFromBech32(BountyCommunitySaleContractAddress)
		require.NoError(t, err)
		mocks.BankViewKeeper.EXPECT().
			SpendableCoin(ctx, communitySaleAddr, BountyIbcUsdtDenom).
			Return(sdk.NewInt64Coin(BountyIbcUsdtDenom, available))

		// The test keeper has no WASM store, so any withdrawal would fail.
		require.NoError(t, distributeBountyRewards(ctx, k))
	}
}
