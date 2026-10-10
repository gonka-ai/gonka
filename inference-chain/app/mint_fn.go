package app

import (
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
)

// GonkaMintFn is DefaultMintFn without the BondedRatio scan of all validators when
// InflationMin == InflationMax: the rate is clamped to that value whatever the ratio.
func GonkaMintFn() mintkeeper.MintFn {
	return func(ctx sdk.Context, k *mintkeeper.Keeper) error {
		minter, err := k.Minter.Get(ctx)
		if err != nil {
			return err
		}
		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}
		totalStakingSupply, err := k.StakingTokenSupply(ctx)
		if err != nil {
			return err
		}

		pinned := params.InflationMax.Equal(params.InflationMin)
		bondedRatio := math.LegacyZeroDec()
		if !pinned {
			if bondedRatio, err = k.BondedRatio(ctx); err != nil {
				return err
			}
		}

		prevInflation, prevProvisions := minter.Inflation, minter.AnnualProvisions
		minter.Inflation = minttypes.DefaultInflationCalculationFn(ctx, minter, params, bondedRatio)
		minter.AnnualProvisions = minter.NextAnnualProvisions(params, totalStakingSupply)
		// With inflation at zero the Minter never changes; rewriting it still writes the mint store every block.
		if !minter.Inflation.Equal(prevInflation) || !minter.AnnualProvisions.Equal(prevProvisions) {
			if err = k.Minter.Set(ctx, minter); err != nil {
				return err
			}
		}

		mintedCoin := minter.BlockProvision(params)
		mintedCoins := sdk.NewCoins(mintedCoin)
		if err = k.MintCoins(ctx, mintedCoins); err != nil {
			return err
		}
		if err = k.AddCollectedFees(ctx, mintedCoins); err != nil {
			return err
		}

		if mintedCoin.Amount.IsInt64() {
			defer telemetry.ModuleSetGauge(minttypes.ModuleName, float32(mintedCoin.Amount.Int64()), "minted_tokens")
		}

		attrs := make([]sdk.Attribute, 0, 4)
		if !pinned {
			attrs = append(attrs, sdk.NewAttribute(minttypes.AttributeKeyBondedRatio, bondedRatio.String()))
		}
		attrs = append(attrs,
			sdk.NewAttribute(minttypes.AttributeKeyInflation, minter.Inflation.String()),
			sdk.NewAttribute(minttypes.AttributeKeyAnnualProvisions, minter.AnnualProvisions.String()),
			sdk.NewAttribute(sdk.AttributeKeyAmount, mintedCoin.Amount.String()),
		)
		ctx.EventManager().EmitEvent(sdk.NewEvent(minttypes.EventTypeMint, attrs...))
		return nil
	}
}
