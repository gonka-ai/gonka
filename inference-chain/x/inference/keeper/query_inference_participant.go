package keeper

import (
	"context"
	"encoding/base64"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) InferenceParticipant(goCtx context.Context, req *types.QueryInferenceParticipantRequest) (*types.QueryInferenceParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Process the query
	_ = ctx
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid address")
	}
	k.LogDebug("InferenceParticipant address converted", "address", addr.String())
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc == nil {
		k.LogError("InferenceParticipant: Not Found", "address", req.Address)
		return nil, status.Error(codes.NotFound, "account not found")
	}
	k.LogDebug("InferenceParticipant account found", "address", req.Address)
	balance := k.bankView.SpendableCoin(ctx, addr, types.BaseCoin)

	k.LogDebug("InferenceParticipant balance", "balance", balance)
	k.LogDebug("InferenceParticipant pubkey", "pubkey", acc.GetPubKey().Bytes())
	return &types.QueryInferenceParticipantResponse{
		Pubkey:  base64.StdEncoding.EncodeToString(acc.GetPubKey().Bytes()),
		Balance: balance.Amount.Int64(),
	}, nil
}
