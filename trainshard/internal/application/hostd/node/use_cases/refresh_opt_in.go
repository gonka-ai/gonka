package usecases

import (
	"context"
	"time"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type RefreshOptInUseCase struct {
	probe     ports.Probe
	cards     readiness.Cards
	claim     readiness.Claim
	submitter shard.ChainSubmitter
	spec      readiness.Spec
	ttl       time.Duration
}

func NewRefreshOptInUseCase(
	probe ports.Probe,
	cards readiness.Cards,
	claim readiness.Claim,
	submitter shard.ChainSubmitter,
	spec readiness.Spec,
	ttl time.Duration,
) *RefreshOptInUseCase {
	return &RefreshOptInUseCase{
		probe:     probe,
		cards:     cards,
		claim:     claim,
		submitter: submitter,
		spec:      spec,
		ttl:       ttl,
	}
}

func (uc *RefreshOptInUseCase) Execute(ctx context.Context, node vo.NodeRef) (readiness.Result, error) {
	// 1. Collect what the machine and the chain say about this node
	result := readiness.Collect(ctx, uc.probe, uc.cards, uc.claim, node, uc.spec)
	if !result.Ready {
		return result, nil
	}

	// 2. Refresh TTL with opt-in
	return result, uc.submitter.OptIn(ctx, node, uc.ttl)
}
