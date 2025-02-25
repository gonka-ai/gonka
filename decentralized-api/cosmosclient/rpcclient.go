package cosmosclient

import (
	"context"
	"github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
)

type TendermintClient struct {
	ChainNodeUrl string
}

// NewRpcClient Can be used to query Block, Validators, and other data from the Cosmos SDK node.
func NewRpcClient(address string) (*http.HTTP, error) {
	return http.New(address, "/websocket")
}

func (c *TendermintClient) Status() (*coretypes.ResultStatus, error) {
	client, err := NewRpcClient(c.ChainNodeUrl)
	if err != nil {
		return nil, err
	}

	return client.Status(context.Background())
}
