package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPow(goCtx context.Context, msg *types.MsgSubmitPow) (*types.MsgSubmitPowResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// 1. Get block hash
	startBlockHeight := msg.BlockHeight
	currentBlockHeight := ctx.BlockHeight()

	if startBlockHeight%240 != 0 {
		return nil, types.ErrWrongStartBlockHeight
	}

	switch uint64(currentBlockHeight) - startBlockHeight {
	case 300, 301, 302, 303: // DO NOTHING
	default:
		return nil, types.ErrPowTooLate
	}

	// 1. Get block hash from startBlockHeight

	// 2. Get signer public key
	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, err
	}
	account := k.AccountKeeper.GetAccount(ctx, addr)
	pubKey := account.GetPubKey()
	// PRTODO: use block hash and pubKey to verify proofs
	_ = pubKey

	_ = ctx

	return &types.MsgSubmitPowResponse{}, nil
}
