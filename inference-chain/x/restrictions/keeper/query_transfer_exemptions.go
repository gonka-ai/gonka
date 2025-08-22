package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/restrictions/types"
)

// TransferExemptions queries all active emergency transfer exemptions
func (k Keeper) TransferExemptions(goCtx context.Context, req *types.QueryTransferExemptionsRequest) (*types.QueryTransferExemptionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)

	currentHeight := uint64(ctx.BlockHeight())
	var filteredExemptions []types.EmergencyTransferExemption

	// Filter exemptions based on expiry if not including expired ones
	for _, exemption := range params.EmergencyTransferExemptions {
		if req.IncludeExpired || exemption.ExpiryBlock > currentHeight {
			filteredExemptions = append(filteredExemptions, exemption)
		}
	}

	// Apply pagination
	var pageRes *query.PageResponse
	var paginatedExemptions []types.EmergencyTransferExemption

	// Note: For simplicity, we're applying pagination to the in-memory slice
	// In a production system with many exemptions, you might want to store them
	// separately and use proper store-based pagination
	if req.Pagination != nil {
		// Calculate pagination bounds
		offset := req.Pagination.Offset
		limit := req.Pagination.Limit
		if limit == 0 {
			limit = 100 // Default limit
		}

		totalLen := uint64(len(filteredExemptions))

		// Apply offset
		if offset >= totalLen {
			paginatedExemptions = []types.EmergencyTransferExemption{}
		} else {
			// Apply limit
			end := offset + limit
			if end > totalLen {
				end = totalLen
			}
			paginatedExemptions = filteredExemptions[offset:end]
		}

		// Create page response
		pageRes = &query.PageResponse{
			Total: totalLen,
		}

		// Set next key if there are more results
		if offset+limit < totalLen {
			pageRes.NextKey = []byte("has_more")
		}
	} else {
		paginatedExemptions = filteredExemptions
		pageRes = &query.PageResponse{
			Total: uint64(len(filteredExemptions)),
		}
	}

	return &types.QueryTransferExemptionsResponse{
		Exemptions: paginatedExemptions,
		Pagination: pageRes,
	}, nil
}
