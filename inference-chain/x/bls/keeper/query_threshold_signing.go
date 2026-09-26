package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/bls/types"
)

// SigningStatus queries the status of a specific threshold signing request
func (k Keeper) SigningStatus(ctx context.Context, req *types.QuerySigningStatusRequest) (*types.QuerySigningStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Validate request_id
	if len(req.RequestId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "request_id cannot be empty")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Get threshold signing request using the existing GetSigningStatus method
	signingRequest, err := k.GetSigningStatus(sdkCtx, req.RequestId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "threshold signing request not found: %s", err.Error())
	}

	return &types.QuerySigningStatusResponse{
		SigningRequest: *signingRequest,
	}, nil
}

// SigningHistory queries threshold signing requests with filtering and pagination
func (k Keeper) SigningHistory(ctx context.Context, req *types.QuerySigningHistoryRequest) (*types.QuerySigningHistoryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Get KV store with threshold signing prefix
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(sdkCtx))
	signingStore := prefix.NewStore(store, types.ThresholdSigningRequestPrefix)

	var signingRequests []types.ThresholdSigningRequest

	// FilteredPaginate applies the epoch/status filters before the page
	// limit, so a page holds up to Limit matching requests and count_total
	// counts matches. query.Paginate counted every stored request against
	// the limit, returning empty pages with a next_key for filtered queries.
	pageRes, err := query.FilteredPaginate(signingStore, req.Pagination, func(key []byte, value []byte, accumulate bool) (bool, error) {
		var signingRequest types.ThresholdSigningRequest
		if err := k.cdc.Unmarshal(value, &signingRequest); err != nil {
			return false, err
		}

		// Apply filters
		if !k.matchesFilters(&signingRequest, req) {
			return false, nil // Skip this request
		}
		if !accumulate {
			return true, nil
		}

		// Rehydrate PartialSignatures from sub-keys. The base struct is
		// persisted with PartialSignatures stripped (see
		// storeThresholdSigningRequest); reading it raw here would expose
		// an empty slice to API consumers.
		partials, err := k.ListThresholdPartialSignatures(sdkCtx, signingRequest.RequestId)
		if err != nil {
			return false, fmt.Errorf("list partial sigs for request %x: %w", signingRequest.RequestId, err)
		}
		signingRequest.PartialSignatures = partials

		signingRequests = append(signingRequests, signingRequest)
		return true, nil
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to paginate signing requests: %s", err.Error())
	}

	return &types.QuerySigningHistoryResponse{
		SigningRequests: signingRequests,
		Pagination:      pageRes,
	}, nil
}

// matchesFilters checks if a signing request matches the query filters
func (k Keeper) matchesFilters(request *types.ThresholdSigningRequest, req *types.QuerySigningHistoryRequest) bool {
	// Filter by epoch if specified (0 means all epochs)
	if req.CurrentEpochId != 0 && request.CurrentEpochId != req.CurrentEpochId {
		return false
	}

	// Filter by status if specified (UNDEFINED means all statuses)
	if req.StatusFilter != types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_UNDEFINED &&
		request.Status != req.StatusFilter {
		return false
	}

	return true
}

// GetActiveSigningRequestsCount returns the count of active signing requests for an epoch
// This can be used to extend the existing EpochBLSData query
func (k Keeper) GetActiveSigningRequestsCount(ctx sdk.Context, epochId uint64) (uint32, error) {
	activeRequests, err := k.ListActiveSigningRequests(ctx, epochId)
	if err != nil {
		return 0, fmt.Errorf("failed to list active signing requests: %w", err)
	}

	return uint32(len(activeRequests)), nil
}
