package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) BridgeTransaction(goCtx context.Context, req *types.QueryGetBridgeTransactionRequest) (*types.QueryGetBridgeTransactionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Find all bridge transactions that match the receipt location
	transactions := k.GetBridgeTransactionsByReceipt(goCtx, req.OriginChain, req.BlockNumber, req.ReceiptIndex)

	// Return all matching transactions (empty array if none found)
	// This allows API consumers to:
	// - See if there are no transactions (empty array)
	// - See normal case (single transaction)
	// - Detect conflicts (multiple transactions with different content)
	return &types.QueryGetBridgeTransactionResponse{
		BridgeTransactions: transactions,
	}, nil
}

func (k Keeper) BridgeTransactions(goCtx context.Context, req *types.QueryAllBridgeTransactionsRequest) (*types.QueryAllBridgeTransactionsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	bridgeTransactions, pageRes, err := query.CollectionPaginate(
		goCtx,
		k.BridgeTransactionsMap,
		req.Pagination,
		func(_ collections.Triple[string, string, string], v types.BridgeTransaction) (types.BridgeTransaction, error) {
			return v, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllBridgeTransactionsResponse{
		BridgeTransactions: bridgeTransactions,
		Pagination:         pageRes,
	}, nil
}
