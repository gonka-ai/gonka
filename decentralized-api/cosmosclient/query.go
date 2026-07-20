package cosmosclient

import (
	"context"
	"common/logging"
	"decentralized-api/observability"
	"fmt"

	rpcclient "github.com/cometbft/cometbft/rpc/client"
	"github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/x/inference/types"
)

// QueryByKeyWithOptions Query any stored value by key, e.g.:
// storeKey: "inference",
// dataKey: "ActiveParticipants/value/"
func QueryByKeyWithOptions(rpcClient *http.HTTP, storeKey string, dataKey []byte, blockHeight int64, withProof bool) (*coretypes.ResultABCIQuery, error) {
	logging.Info("Querying store", types.System, "storeKey", storeKey, "dataKey", dataKey)

	path := fmt.Sprintf("store/%s/key", storeKey)
	queryCtx, queryOp := observability.Chain.StartStoreQuery(context.Background(), storeKey, withProof, blockHeight)
	var spanError error
	defer queryOp.FinishErr(&spanError)

	result, err := rpcClient.ABCIQueryWithOptions(queryCtx, path, dataKey, rpcclient.ABCIQueryOptions{Height: blockHeight, Prove: withProof})
	if err != nil {
		spanError = fmt.Errorf("query store %s with options: %w", storeKey, err)
	}
	return result, err
}

func QueryByKey(rpcClient *http.HTTP, storeKey string, dataKey []byte) (*coretypes.ResultABCIQuery, error) {
	logging.Info("Querying store", types.System, "storeKey", storeKey, "dataKey", dataKey)

	path := fmt.Sprintf("store/%s/key", storeKey)
	queryCtx, queryOp := observability.Chain.StartStoreQuery(context.Background(), storeKey, false, 0)
	var spanError error
	defer queryOp.FinishErr(&spanError)

	result, err := rpcClient.ABCIQuery(queryCtx, path, dataKey)
	if err != nil {
		spanError = fmt.Errorf("query store %s: %w", storeKey, err)
	}
	return result, err
}
