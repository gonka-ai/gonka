package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// TransferStatus queries the completion status for a specific genesis account
func (k Keeper) TransferStatus(goCtx context.Context, req *types.QueryTransferStatusRequest) (*types.QueryTransferStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.GenesisAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "genesis address cannot be empty")
	}

	// Try to get the transfer record for this address
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(goCtx))
	transferRecordStore := prefix.NewStore(store, types.KeyPrefix(types.TransferRecordKeyPrefix))

	key := []byte(req.GenesisAddress)
	bz := transferRecordStore.Get(key)

	if bz == nil {
		// No transfer record found
		return &types.QueryTransferStatusResponse{
			IsTransferred:  false,
			TransferRecord: nil,
		}, nil
	}

	// Unmarshal the transfer record
	var transferRecord types.TransferRecord
	k.cdc.MustUnmarshal(bz, &transferRecord)

	return &types.QueryTransferStatusResponse{
		IsTransferred:  transferRecord.Completed,
		TransferRecord: &transferRecord,
	}, nil
}

// TransferHistory retrieves historical transfer records with optional pagination
func (k Keeper) TransferHistory(goCtx context.Context, req *types.QueryTransferHistoryRequest) (*types.QueryTransferHistoryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Use the dedicated transfer records function for better maintainability
	transferRecords, pageRes, err := k.GetAllTransferRecords(goCtx, req.Pagination)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryTransferHistoryResponse{
		TransferRecords: transferRecords,
		Pagination:      pageRes,
	}, nil
}

// AllowedAccounts queries the whitelist of accounts eligible for transfer (if enabled)
func (k Keeper) AllowedAccounts(goCtx context.Context, req *types.QueryAllowedAccountsRequest) (*types.QueryAllowedAccountsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	params := k.GetParams(goCtx)

	return &types.QueryAllowedAccountsResponse{
		AllowedAccounts: params.AllowedAccounts,
		RestrictToList:  params.RestrictToList,
	}, nil
}

// TransferEligibility validates whether a specific account can be transferred
func (k Keeper) TransferEligibility(goCtx context.Context, req *types.QueryTransferEligibilityRequest) (*types.QueryTransferEligibilityResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.GenesisAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "genesis address cannot be empty")
	}

	// Parse the genesis address for comprehensive validation
	genesisAddr, err := sdk.AccAddressFromBech32(req.GenesisAddress)
	if err != nil {
		return &types.QueryTransferEligibilityResponse{
			IsEligible:         false,
			Reason:             "invalid genesis address format",
			AlreadyTransferred: false,
		}, nil
	}

	// Use our comprehensive validation function which includes:
	// - Account existence validation
	// - Balance validation
	// - Account type validation
	// - Transfer history validation
	// - Whitelist validation
	isEligible, reason, alreadyTransferred, err := k.ValidateTransferEligibility(goCtx, genesisAddr)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryTransferEligibilityResponse{
		IsEligible:         isEligible,
		Reason:             reason,
		AlreadyTransferred: alreadyTransferred,
	}, nil
}
