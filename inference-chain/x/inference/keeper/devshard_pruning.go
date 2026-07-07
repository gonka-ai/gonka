package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const DevshardPruningThreshold = uint64(2)
const DevshardPruningMax = int64(100)

// distributeUnsettledEscrow splits the escrowed funds equally among unique validators in the group.
// Integer division remainder stays in the module account.
// All payments are wrapped in a CacheContext: if any SendCoins fails, no payments are committed,
// preventing partial distributions that would cause double-payment on the next pruning epoch.
func (k Keeper) distributeUnsettledEscrow(ctx context.Context, escrow types.DevshardEscrow) error {
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

	// Wrap all payments in a cache context. If any SendCoins call fails, commit() is not
	// called, so no coins are transferred. The escrow remains in state and will be retried
	// on the next pruning epoch.
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cacheCtx, commit := sdkCtx.CacheContext()

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
		err = k.BankKeeper.SendCoinsFromModuleToAccount(cacheCtx, types.ModuleName, recipient, coins, "devshard_escrow_unsettled_distribution")
		if err != nil {
			k.LogError("failed to distribute unsettled escrow funds", types.Pruning,
				"escrow_id", escrow.Id, "address", addr, "error", err)
			return fmt.Errorf("failed to send coins to %s for escrow %d: %w", addr, escrow.Id, err)
		}
	}

	commit()
	return nil
}
