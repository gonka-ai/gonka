package event_listener

import (
	"context"
	"fmt"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

// inferenceQueryProvider is the subset of cosmosclient used for escrow membership.
type inferenceQueryProvider interface {
	NewInferenceQueryClient() types.QueryClient
}

// ChainEscrowQuerier looks up escrow slot membership via chain query.
type ChainEscrowQuerier struct {
	client inferenceQueryProvider
}

// NewChainEscrowQuerier wraps a cosmos query client for GetEscrowSlots.
func NewChainEscrowQuerier(client inferenceQueryProvider) *ChainEscrowQuerier {
	return &ChainEscrowQuerier{client: client}
}

func (q *ChainEscrowQuerier) GetEscrowSlots(escrowID string) ([]string, error) {
	if q == nil || q.client == nil {
		return nil, fmt.Errorf("escrow querier: nil client")
	}
	id, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow id: %w", err)
	}
	resp, err := q.client.NewInferenceQueryClient().DevshardEscrow(context.Background(), &types.QueryGetDevshardEscrowRequest{Id: id})
	if err != nil {
		return nil, fmt.Errorf("query devshard escrow: %w", err)
	}
	if resp == nil || !resp.Found || resp.Escrow == nil {
		return nil, fmt.Errorf("escrow %s not found", escrowID)
	}
	return resp.Escrow.Slots, nil
}
