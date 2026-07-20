package rpcface

import (
	"context"
	"fmt"

	"devshard/testenv/mockchain/adminface"
)

// AdvanceEpoch fast-forwards CometBFT blocks to the next PoC start height, then commits
// the epoch transition on the shared store (simulates chain catching up to epoch boundary).
func (s *Service) AdvanceEpoch(_ context.Context) (*adminface.EpochAdvanceResponse, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("mockchain rpc: nil service")
	}
	plan := s.store.PlanEpochAdvance()
	published, err := s.fastForwardBlocks(plan.FromHeight, plan.ToHeight)
	if err != nil {
		return nil, err
	}
	epoch := s.store.ApplyEpochAdvance(plan)
	return &adminface.EpochAdvanceResponse{
		Epoch:                   epoch,
		FromBlockHeight:         plan.FromHeight,
		ToBlockHeight:           plan.ToHeight,
		NextPocStartBlockHeight: plan.NewNextPoc,
		BlocksPublished:         published,
	}, nil
}

func (s *Service) fastForwardBlocks(fromHeight, toHeight int64) (int64, error) {
	if toHeight <= fromHeight {
		return 0, nil
	}
	var published int64
	for h := fromHeight + 1; h <= toHeight; h++ {
		s.store.SetBlockHeight(h)
		if err := s.publishNewBlock(h); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

// Ensure Service implements adminface.EpochAdvancer.
var _ adminface.EpochAdvancer = (*Service)(nil)
