package adminface

import (
	"context"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// EpochAdvancer performs a catch-up epoch transition with simulated block production.
type EpochAdvancer interface {
	AdvanceEpoch(ctx context.Context) (*EpochAdvanceResponse, error)
}

// EpochAdvanceResponse is returned by POST /testenv/epoch with advance=true.
type EpochAdvanceResponse struct {
	Epoch                   inferencetypes.Epoch `json:"epoch"`
	FromBlockHeight         int64                `json:"from_block_height"`
	ToBlockHeight           int64                `json:"to_block_height"`
	NextPocStartBlockHeight int64                `json:"next_poc_start_block_height"`
	BlocksPublished         int64                `json:"blocks_published"`
}
