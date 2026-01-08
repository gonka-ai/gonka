package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
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

	case *inferencetypes.MsgSubmitPocValidation:
		if err := ppd.inferenceKeeper.CheckPoCMessageTooLate(ctx, m.PocStageStartBlockHeight, inferencemodulekeeper.PoCWindowValidation); err != nil {
			ppd.inferenceKeeper.LogDebug(
				"AnteHandle: PocPeriodValidation - rejecting MsgSubmitPocValidation as too late",
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

func (ppd PocPeriodValidationDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg) error {
	switch m := msg.(type) {
	case *inferencetypes.MsgSubmitPocBatch, *inferencetypes.MsgSubmitPocValidation:
		return ppd.checkPocMessageTooLate(ctx, msg)

	case *authztypes.MsgExec:
		// Recursively validate messages inside MsgExec
		if ppd.inferenceKeeper == nil {
			return nil
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
			if err := ppd.checkMessage(ctx, unwrapped); err != nil {
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
		if err := ppd.checkMessage(ctx, msg); err != nil {
			return ctx, err
		}
	}

	return next(ctx, tx, simulate)
}
