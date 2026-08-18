package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type PocPeriodValidationDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
}

func NewPocPeriodValidationDecorator(ik *inferencemodulekeeper.Keeper) PocPeriodValidationDecorator {
	return PocPeriodValidationDecorator{
		inferenceKeeper: ik,
	}
}

func (ppd PocPeriodValidationDecorator) checkPocMessageTooLate(ctx sdk.Context, msg sdk.Msg) error {
	if ppd.inferenceKeeper == nil {
		return nil
	}

	switch m := msg.(type) {
	case *inferencetypes.MsgSubmitPocBatch:
		params, err := ppd.inferenceKeeper.GetParams(ctx)
		if err != nil {
			return err
		}
		if !params.PocParams.PocV2Enabled {
			if err := ppd.inferenceKeeper.CheckPoCMessageTooLate(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PoCWindowBatch); err != nil {
				ppd.inferenceKeeper.LogDebug(
					"AnteHandle: PocPeriodValidation - rejecting MsgSubmitPocBatch as too late",
					inferencetypes.PoC,
					"msg_type_url", sdk.MsgTypeURL(msg),
					"pocStageStartBlockHeight", m.PocStageStartBlockHeight,
					"currentBlockHeight", ctx.BlockHeight(),
					"error", err,
				)
				return err
			}
			return nil
		}
		ppd.inferenceKeeper.LogDebug(
			"AnteHandle: PocPeriodValidation - rejecting deprecated MsgSubmitPocBatch (V2 mode)",
			inferencetypes.PoC,
			"msg_type_url", sdk.MsgTypeURL(msg),
		)
		return inferencetypes.ErrDeprecated

	case *inferencetypes.MsgSubmitPocValidationsV2:
		params, err := ppd.inferenceKeeper.GetParams(ctx)
		if err != nil {
			return err
		}
		if !params.PocParams.PocV2Enabled {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgSubmitPocValidationsV2 (V1 mode)",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
			)
			return inferencetypes.ErrNotSupported
		}
		if err := ppd.inferenceKeeper.CheckPoCMessageTooLate(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PoCWindowValidation); err != nil {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgSubmitPocValidationsV2 as too late",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
				"pocStageStartBlockHeight", m.PocStageStartBlockHeight,
				"currentBlockHeight", ctx.BlockHeight(),
				"error", err,
			)
			return err
		}

	case *inferencetypes.MsgPoCV2StoreCommit:
		params, err := ppd.inferenceKeeper.GetParams(ctx)
		if err != nil {
			return err
		}
		if !params.PocParams.PocV2Enabled {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgPoCV2StoreCommit (V1 mode)",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
			)
			return inferencetypes.ErrNotSupported
		}
		if err := ppd.inferenceKeeper.CheckPoCMessageTooLate(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PoCWindowBatch); err != nil {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgPoCV2StoreCommit",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
				"pocStageStartBlockHeight", m.PocStageStartBlockHeight,
				"currentBlockHeight", ctx.BlockHeight(),
				"error", err,
			)
			return err
		}

	case *inferencetypes.MsgMLNodeWeightDistribution:
		params, err := ppd.inferenceKeeper.GetParams(ctx)
		if err != nil {
			return err
		}
		if !params.PocParams.PocV2Enabled {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgMLNodeWeightDistribution (V1 mode)",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
			)
			return inferencetypes.ErrNotSupported
		}
		if err := ppd.inferenceKeeper.CheckPoCMessageTooLate(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PoCWindowValidation); err != nil {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgMLNodeWeightDistribution",
				inferencetypes.PoC,
				"msg_type_url", sdk.MsgTypeURL(msg),
				"pocStageStartBlockHeight", m.PocStageStartBlockHeight,
				"currentBlockHeight", ctx.BlockHeight(),
				"error", err,
			)
			return err
		}
	}

	return nil
}

// checkMessage rejects PoC messages submitted outside their stage window,
// descending through authz MsgExec wrappers to find them.
//
// depth is the number of MsgExec levels already unwrapped; callers start at 0.
// Unwrapping is bounded by maxMsgExecNestingDepth for the same reason as in
// NetworkDutySignerDecorator: this runs CheckTx-only, where ante work is not
// gas-metered, so an unbounded walk would be a free DoS surface. Beyond the
// limit the transaction is rejected rather than passed through — at that depth
// the tree cannot be inspected, so it cannot be shown not to carry a late PoC
// message.
func (ppd PocPeriodValidationDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg, depth int) error {
	switch m := msg.(type) {
	case *inferencetypes.MsgSubmitPocBatch,
		*inferencetypes.MsgSubmitPocValidationsV2,
		*inferencetypes.MsgPoCV2StoreCommit, *inferencetypes.MsgMLNodeWeightDistribution:
		return ppd.checkPocMessageTooLate(ctx, msg)

	case *authztypes.MsgExec:
		// Recursively validate messages inside MsgExec
		if ppd.inferenceKeeper == nil {
			return nil
		}
		if depth >= maxMsgExecNestingDepth {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgExec nested past the inspection limit",
				inferencetypes.PoC,
				"depth", depth,
				"grantee", m.Grantee,
			)
			return sdkerrors.ErrInvalidRequest.Wrapf(
				"MsgExec nested more than %d levels cannot be validated during CheckTx", maxMsgExecNestingDepth)
		}
		for _, innerMsg := range m.Msgs {
			var unwrapped sdk.Msg
			if err := ppd.inferenceKeeper.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
				ppd.inferenceKeeper.LogDebug(
					"AnteHandle: PocPeriodValidation - failed to unpack authz MsgExec inner msg",
					inferencetypes.PoC,
					"error", err,
				)
				continue
			}
			if err := ppd.checkMessage(ctx, unwrapped, depth+1); err != nil {
				return err
			}
		}
	}

	return nil
}

func (ppd PocPeriodValidationDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}

	// Only perform validation during CheckTx (including ReCheckTx)
	if !ctx.IsCheckTx() {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		if err := ppd.checkMessage(ctx, msg, 0); err != nil {
			return ctx, err
		}
	}

	return next(ctx, tx, simulate)
}
