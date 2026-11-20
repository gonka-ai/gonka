package keeper

import (
	sdkerrors "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ChargeExtraGas consumes period-base + qty×rate gas when a MsgGasRule exists
// for msg. enabled_fee_groups is the coin-price switch only: an upgrade can
// ship with it empty, observe epoch tx gas, then enable "epoch" so those
// same txs start paying ngonka. Extra gas stays on so the StoreCommit sybil
// meter (and any nonzero HD/repeated_len rate) is already live in that window.
// firstOfPeriod is the caller's first-commit signal (StoreCommit: empty
// existingByModel). Period-base persistence is not stored separately.
//
// Quantity dispatch: repeated_len is read from the protobuf field on msg;
// stored_delta / stored_bytes use the handler-provided qty. Field paths on
// stored_* rules are Validate-only; they do not drive quantity.
func (k Keeper) ChargeExtraGas(ctx sdk.Context, signer sdk.AccAddress, msg sdk.Msg, qty uint64, firstOfPeriod bool) error {
	if msg == nil {
		return nil
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	fp := params.FeeParams
	if fp == nil {
		return nil
	}

	group, rule := fp.RuleForTypeURL(sdk.MsgTypeURL(msg))
	if rule == nil {
		return nil
	}

	if firstOfPeriod {
		if err := k.chargePeriodBase(ctx, signer, msg, group, rule); err != nil {
			return err
		}
	}

	qtyToCharge, err := extraGasQty(rule, msg, qty)
	if err != nil {
		return err
	}

	rate := extraGasRate(rule)
	if rate == 0 || qtyToCharge == 0 {
		return nil
	}
	countGas, overflow := checkedMul(qtyToCharge, rate)
	if overflow {
		return sdkerrors.Wrap(types.ErrIllegalState, "extra gas quantity * rate overflow")
	}
	if b := rule.GetStoredBytes(); b != nil {
		div, ok := types.StoredBytesUnitSize(b.Unit)
		if !ok || div == 0 {
			return sdkerrors.Wrap(types.ErrIllegalState, "stored_bytes.unit is invalid")
		}
		countGas = countGas / div
		if countGas == 0 {
			return nil
		}
	}
	ctx.GasMeter().ConsumeGas(storetypes.Gas(countGas), "fee_group_extra")
	return nil
}

func extraGasRate(rule *types.MsgGasRule) uint64 {
	if rule == nil {
		return 0
	}
	if d := rule.GetStoredDelta(); d != nil {
		return d.GasPerUnit
	}
	if b := rule.GetStoredBytes(); b != nil {
		return b.GasPerUnit
	}
	if r := rule.GetRepeatedLen(); r != nil {
		return r.GasPerUnit
	}
	return 0
}

// extraGasQty uses the configured function: repeated_len reads the protobuf
// field on msg and ignores handlerQty; stored_* keep the handler-provided
// canonical-state quantity (Count delta, inventory byte growth).
func extraGasQty(rule *types.MsgGasRule, msg sdk.Msg, handlerQty uint64) (uint64, error) {
	if rule == nil {
		return 0, nil
	}
	r := rule.GetRepeatedLen()
	if r == nil {
		return handlerQty, nil
	}
	n, ok := types.RepeatedFieldLen(msg, r.Field)
	if !ok {
		return 0, sdkerrors.Wrapf(types.ErrIllegalState, "repeated_len field %q missing or not repeated", r.Field)
	}
	return n, nil
}

// ChargeMessageRuleGas applies extra gas that can be computed from the
// message alone (repeated_len). stored_* rules no-op here because they
// need handler-provided canonical-state quantities.
func (k Keeper) ChargeMessageRuleGas(ctx sdk.Context, msg sdk.Msg) error {
	return k.ChargeExtraGas(ctx, nil, msg, 0, false)
}

func (k Keeper) chargePeriodBase(ctx sdk.Context, _ sdk.AccAddress, msg sdk.Msg, group *types.FeeGroup, rule *types.MsgGasRule) error {
	base := types.ResolvedPeriodBase(group, rule)
	if base == nil || base.Gas == 0 {
		return nil
	}
	// StoreCommit first-of-stage is decided by existingByModel (firstOfPeriod).
	// Generic epoch/block buckets are not implemented; Validate rejects any
	// other nonzero period base so this cannot silently under-charge.
	if _, ok := msg.(*types.MsgPoCV2StoreCommit); !ok {
		return nil
	}
	ctx.GasMeter().ConsumeGas(storetypes.Gas(base.Gas), "fee_group_period_base")
	return nil
}

// ChargeStoreCommitGas consumes the StoreCommit period base, the positive
// Count delta, and one static gas for each new model this transaction
// inserts. The quantities come from chain state after semantic checks. A
// model that already has a row for this stage is not new. The gas meter is
// what the ante fee decorator bills; this function does not debit a
// participant account. A feegrant, including a third-party grant, therefore
// sponsors the charge.
func (k Keeper) ChargeStoreCommitGas(
	ctx sdk.Context,
	signer sdk.AccAddress,
	msg *types.MsgPoCV2StoreCommit,
	totalCountDelta uint64,
	newCommitKeyCount uint64,
	firstOfPeriod bool,
) error {
	if msg == nil {
		return nil
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	fp := params.FeeParams
	if fp == nil {
		return nil
	}
	group, rules := fp.RulesForTypeURL(sdk.MsgTypeURL(msg))
	if len(rules) == 0 {
		return nil
	}
	var deltaRule, staticRule, baseRule *types.MsgGasRule
	for _, rule := range rules {
		if rule.GetStoredDelta() != nil {
			deltaRule = rule
		}
		if rule.GetStatic() != nil {
			staticRule = rule
		}
		if rule.Base != nil {
			baseRule = rule
		}
	}
	if baseRule == nil {
		baseRule = deltaRule
	}
	if firstOfPeriod && baseRule != nil {
		if err := k.chargePeriodBase(ctx, signer, msg, group, baseRule); err != nil {
			return err
		}
	}
	var countGas uint64
	if deltaRule != nil {
		var overflow bool
		countGas, overflow = checkedMul(totalCountDelta, deltaRule.GetStoredDelta().GasPerUnit)
		if overflow {
			return sdkerrors.Wrap(types.ErrIllegalState, "store commit count gas overflow")
		}
	}
	var newKeyGas uint64
	if staticRule != nil {
		var overflow bool
		newKeyGas, overflow = checkedMul(newCommitKeyCount, staticRule.GetStatic().Gas)
		if overflow {
			return sdkerrors.Wrap(types.ErrIllegalState, "store commit new-key gas overflow")
		}
	}
	extra, overflow := checkedAdd(countGas, newKeyGas)
	if overflow {
		return sdkerrors.Wrap(types.ErrIllegalState, "store commit extra gas overflow")
	}
	if extra == 0 {
		return nil
	}
	ctx.GasMeter().ConsumeGas(storetypes.Gas(extra), "fee_group_extra")
	return nil
}

// ChargeStaticGas consumes the static fee-rule gas once. The caller decides
// whether this message occurrence is charged. SubmitNewParticipant calls it
// only when the participant does not already exist.
func (k Keeper) ChargeStaticGas(ctx sdk.Context, msg sdk.Msg) error {
	if msg == nil {
		return nil
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.FeeParams == nil {
		return nil
	}
	_, rule := params.FeeParams.RuleForTypeURL(sdk.MsgTypeURL(msg))
	static := (*types.StaticParams)(nil)
	if rule != nil {
		static = rule.GetStatic()
	}
	if static == nil || static.Gas == 0 {
		return nil
	}
	ctx.GasMeter().ConsumeGas(storetypes.Gas(static.Gas), "fee_group_extra")
	return nil
}
