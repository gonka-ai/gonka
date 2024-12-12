package cosmosclient

import (
	"context"
	"fmt"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	"github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"log/slog"
)

// QueryByKey Query any stored value by key, e.g.:
// storeKey: "inference",
// dataKey: "ActiveParticipants/value/"
func QueryByKey(rpcClient *http.HTTP, storeKey, dataKey string, blockHeight int64, withProof bool) (*coretypes.ResultABCIQuery, error) {
	slog.Info("Querying store", "storeKey", storeKey, "dataKey", dataKey)

	key := []byte(dataKey)
	path := fmt.Sprintf("store/%s/key", storeKey)

	return rpcClient.ABCIQueryWithOptions(context.Background(), path, key, rpcclient.ABCIQueryOptions{Height: blockHeight, Prove: withProof})
}
