package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) BackfillEpochTrustWeights(ctx context.Context) (int, error) {
	currentEpoch, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return 0, nil
	}
	epochs := []uint64{currentEpoch + 1, currentEpoch}
	if currentEpoch >= 1 {
		epochs = append(epochs, currentEpoch-1)
	}
	filled := 0
	for _, epochIndex := range epochs {
		aps, found := k.GetActiveParticipants(ctx, epochIndex)
		if !found {
			continue
		}
		aps.EpochId = epochIndex
		if err := k.SetEpochTrustWeights(ctx, aps); err != nil {
			k.LogError("Bridge trust backfill: epoch skipped, votes fall back to the snapshot", types.Upgrades,
				"epochIndex", epochIndex, "error", err)
			continue
		}
		filled++
	}
	return filled, nil
}

type BridgeTrustMigrationResult struct {
	Recomputed int
	Completed  int
}

func (k Keeper) MigrateBridgeTrustPower(ctx sdk.Context) (BridgeTrustMigrationResult, error) {
	var result BridgeTrustMigrationResult
	iter, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
	if err != nil {
		return result, err
	}
	var records []types.BridgeTransaction
	for ; iter.Valid(); iter.Next() {
		tx, err := iter.Value()
		if err != nil {
			iter.Close()
			return result, err
		}
		records = append(records, tx)
	}
	iter.Close()

	completionFloor := uint64(0)
	if currentEpoch, found := k.GetEffectiveEpochIndex(ctx); found && currentEpoch >= 1 {
		completionFloor = currentEpoch - 1
	}

	trustByEpoch := make(map[uint64]*EpochTrust)
	for i := range records {
		tx := &records[i]
		trust, ok := trustByEpoch[tx.EpochIndex]
		if !ok {
			loaded, err := k.GetEpochTrust(ctx, tx.EpochIndex)
			if err == nil {
				trust = &loaded
			}
			trustByEpoch[tx.EpochIndex] = trust
		}
		k.hydrateBridgeTransactionValidators(ctx, tx)
		power, err := k.bridgeVotedTrustPower(ctx, trust, tx.Validators)
		if err != nil {
			return result, err
		}
		tx.TotalValidationPower = power
		k.SetBridgeTransaction(ctx, tx)
		result.Recomputed++

		if tx.Status != types.BridgeTransactionStatus_BRIDGE_PENDING || trust == nil || tx.EpochIndex < completionFloor {
			continue
		}
		if trust.Total <= 0 || power < bridgeMajorityThreshold(trust.Total) {
			continue
		}
		if k.completeMigratedBridgeTransaction(ctx, tx) {
			result.Completed++
		}
	}
	return result, nil
}

func (k Keeper) completeMigratedBridgeTransaction(ctx sdk.Context, tx *types.BridgeTransaction) bool {
	cacheCtx, write := ctx.CacheContext()
	completed := *tx
	completed.Status = types.BridgeTransactionStatus_BRIDGE_COMPLETED
	completed.Validators = nil
	k.SetBridgeTransaction(cacheCtx, &completed)
	if err := k.handleCompletedBridgeTransaction(cacheCtx, &completed); err != nil {
		k.LogError("Bridge trust migration: completion failed, record stays pending", types.Upgrades,
			"id", tx.Id, "epochIndex", tx.EpochIndex, "error", err)
		return false
	}
	write()
	k.LogInfo("Bridge trust migration: completed record that already holds a trusted majority", types.Upgrades,
		"id", tx.Id, "epochIndex", tx.EpochIndex, "power", tx.TotalValidationPower)
	return true
}

func (k Keeper) bridgeVotedTrustPower(ctx context.Context, trust *EpochTrust, voters []string) (int64, error) {
	if trust == nil {
		return 0, nil
	}
	seen := make(map[string]struct{}, len(voters))
	var total int64
	for _, voter := range voters {
		addr, err := sdk.AccAddressFromBech32(voter)
		if err != nil {
			continue
		}
		if _, dup := seen[addr.String()]; dup {
			continue
		}
		seen[addr.String()] = struct{}{}
		weight, err := k.EpochTrustWeight(ctx, *trust, addr)
		if err != nil {
			return 0, err
		}
		total += weight
	}
	return total, nil
}
