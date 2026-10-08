package readiness

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Cards reads the GPUs this machine actually has
type Cards interface {
	// Inventory returns the model and count the host sees for the node; an error means they could
	// not be read, and keeps the node out
	Inventory(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}

// Claim reads the GPU inventory the chain holds for a node
type Claim interface {
	// Hardware returns what the node declared on chain; a node that declared nothing is a zero
	// inventory, not an error. An error means the chain could not be asked
	Hardware(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}

// Keys reads what the chain lets the daemon's key sign for the participant
type Keys interface {
	// MissingGrants returns the training message types the signer holds no grant for; none when
	// the signer is the participant itself. An error means the chain could not be asked
	MissingGrants(ctx context.Context, participant vo.Participant, signer vo.Address) ([]string, error)
}
