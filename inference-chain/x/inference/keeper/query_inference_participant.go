package keeper

import (
	"context"
	"encoding/base64"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) AccountByAddress(goCtx context.Context, req *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid address")
	}

	balance := k.BankView.SpendableCoin(ctx, addr, types.BaseCoin)

	k.LogDebug("AccountByAddress address converted", types.Participants, "address", addr.String())
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		k.LogError("AccountByAddress: Not Found", types.Participants, "address", req.Address)
		return nil, status.Error(codes.NotFound, "account not found")
	}
	k.LogDebug("AccountByAddress account found", types.Participants, "address", req.Address)

	k.LogDebug("AccountByAddress balance", types.Participants, "balance", balance)

	pubKey := acc.GetPubKey()
	if pubKey == nil {
		k.LogError("AccountByAddress: PubKey not found", types.Participants, "address", req.Address)
		return nil, status.Error(codes.NotFound, types.ErrPubKeyUnavailable.Error())
	}

	k.LogDebug("AccountByAddress pubkey", types.Participants, "pubkey", pubKey.Bytes())
	return &types.QueryAccountByAddressResponse{
		Pubkey:  base64.StdEncoding.EncodeToString(pubKey.Bytes()),
		Balance: balance.Amount.Int64(),
		Denom:   types.BaseCoin,
	}, nil
}
