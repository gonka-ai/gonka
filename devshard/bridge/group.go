package bridge

import (
	"fmt"
	"devshard/types"
)

// BuildGroup fetches escrow data to construct a session group.
// Slots come from the chain (stored in DevshardEscrow), no re-derivation needed.
func BuildGroup(escrowID string, b MainnetBridge) ([]types.SlotAssignment, error) {
	escrow, err := b.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
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
