package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) BridgeTransaction(goCtx context.Context, req *types.QueryGetBridgeTransactionRequest) (*types.QueryGetBridgeTransactionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	bridgeTx, found := k.GetBridgeTransaction(ctx, req.OriginChain, req.BlockNumber, req.ReceiptIndex)
	if !found {
		return nil, status.Error(codes.NotFound, "bridge transaction not found")
	}

	return &types.QueryGetBridgeTransactionResponse{
		BridgeTransaction: *bridgeTx,
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
