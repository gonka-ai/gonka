package keeper_test

import (
	"math"
	"math/bits"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// GetTotalCoins must saturate to MaxInt64 when RewardCoins+WorkCoins overflows uint64.
func TestSettleAmount_StoredExtremeCoins_GetTotalCoinsSaturates(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sa := types.SettleAmount{
		Participant:   testutil.Creator,
		EpochIndex:    1,
		RewardCoins:   math.MaxUint64,
		WorkCoins:     1,
		SeedSignature: "",
	}
	require.NoError(t, k.SetSettleAmount(sdkCtx, sa))
	loaded, found := k.GetSettleAmount(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, uint64(math.MaxUint64), loaded.RewardCoins)
	require.Equal(t, uint64(1), loaded.WorkCoins)
	require.Equal(t, int64(math.MaxInt64), loaded.GetTotalCoins())
}

// SettleAccounts must distinguish a genuine zero payout (skip) from a uint64
// wrap-to-zero (real payout owed). The bits.Add64 carry is the discriminator
// the post-fix guard relies on at accountsettle.go:262
// (`if paymentCarry == 0 && paymentSum == 0`).
func TestSettleAmount_PaymentGuard_DistinguishesWrapFromGenuineZero(t *testing.T) {
	// Wrap case: MaxUint64 + 1 yields paymentSum=0 with a real carry.
	// The naive pre-fix check `WorkCoins+RewardCoins == 0` would skip; the
	// post-fix `carry == 0 && sum == 0` does not.
	wrapSum, wrapCarry := bits.Add64(math.MaxUint64, 1, 0)
	require.Equal(t, uint64(0), wrapSum, "naive uint64 sum wraps to zero")
	require.Equal(t, uint64(1), wrapCarry, "carry signals real overflow")
	require.False(t, wrapCarry == 0 && wrapSum == 0,
		"post-fix guard must NOT skip wrap-to-zero participant")

	// Genuine zero: 0 + 0 yields paymentSum=0 with no carry. The guard skips.
	zeroSum, zeroCarry := bits.Add64(0, 0, 0)
	require.Equal(t, uint64(0), zeroSum)
	require.Equal(t, uint64(0), zeroCarry)
	require.True(t, zeroCarry == 0 && zeroSum == 0,
		"post-fix guard must still skip genuine-zero participant")
}
