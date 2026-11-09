package app

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// enforceDeclaredGroupGas rejects a transaction whose declared gas is above a
// capped group's ceiling. During FinalizeBlock it also reserves that gas
// against the group's block budget. CheckTx, ReCheckTx, simulation, and
// ProcessProposal apply the per-transaction ceiling only.
//
// The reservation is written from ante. BaseApp commits the ante branch
// before the message handler runs, so a later out-of-gas failure still
// occupies the block budget.
func enforceDeclaredGroupGas(
	ctx sdk.Context,
	k *inferencemodulekeeper.Keeper,
	fp *inferencetypes.FeeParams,
	msgs []sdk.Msg,
	gas uint64,
) error {
	if fp == nil {
		for _, msg := range msgs {
			if inferencetypes.IsRequiredCappedFeeGroup(inferencetypes.FeeGroupOf(msg)) {
				return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "fee params are missing")
			}
		}
		return nil
	}

	groups, err := fp.CappedGroups(msgs)
	if err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
	}
	for _, group := range groups {
		if gas > group.MaxGas {
			return errorsmod.Wrapf(sdkerrors.ErrOutOfGas,
				"declared gas %d exceeds %s maximum %d",
				gas, group.Name, group.MaxGas)
		}
	}
	if ctx.ExecMode() != sdk.ExecModeFinalize || k == nil || len(groups) == 0 {
		return nil
	}

	type reservation struct {
		name string
		used uint64
	}
	pending := make([]reservation, 0, len(groups))
	for _, group := range groups {
		used, err := k.BlockFeeGroupGasUsed(ctx, group.Name)
		if err != nil {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
		}
		if inferencetypes.GroupGasExceeds(used, gas, group.MaxGas) {
			return errorsmod.Wrapf(sdkerrors.ErrOutOfGas,
				"block %s gas %d+%d exceeds %d",
				group.Name, used, gas, group.MaxGas)
		}
		pending = append(pending, reservation{name: group.Name, used: used + gas})
	}
	for _, item := range pending {
		if err := k.SetBlockFeeGroupGasUsed(ctx, item.name, item.used); err != nil {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
		}
	}
	return nil
}
