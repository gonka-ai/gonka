package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListClaimRecipients returns every currently-scheduled (epoch, recipient)
// override for the given participant, ordered by epoch.
func (k Keeper) ListClaimRecipients(ctx context.Context, req *types.QueryListClaimRecipientsRequest) (*types.QueryListClaimRecipientsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addr, err := sdk.AccAddressFromBech32(req.Participant)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %s", err)
	}
	entries, err := k.GetClaimRecipientsByParticipant(ctx, addr)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read schedule: %s", err)
	}
	return &types.QueryListClaimRecipientsResponse{Entries: entries}, nil
}
