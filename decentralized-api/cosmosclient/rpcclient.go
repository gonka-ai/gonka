package cosmosclient

import "github.com/cometbft/cometbft/rpc/client/http"

// NewRpcClient Can be used to query Block, Validators, and other data from the Cosmos SDK node.
func NewRpcClient(address string) (*http.HTTP, error) {
	return http.New(address, "/websocket")
}
