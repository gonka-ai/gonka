package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ParticipantsWithBalances(ctx context.Context, req *types.QueryParticipantsWithBalancesRequest) (*types.QueryParticipantsWithBalancesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	participants, pageRes, err := query.CollectionPaginate(
		ctx,
		k.Participants,
		req.Pagination,
		func(address sdk.AccAddress, p types.Participant) (types.ParticipantWithBalance, error) {
			balances := k.BankView.GetAllBalances(ctx, address)
			return types.ParticipantWithBalance{
				Participant: p,
				Balances:    balances,
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return &types.QueryParticipantsWithBalancesResponse{
		Participants: participants,
		Pagination:   pageRes,
		BlockHeight:  sdkCtx.BlockHeight(),
	}, nil
}
