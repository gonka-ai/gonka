package keeper

import (
	"context"
	"fmt"
	"math"

	"github.com/productscience/inference/x/inference/types"
)

const DevshardPruningThreshold = uint64(2)
const DevshardPruningMax = int64(100)

// distributeUnsettledEscrow splits the escrowed funds across the group's slots: each slot
// receives an equal share, so a validator occupying N slots receives N shares. This matches
// how settlement pays per slot; distributing per unique address instead under-pays
// validators that hold more than one slot in the group.
// Integer division remainder stays in the module account.
func (k Keeper) distributeUnsettledEscrow(ctx context.Context, escrow types.DevshardEscrow) error {
	slotCount := uint64(len(escrow.Slots))
	if slotCount == 0 {
		return nil
	}

	share := escrow.Amount / slotCount
	if share == 0 {
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}
	if params.TokenomicsParams == nil {
		return fmt.Errorf("tokenomics params not configured")
	}
	// Host shares vest over WorkVestingPeriod. A period of 0 remains a liquid transfer.
	workVestingPeriod := &params.TokenomicsParams.WorkVestingPeriod

	// Aggregate the per-slot share by recipient (a validator in N slots is owed N shares),
	// preserving deterministic slot order for the first appearance of each address.
	amountByAddr := make(map[string]uint64)
	order := make([]string, 0, len(escrow.Slots))
	for _, addr := range escrow.Slots {
		if _, seen := amountByAddr[addr]; !seen {
			order = append(order, addr)
		}
		amountByAddr[addr] += share
	}

	for _, addr := range order {
		recipient, err := k.ResolveClaimRecipientAddress(ctx, addr, escrow.EpochIndex)
		if err != nil {
			k.LogError("failed to resolve unsettled escrow recipient", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "epoch", escrow.EpochIndex, "error", err)
			continue
		}
		amount := amountByAddr[addr]
		if amount > math.MaxInt64 {
			k.LogError("unsettled escrow share exceeds max int64", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "amount", amount)
			continue
		}
		err = k.PayParticipantFromModule(ctx, recipient.String(), int64(amount), types.ModuleName, "devshard_escrow_unsettled_distribution", workVestingPeriod)
		if err != nil {
			k.LogError("failed to distribute unsettled escrow funds", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "error", err)
		}
	}

	return nil
}
