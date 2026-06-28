package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
		recipient, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			k.LogError("invalid address in unsettled escrow", types.Pruning,
				"escrow_id", escrow.Id, "address", addr)
			continue
		}
		coins, err := types.GetCoins(int64(amountByAddr[addr]))
		if err != nil {
			continue
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipient, coins, "devshard_escrow_unsettled_distribution")
		if err != nil {
			k.LogError("failed to distribute unsettled escrow funds", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "error", err)
		}
	}

	return nil
}
