package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const SubnetPruningThreshold = uint64(2)
const SubnetPruningMax = int64(100)

// distributeUnsettledEscrow splits the escrowed funds equally among unique validators in the group.
// Integer division remainder stays in the module account.
func (k Keeper) distributeUnsettledEscrow(ctx context.Context, escrow types.SubnetEscrow) error {
	// Count unique addresses (first pass)
	seen := make(map[string]bool)
	var uniqueCount uint64
	for _, addr := range escrow.Slots {
		if !seen[addr] {
			seen[addr] = true
			uniqueCount++
		}
	}

	if uniqueCount == 0 {
		return nil
	}

	share := escrow.Amount / uniqueCount
	if share == 0 {
		return nil
	}

	// Pay in slot order (deterministic iteration over escrow.Slots)
	paid := make(map[string]bool)
	for _, addr := range escrow.Slots {
		if paid[addr] {
			continue
		}
		paid[addr] = true

		recipient, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			k.LogError("invalid address in unsettled escrow", types.Pruning,
				"escrow_id", escrow.Id, "address", addr)
			continue
		}
		coins, err := types.GetCoins(int64(share))
		if err != nil {
			continue
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipient, coins, "subnet_escrow_unsettled_distribution")
		if err != nil {
			k.LogError("failed to distribute unsettled escrow funds", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "error", err)
		}
	}

	return nil
}
