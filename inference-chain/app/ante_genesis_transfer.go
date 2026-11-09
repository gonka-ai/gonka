package app

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	genesistransfertypes "github.com/productscience/inference/x/genesistransfer/types"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
)

// GenesisTransferChecker is the part of the genesistransfer keeper the ante
// chain needs to apply the MsgTransferOwnership handler's signer checks.
type GenesisTransferChecker interface {
	IsTransferableAccount(ctx context.Context, address string) bool
	GetTransferRecord(ctx context.Context, genesisAddr sdk.AccAddress) (*genesistransfertypes.TransferRecord, bool, error)
}

// GenesisTransferEarlyRejectDecorator rejects in CheckTx a MsgTransferOwnership
// that its handler is certain to reject. The message has no fee group and no duty
// waiver (intentionallyUngrouped), so a zero-fee tx carrying it is admitted,
// gossiped and included, and only fails in DeliverTx — e.g. a signer outside
// allowed_accounts while restrict_to_list is on, or an account that has already
// transferred.
//
// Only those two checks run here, in the handler's order. The handler's
// balance check is left to DeliverTx: CheckTx state does not see funds arriving
// from txs still in the mempool, so checking it here could reject a tx the
// block would accept.
//
// CheckTx-only, like BridgeExchangeEarlyRejectDecorator: DeliverTx runs the
// handler unchanged. Messages inside MsgExec are checked too, unwrapped the same
// way fee classification does.
type GenesisTransferEarlyRejectDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
	checker         GenesisTransferChecker
}

func NewGenesisTransferEarlyRejectDecorator(ik *inferencemodulekeeper.Keeper, c GenesisTransferChecker) GenesisTransferEarlyRejectDecorator {
	return GenesisTransferEarlyRejectDecorator{inferenceKeeper: ik, checker: c}
}

func (d GenesisTransferEarlyRejectDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate || !ctx.IsCheckTx() || d.checker == nil {
		return next(ctx, tx, simulate)
	}
	msgs, err := unwrapFeeMsgs(tx.GetMsgs(), d.inferenceKeeper)
	if err != nil {
		// Fee classification already rejected this shape.
		return next(ctx, tx, simulate)
	}
	for _, msg := range msgs {
		m, ok := msg.(*genesistransfertypes.MsgTransferOwnership)
		if !ok {
			continue
		}
		// The two checks run in ValidateTransfer's order.
		genesisAddr, err := sdk.AccAddressFromBech32(m.GenesisAddress)
		if err != nil {
			return ctx, err
		}
		record, found, err := d.checker.GetTransferRecord(ctx, genesisAddr)
		if err != nil {
			return ctx, err
		}
		if found {
			return ctx, genesistransfertypes.ErrAlreadyTransferred.Wrapf(
				"genesis account %s has already been transferred to %s at height %d",
				m.GenesisAddress, record.RecipientAddress, record.TransferHeight)
		}
		if !d.checker.IsTransferableAccount(ctx, m.GenesisAddress) {
			return ctx, genesistransfertypes.ErrNotInAllowedList.Wrapf(
				"genesis account %s is not in the allowed accounts whitelist", m.GenesisAddress)
		}
	}
	return next(ctx, tx, simulate)
}
