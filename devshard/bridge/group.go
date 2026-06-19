package bridge

import (
	"fmt"
	"devshard/types"
)

// BuildGroupFromEscrow constructs slot assignments from an already-fetched escrow.
// Slots come from the chain (stored in DevshardEscrow), no re-derivation needed.
func BuildGroupFromEscrow(escrow *EscrowInfo) ([]types.SlotAssignment, error) {
	if escrow == nil {
		return nil, fmt.Errorf("escrow is nil")
	}

	group := make([]types.SlotAssignment, len(escrow.Slots))
	for i, addr := range escrow.Slots {
		group[i] = types.SlotAssignment{
			SlotID:           uint32(i),
			ValidatorAddress: addr,
		}
	}

	if err := types.ValidateGroup(group); err != nil {
		return nil, err
	}
	return group, nil
}

// BuildGroup fetches escrow data and constructs a session group.
func BuildGroup(escrowID string, b MainnetBridge) ([]types.SlotAssignment, error) {
	escrow, err := b.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}
	return BuildGroupFromEscrow(escrow)
}
