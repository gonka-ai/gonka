package cosmosclient

import (
	"context"
	"decentralized-api/logging"
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

	return rpcClient.ABCIQueryWithOptions(context.Background(), path, dataKey, rpcclient.ABCIQueryOptions{Height: blockHeight, Prove: withProof})
}

func QueryByKey(rpcClient *http.HTTP, storeKey string, dataKey []byte) (*coretypes.ResultABCIQuery, error) {
	logging.Info("Querying store", types.System, "storeKey", storeKey, "dataKey", dataKey)

	path := fmt.Sprintf("store/%s/key", storeKey)

	return rpcClient.ABCIQuery(context.Background(), path, dataKey)
}
