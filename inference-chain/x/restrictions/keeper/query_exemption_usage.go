package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/restrictions/types"
)

// ExemptionUsage queries usage statistics for emergency exemptions
func (k Keeper) ExemptionUsage(goCtx context.Context, req *types.QueryExemptionUsageRequest) (*types.QueryExemptionUsageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Validate account address if provided
	if req.AccountAddress != "" {
		if _, err := sdk.AccAddressFromBech32(req.AccountAddress); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid account address: %s", err)
		}
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)

	var filteredUsage []types.ExemptionUsage

	// Filter usage entries based on request parameters
	for _, usage := range params.ExemptionUsageTracking {
		// Filter by exemption ID if specified
		if req.ExemptionId != "" && usage.ExemptionId != req.ExemptionId {
			continue
		}

		// Filter by account address if specified
		if req.AccountAddress != "" && usage.AccountAddress != req.AccountAddress {
			continue
		}

		filteredUsage = append(filteredUsage, usage)
	}

	// Apply pagination
	var pageRes *query.PageResponse
	var paginatedUsage []types.ExemptionUsage

	// Note: For simplicity, we're applying pagination to the in-memory slice
	// In a production system with many usage entries, you might want to store them
	// separately and use proper store-based pagination
	if req.Pagination != nil {
		// Calculate pagination bounds
		offset := req.Pagination.Offset
		limit := req.Pagination.Limit
		if limit == 0 {
			limit = 100 // Default limit
		}

		totalLen := uint64(len(filteredUsage))

		// Apply offset
		if offset >= totalLen {
			paginatedUsage = []types.ExemptionUsage{}
		} else {
			// Apply limit
			end := offset + limit
			if end > totalLen {
				end = totalLen
			}
			paginatedUsage = filteredUsage[offset:end]
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
		paginatedUsage = filteredUsage
		pageRes = &query.PageResponse{
			Total: uint64(len(filteredUsage)),
		}
	}

	return &types.QueryExemptionUsageResponse{
		UsageEntries: paginatedUsage,
		Pagination:   pageRes,
	}, nil
}
