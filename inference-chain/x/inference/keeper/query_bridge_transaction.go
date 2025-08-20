package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
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

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(goCtx))
	bridgeStore := prefix.NewStore(storeAdapter, []byte(BridgeTransactionKeyPrefix))

	var bridgeTransactions []*types.BridgeTransaction
	pageRes, err := query.Paginate(bridgeStore, req.Pagination, func(key []byte, value []byte) error {
		var bridgeTx types.BridgeTransaction
		if err := k.cdc.Unmarshal(value, &bridgeTx); err != nil {
			return err
		}
		bridgeTransactions = append(bridgeTransactions, &bridgeTx)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	txs := make([]types.BridgeTransaction, len(bridgeTransactions))
	for i, tx := range bridgeTransactions {
		txs[i] = *tx
	}

	return &types.QueryAllBridgeTransactionsResponse{
		BridgeTransactions: txs,
		Pagination:         pageRes,
	}, nil
}
