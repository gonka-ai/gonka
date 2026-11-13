package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BridgeKeyLink returns the link for a receipt owner address.
// A missing link is found=false, not an error. Settlement then pays owner_address.
func (k Keeper) BridgeKeyLink(ctx context.Context, req *types.QueryBridgeKeyLinkRequest) (*types.QueryBridgeKeyLinkResponse, error) {
	if req == nil || req.OwnerAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_address is required")
	}
	owner, err := sdk.AccAddressFromBech32(req.OwnerAddress)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid owner_address")
	}
	canonical := owner.String()

	link, err := k.BridgeKeyLinks.Get(ctx, canonical)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryBridgeKeyLinkResponse{
				Found:        false,
				OwnerAddress: canonical,
			}, nil
		}
		return nil, err
	}
	return &types.QueryBridgeKeyLinkResponse{
		Found:            true,
		OwnerAddress:     canonical,
		RecipientAddress: link.RecipientAddress,
		Nonce:            link.Nonce,
	}, nil
}
