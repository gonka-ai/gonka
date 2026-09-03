package readiness

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Cards is this machine's GPU inventory
type Cards interface {
	// Inventory returns model and count as the host sees them
	Inventory(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}

// Claim is the GPU inventory the chain holds for a node
type Claim interface {
	// Hardware returns what the node claimed on chain
	Hardware(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}
