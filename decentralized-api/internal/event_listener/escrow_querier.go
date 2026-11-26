package event_listener

import (
	"context"
	"fmt"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// devshardEscrowQueryClient is the subset of cosmosclient.InferenceCosmosClient
// used by ChainEscrowQuerier.
type devshardEscrowQueryClient interface {
	NewInferenceQueryClient() types.QueryClient
}

// escrowRPC is the query ChainEscrowQuerier actually calls.
type escrowRPC interface {
	DevshardEscrow(ctx context.Context, in *types.QueryGetDevshardEscrowRequest, opts ...grpc.CallOption) (*types.QueryGetDevshardEscrowResponse, error)
}

// ChainEscrowQuerier resolves escrow slot membership by querying the chain
// directly (x/inference DevshardEscrow), so DAPI's event listener can filter
// host events without depending on the devshard module.
type ChainEscrowQuerier struct {
	rpc func() escrowRPC
}

// NewChainEscrowQuerier builds a ChainEscrowQuerier backed by recorder.
func NewChainEscrowQuerier(recorder devshardEscrowQueryClient) *ChainEscrowQuerier {
	return &ChainEscrowQuerier{rpc: func() escrowRPC { return recorder.NewInferenceQueryClient() }}
}

// GetEscrow implements escrowQuerier. ctx is passed through to the chain query.
func (q *ChainEscrowQuerier) GetEscrow(ctx context.Context, escrowID string) (*EscrowSlotInfo, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow id %q: %w", escrowID, err)
	}
	if q == nil || q.rpc == nil {
		return nil, fmt.Errorf("query devshard escrow %s: no querier", escrowID)
	}
	resp, err := q.rpc().DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("query devshard escrow %s: %w", escrowID, err)
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, ErrEscrowNotFound
	}
	slots := make([]string, len(resp.Escrow.Slots))
	copy(slots, resp.Escrow.Slots)
	return &EscrowSlotInfo{EscrowID: escrowID, Slots: slots}, nil
}
