package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// BridgeExchangeEarlyRejectDecorator rejects MsgBridgeExchange during CheckTx when
// the vote cannot succeed on DeliverTx. SDK 0.53 does not run message handlers in
// CheckTx, so without this ante BroadcastTxSync returns Code=0 and the API feeder
// stalls on confirm-timeout (#1478 classifier never sees the reject).
//
// Validation is delegated to keeper.ValidateBridgeExchange (same registered
// ErrBridge* / ErrActiveParticipantNotFound as the msg server). The API
// classifier matches those messages via types.Err*.Error() — do not hardcode
// RawLog substrings here.
//
// DeliverTx still enforces the full handler; this is mempool admission only.
type BridgeExchangeEarlyRejectDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
}

func NewBridgeExchangeEarlyRejectDecorator(ik *inferencemodulekeeper.Keeper) BridgeExchangeEarlyRejectDecorator {
	return BridgeExchangeEarlyRejectDecorator{inferenceKeeper: ik}
}

func (d BridgeExchangeEarlyRejectDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}
	if !ctx.IsCheckTx() {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		if err := d.checkMessage(ctx, msg); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

func (d BridgeExchangeEarlyRejectDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg) error {
	switch m := msg.(type) {
	case *inferencetypes.MsgBridgeExchange:
		return d.checkBridgeExchangeMsg(ctx, m)

	case *authztypes.MsgExec:
		if d.inferenceKeeper == nil {
			return nil
		}
		for _, innerMsg := range m.Msgs {
			var unwrapped sdk.Msg
			if err := d.inferenceKeeper.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
				d.inferenceKeeper.LogDebug(
					"AnteHandle: BridgeExchangeEarlyReject - failed to unpack authz MsgExec inner msg",
					inferencetypes.Messages,
					"error", err,
				)
				continue
			}
			if err := d.checkMessage(ctx, unwrapped); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d BridgeExchangeEarlyRejectDecorator) checkBridgeExchangeMsg(ctx sdk.Context, msg *inferencetypes.MsgBridgeExchange) error {
	if d.inferenceKeeper == nil || msg == nil {
		return nil
	}
	_, err := d.inferenceKeeper.ValidateBridgeExchange(ctx, msg)
	return err
}
