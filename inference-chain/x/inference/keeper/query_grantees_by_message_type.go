package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
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

	// Parse granter address to validate it
	_, err := sdk.AccAddressFromBech32(req.GranterAddress)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid granter address")
	}

	// For now, return a simple implementation
	// TODO: This is a minimal implementation that needs to be enhanced
	// to properly iterate through authz grants when the authz keeper interface is extended

	k.LogInfo("GranteesByMessageType query called", types.Participants,
		"granter", req.GranterAddress,
		"messageType", req.MessageTypeUrl)

	// Placeholder implementation - in a real scenario we would:
	// 1. Iterate through all authz grants where the granter matches the request
	// 2. Filter by message type URL
	// 3. Check that grants haven't expired
	// 4. Return deduplicated list of grantee addresses

	// For now, return empty response
	return &types.QueryGranteesByMessageTypeResponse{
		GranteeAddresses: []string{},
	}, nil
}
