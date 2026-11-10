package tx_manager

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func TestMaxAffordableGas_CapsToSpendable(t *testing.T) {
	price := int64(1000)
	// 105M fee denom → 105k gas, not a dummy 10M bill.
	require.Equal(t, uint64(105_000), maxAffordableGas(math.NewInt(105_000_000), price))

	whale := math.NewInt(1_000_000).MulRaw(1_000_000_000) // 1e15 / 1000 > BatchGasLimit
	require.Equal(t, uint64(BatchGasLimit), maxAffordableGas(whale, price))

	require.Equal(t, uint64(0), maxAffordableGas(math.NewInt(500), price), "less than one gas")
	require.Equal(t, uint64(BatchGasLimit), maxAffordableGas(math.NewInt(1), 0), "price 0 uses BatchGasLimit")
}

func TestApplyFeegrantCap(t *testing.T) {
	bank := math.NewInt(1_000)
	require.True(t, applyFeegrantCap(bank, math.NewInt(-1)).Equal(bank), "unlimited")
	require.Equal(t, int64(50), applyFeegrantCap(bank, math.NewInt(50)).Int64())
	require.True(t, applyFeegrantCap(bank, math.NewInt(5_000)).Equal(bank))
}

func TestFeegrantRemaining(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	unlimited := feegrantRemaining(&feegrant.BasicAllowance{}, now)
	require.True(t, unlimited.Equal(math.NewInt(-1)))

	limited := feegrantRemaining(&feegrant.BasicAllowance{
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 50_000_000)),
	}, now)
	require.Equal(t, int64(50_000_000), limited.Int64())

	exp := now.Add(-time.Hour)
	expired := feegrantRemaining(&feegrant.BasicAllowance{Expiration: &exp}, now)
	require.True(t, expired.IsZero())

	periodLimit := sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 10_000_000))
	stalePeriod := feegrantRemaining(&feegrant.PeriodicAllowance{
		Basic:            feegrant.BasicAllowance{SpendLimit: sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 50_000_000))},
		Period:           time.Hour,
		PeriodSpendLimit: periodLimit,
		PeriodCanSpend:   sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 0)),
		PeriodReset:      now.Add(-time.Minute),
	}, now)
	require.Equal(t, int64(10_000_000), stalePeriod.Int64(), "after PeriodReset, remaining is PeriodSpendLimit not stale PeriodCanSpend")

	beforeReset := feegrantRemaining(&feegrant.PeriodicAllowance{
		Basic:            feegrant.BasicAllowance{SpendLimit: sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 50_000_000))},
		Period:           time.Hour,
		PeriodSpendLimit: periodLimit,
		PeriodCanSpend:   sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 1_000)),
		PeriodReset:      now.Add(time.Hour),
	}, now)
	require.Equal(t, int64(1_000), beforeReset.Int64())

	wrapped, err := feegrant.NewAllowedMsgAllowance(&feegrant.BasicAllowance{
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 7_000_000)),
	}, []string{"/inference.inference.MsgSubmitHardwareDiff"})
	require.NoError(t, err)
	require.Equal(t, int64(7_000_000), feegrantRemaining(wrapped, now).Int64())

	require.True(t, feegrantRemaining(&feegrant.AllowedMsgAllowance{}, now).IsZero(), "unpacked-empty wrapper is fail-closed")
}
