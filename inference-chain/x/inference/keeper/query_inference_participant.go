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
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	balance := k.bankView.SpendableCoin(ctx, addr, BaseCoin)

	return &types.QueryInferenceParticipantResponse{
		Pubkey:  base64.StdEncoding.EncodeToString(acc.GetPubKey().Bytes()),
		Balance: balance.Amount.Int64(),
	}, nil
}
