package app

import (
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type PocPeriodValidationDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
	cdc             codec.Codec
}

func NewPocPeriodValidationDecorator(ik *inferencemodulekeeper.Keeper, cdc codec.Codec) PocPeriodValidationDecorator {
	return PocPeriodValidationDecorator{
		inferenceKeeper: ik,
		cdc:             cdc,
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

func (ppd PocPeriodValidationDecorator) checkPocMessageSender(ctx sdk.Context, msg sdk.Msg) error {
	if ppd.inferenceKeeper == nil {
		return nil
	}

	cdc, err := ppd.msgCodec()
	if err != nil {
		return err
	}

	signers, _, err := cdc.GetMsgV1Signers(msg)
	if err != nil {
		return err
	}
	if len(signers) != 1 {
		return authztypes.ErrAuthorizationNumOfSigners
	}

	granter := sdk.AccAddress(signers[0])
	found, err := ppd.inferenceKeeper.Participants.Has(ctx, granter)
	if err != nil {
		return err
	}
	if !found {
		return inferencetypes.ErrParticipantNotFound
	}
	return nil
}

func (ppd PocPeriodValidationDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg) error {
	switch m := msg.(type) {
	case *inferencetypes.MsgSubmitPocBatch,
		*inferencetypes.MsgSubmitPocValidationsV2,
		*inferencetypes.MsgPoCV2StoreCommit, *inferencetypes.MsgMLNodeWeightDistribution:
		if err := ppd.checkPocMessageSender(ctx, msg); err != nil {
			return err
		}
		return ppd.checkPocMessageTooLate(ctx, msg)

	case *authztypes.MsgExec:
		// Recursively validate messages inside MsgExec. Fail closed on
		// unpack errors and nested wrappers — this is a mempool admission filter.
		if ppd.cdc == nil {
			return sdkerrors.ErrInvalidRequest.Wrap("codec is required to unpack MsgExec")
		}
		for _, innerMsg := range m.Msgs {
			var unwrapped sdk.Msg
			if err := ppd.cdc.UnpackAny(innerMsg, &unwrapped); err != nil {
				if ppd.inferenceKeeper != nil {
					ppd.inferenceKeeper.LogDebug(
						"AnteHandle: PocPeriodValidation - failed to unpack authz MsgExec inner msg",
						inferencetypes.PoC,
						"error", err,
					)
				}
				return err
			}
			if _, nested := unwrapped.(*authztypes.MsgExec); nested {
				return errNestedMsgExec
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

func (ppd PocPeriodValidationDecorator) msgCodec() (codec.Codec, error) {
	if ppd.cdc != nil {
		return ppd.cdc, nil
	}
	return nil, sdkerrors.ErrInvalidRequest.Wrap("codec is required for PoC sender check")
}
