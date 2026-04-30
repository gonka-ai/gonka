package app

import (
	"fmt"
	"time"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/server"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// InitInferenceAppForTestnet rewrites staking and slashing state so that the
// local validator becomes the sole bonded validator and schedules the
// upgrade requested via --trigger-testnet-upgrade.
//
// Cosmos SDK's `in-place-testnet` only patches CometBFT consensus state and
// declares the upgrade flag; both the staking-side rewrite and the upgrade
// scheduling have to be done by the chain app. Pattern follows Osmosis'
// InitOsmosisAppForTestnet.
func InitInferenceAppForTestnet(app *App, appOpts servertypes.AppOptions) error {
	valAddr, _ := appOpts.Get(server.KeyNewValAddr).(cmtbytes.HexBytes)
	cmtPubKey, _ := appOpts.Get(server.KeyUserPubKey).(cmtcrypto.PubKey)
	operatorAddr, _ := appOpts.Get(server.KeyNewOpAddr).(string)
	upgradeName, _ := appOpts.Get(server.KeyTriggerTestnetUpgrade).(string)

	pubKey, err := cryptocodec.FromCmtPubKeyInterface(cmtPubKey)
	if err != nil {
		return fmt.Errorf("convert pubkey: %w", err)
	}
	opAddr, err := sdk.ValAddressFromBech32(operatorAddr)
	if err != nil {
		return fmt.Errorf("parse operator addr: %w", err)
	}
	consAddr := sdk.ConsAddress(valAddr.Bytes())

	ctx := app.BaseApp.NewUncachedContext(true, cmtproto.Header{})

	if err := replaceStakingValidatorSet(ctx, app, pubKey, opAddr); err != nil {
		return err
	}

	signingInfo := slashingtypes.NewValidatorSigningInfo(
		consAddr, ctx.BlockHeight(), 0, time.Unix(0, 0).UTC(), false, 0,
	)
	if err := app.SlashingKeeper.SetValidatorSigningInfo(ctx, consAddr, signingInfo); err != nil {
		return fmt.Errorf("set signing info: %w", err)
	}

	if upgradeName != "" {
		plan := upgradetypes.Plan{Name: upgradeName, Height: ctx.BlockHeight() + 1}
		if err := app.UpgradeKeeper.ScheduleUpgrade(ctx, plan); err != nil {
			return fmt.Errorf("schedule upgrade %q: %w", upgradeName, err)
		}
		app.Logger().Info("testnet fork: scheduled upgrade", "name", upgradeName, "height", plan.Height)
	}

	app.Logger().Info("testnet fork: replaced validator set", "operator", operatorAddr, "cons_addr", consAddr.String())
	return nil
}

func replaceStakingValidatorSet(ctx sdk.Context, app *App, newPubKey cryptotypes.PubKey, newOpAddr sdk.ValAddress) error {
	existing, err := app.StakingKeeper.GetAllValidators(ctx)
	if err != nil {
		return fmt.Errorf("get validators: %w", err)
	}
	for _, v := range existing {
		valAddr, err := sdk.ValAddressFromBech32(v.OperatorAddress)
		if err != nil {
			return err
		}
		// Delete the power index under v's CURRENT tokens; otherwise the
		// recomputed key after we zero tokens won't match the stored one
		// and the old entry leaks.
		if err := app.StakingKeeper.DeleteValidatorByPowerIndex(ctx, v); err != nil {
			return fmt.Errorf("delete power index for %s: %w", v.OperatorAddress, err)
		}
		_ = app.StakingKeeper.DeleteLastValidatorPower(ctx, valAddr)
		v.Status = stakingtypes.Unbonded
		v.Tokens = math.ZeroInt()
		v.DelegatorShares = math.LegacyZeroDec()
		if err := app.StakingKeeper.SetValidator(ctx, v); err != nil {
			return err
		}
		if err := app.StakingKeeper.RemoveValidator(ctx, valAddr); err != nil {
			return fmt.Errorf("remove %s: %w", v.OperatorAddress, err)
		}
	}

	newVal, err := stakingtypes.NewValidator(newOpAddr.String(), newPubKey, stakingtypes.Description{Moniker: "testnet-fork"})
	if err != nil {
		return fmt.Errorf("new validator: %w", err)
	}
	tokens := math.NewInt(900_000_000_000_000)
	newVal.Status = stakingtypes.Bonded
	newVal.Tokens = tokens
	newVal.DelegatorShares = math.LegacyNewDecFromInt(tokens)
	newVal.MinSelfDelegation = math.OneInt()

	if err := app.StakingKeeper.SetValidator(ctx, newVal); err != nil {
		return err
	}
	if err := app.StakingKeeper.SetValidatorByConsAddr(ctx, newVal); err != nil {
		return err
	}
	if err := app.StakingKeeper.SetValidatorByPowerIndex(ctx, newVal); err != nil {
		return err
	}
	power := tokens.Quo(sdk.DefaultPowerReduction).Int64()
	return app.StakingKeeper.SetLastValidatorPower(ctx, newOpAddr, power)
}
