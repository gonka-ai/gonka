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

	// Get current block height to avoid "height must be greater than 0" error
	// when querying with default height 0
	status, err := rpcClient.Status(context.Background())
	if err != nil {
		logging.Error("Failed to get node status for query height", types.System, "error", err)
		return nil, fmt.Errorf("failed to get node status: %w", err)
	}

	blockHeight := status.SyncInfo.LatestBlockHeight
	if blockHeight <= 0 {
		return nil, fmt.Errorf("invalid block height %d from node status", blockHeight)
	}

	logging.Debug("Using block height for query", types.System, "height", blockHeight)

	path := fmt.Sprintf("store/%s/key", storeKey)

	return rpcClient.ABCIQueryWithOptions(context.Background(), path, dataKey, rpcclient.ABCIQueryOptions{Height: blockHeight, Prove: false})
}
