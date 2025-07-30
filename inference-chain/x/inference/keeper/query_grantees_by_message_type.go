package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.GranterAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "granter address cannot be empty")
	}

	if req.MessageTypeUrl == "" {
		return nil, status.Error(codes.InvalidArgument, "message type URL cannot be empty")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockTime := sdkCtx.BlockTime()

	authzKeeper := k.AuthzKeeper
	authReq := &authztypes.QueryGranterGrantsRequest{
		Granter: req.GranterAddress,
	}
	grants, err := authzKeeper.GranterGrants(ctx, authReq)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get grants")
	}

	grantees := []string{}
	for _, grant := range grants.Grants {
		if grant.Expiration != nil && grant.Expiration.Before(blockTime) {
			continue
		}

		authorization := grant.Authorization.GetCachedValue()

		if genericAuth, ok := authorization.(*authztypes.GenericAuthorization); ok {
			if genericAuth.Msg == req.MessageTypeUrl {
				grantees = append(grantees, grant.Grantee)
			}
		}
	}

	k.LogInfo("GranteesByMessageType query called", types.Participants,
		"granter", req.GranterAddress,
		"messageType", req.MessageTypeUrl,
		"grantees", grantees)

	return &types.QueryGranteesByMessageTypeResponse{
		GranteeAddresses: grantees,
	}, nil
}
