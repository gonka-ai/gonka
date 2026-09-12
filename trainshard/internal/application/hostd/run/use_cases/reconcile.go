package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type ReconcileUseCase struct {
	converge *run.Converger
}

func NewReconcileUseCase(converge *run.Converger) *ReconcileUseCase {
	return &ReconcileUseCase{converge: converge}
}

func (uc *ReconcileUseCase) Execute(ctx context.Context, node vo.NodeRef) error {
	// 1. Bring the node to what the chain and the store say it should hold
	return uc.converge.Converge(ctx, node)
}
