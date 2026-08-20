package usecases

import (
	"context"
	"time"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type RefreshOptInUseCase struct {
	readiness *EvaluateReadinessUseCase
	submitter shard.ChainSubmitter
	ttl       time.Duration
}

func NewRefreshOptInUseCase(
	readiness *EvaluateReadinessUseCase,
	submitter shard.ChainSubmitter,
	ttl time.Duration,
) *RefreshOptInUseCase {
	return &RefreshOptInUseCase{readiness: readiness, submitter: submitter, ttl: ttl}
}

func (uc *RefreshOptInUseCase) Execute(ctx context.Context, node vo.NodeRef) (readiness.Result, error) {
	// 1. Return if not ready
	result := uc.readiness.Execute(ctx, node)
	if !result.Ready {
		return result, nil
	}

	// 2. Refresh TTL with opt-in
	return result, uc.submitter.OptIn(ctx, node, uc.ttl)
}
