package usecases

import (
	"context"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type EvaluateReadinessUseCase struct {
	probe ports.Probe
	cards readiness.Cards
	claim readiness.Claim
	spec  readiness.Spec
}

func NewEvaluateReadinessUseCase(
	probe ports.Probe,
	cards readiness.Cards,
	claim readiness.Claim,
	spec readiness.Spec,
) *EvaluateReadinessUseCase {
	return &EvaluateReadinessUseCase{probe: probe, cards: cards, claim: claim, spec: spec}
}

func (uc *EvaluateReadinessUseCase) Execute(ctx context.Context, node vo.NodeRef) readiness.Result {
	// 1. Report what the machine and the chain say about this node
	return readiness.Collect(ctx, uc.probe, uc.cards, uc.claim, node, uc.spec)
}
