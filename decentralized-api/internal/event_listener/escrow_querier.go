package event_listener

import (
	"context"
	"fmt"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

// devshardEscrowQueryClient is the subset of cosmosclient.InferenceCosmosClient
// used by ChainEscrowQuerier.
type devshardEscrowQueryClient interface {
	NewInferenceQueryClient() types.QueryClient
}

// ChainEscrowQuerier resolves escrow slot membership by querying the chain
// directly (x/inference DevshardEscrow), so DAPI's event listener can filter
// host events without depending on the devshard module.
type ChainEscrowQuerier struct {
	recorder devshardEscrowQueryClient
}

// NewChainEscrowQuerier builds a ChainEscrowQuerier backed by recorder.
func NewChainEscrowQuerier(recorder devshardEscrowQueryClient) *ChainEscrowQuerier {
	return &ChainEscrowQuerier{recorder: recorder}
}

// GetEscrow implements escrowQuerier.
func (q *ChainEscrowQuerier) GetEscrow(escrowID string) (*EscrowSlotInfo, error) {
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow id %q: %w", escrowID, err)
	}
	resp, err := q.recorder.NewInferenceQueryClient().DevshardEscrow(context.Background(),
		&types.QueryGetDevshardEscrowRequest{Id: id})
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
