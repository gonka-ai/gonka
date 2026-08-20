package usecases

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Reconciler brings one node to what the store says it should hold, so a command
// answers with the machine's real state instead of the next tick's promise
type Reconciler interface {
	Execute(ctx context.Context, node vo.NodeRef) error
}
