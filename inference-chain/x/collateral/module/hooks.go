package collateral

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/x/collateral/keeper"
)

// StakingHooks wrapper struct
type StakingHooks struct {
	k keeper.Keeper
}

var _ stakingtypes.StakingHooks = StakingHooks{}

// NewStakingHooks creates a new staking hooks
func NewStakingHooks(k keeper.Keeper) StakingHooks {
	return StakingHooks{k}
}

func (h StakingHooks) AfterValidatorCreated(ctx context.Context, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) BeforeValidatorModified(ctx context.Context, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// When a validator is bonded (e.g., un-jailed), we remove them from our jailed list.
	h.k.RemoveJailed(sdkCtx, sdk.AccAddress(valAddr))
	h.k.Logger().Debug("Staking hook: AfterValidatorBonded, removed jailed status", "validator_address", valAddr.String(), "height", sdkCtx.BlockHeight())
	return nil
}

func (h StakingHooks) AfterValidatorBeginUnbonding(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// When a validator is jailed, we mark their corresponding participant as jailed in our module.
	h.k.SetJailed(sdkCtx, sdk.AccAddress(valAddr))
	h.k.Logger().Debug("Staking hook: AfterValidatorBeginUnbonding, set jailed status", "validator_address", valAddr.String(), "height", sdkCtx.BlockHeight())
	return nil
}

func (h StakingHooks) BeforeDelegationCreated(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) BeforeDelegationSharesModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	return nil
}

func (h StakingHooks) AfterUnbondingInitiated(ctx context.Context, id uint64) error {
	return nil
}

func (h StakingHooks) BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, fraction math.LegacyDec) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	accAddr := sdk.AccAddress(valAddr)

	h.k.Logger().Debug("Staking hook: Slashing collateral for validator",
		"validator_address", valAddr.String(),
		"participant_address", accAddr.String(),
		"fraction", fraction.String(),
	)

	// Tendermint driven slashing is not limited per epoch, so pass in a blank reason
	_, err := h.k.Slash(sdkCtx, accAddr, fraction, "")
	return err
}
