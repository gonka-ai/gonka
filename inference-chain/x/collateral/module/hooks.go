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
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	accAddr := sdk.AccAddress(valAddr)

	// When a validator is fully removed from the validator set, return all collateral
	// tokens and clean up associated module data to prevent orphaned entries.

	// 0. Return all active and unbonding collateral tokens to the validator
	if err := h.k.ReturnAllCollateral(sdkCtx, accAddr); err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorRemoved, failed to return collateral tokens",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	// 1. Remove any collateral held by this participant
	if err := h.k.RemoveCollateral(sdkCtx, accAddr); err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorRemoved, failed to remove collateral",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	// 2. Remove jailed status if set
	if err := h.k.RemoveJailed(sdkCtx, accAddr); err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorRemoved, failed to remove jailed status",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	// 3. Remove all unbonding collateral entries for this participant
	removedCount, err := h.k.RemoveAllUnbondingByParticipant(sdkCtx, accAddr)
	if err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorRemoved, failed to remove unbonding collateral",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	h.k.Logger().Info("Staking hook: AfterValidatorRemoved, cleaned up collateral data",
		"validator_address", valAddr.String(),
		"participant_address", accAddr.String(),
		"unbonding_entries_removed", removedCount,
		"height", sdkCtx.BlockHeight(),
	)

	return nil
}

func (h StakingHooks) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	accAddr := sdk.AccAddress(valAddr)

	// When a validator is bonded (e.g., un-jailed), we remove them from our jailed list.
	if err := h.k.RemoveJailed(sdkCtx, accAddr); err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorBonded, failed to remove jailed status",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	h.k.Logger().Debug("Staking hook: AfterValidatorBonded, removed jailed status",
		"validator_address", valAddr.String(),
		"height", sdkCtx.BlockHeight(),
	)
	return nil
}

func (h StakingHooks) AfterValidatorBeginUnbonding(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	accAddr := sdk.AccAddress(valAddr)

	// When a validator is jailed, we mark their corresponding participant as jailed in our module.
	if err := h.k.SetJailed(sdkCtx, accAddr); err != nil {
		h.k.Logger().Error("Staking hook: AfterValidatorBeginUnbonding, failed to set jailed status",
			"validator_address", valAddr.String(),
			"error", err,
		)
		return err
	}

	h.k.Logger().Debug("Staking hook: AfterValidatorBeginUnbonding, set jailed status",
		"validator_address", valAddr.String(),
		"height", sdkCtx.BlockHeight(),
	)
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
	// TODO(tokenomics-v2): Align staking-hook slashing with required-collateral semantics.
	// We currently pass requiredCollateral=0 here, which preserves legacy behavior
	// (slash from total actual balance) and may over-penalize over-collateralized participants.
	// requiredCollateral=zero means slash from total actual balance (legacy behavior for staking hooks)
	_, err := h.k.Slash(sdkCtx, accAddr, fraction, "", math.ZeroInt())
	return err
}
