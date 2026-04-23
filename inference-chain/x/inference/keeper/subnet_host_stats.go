package keeper

import (
	"context"
	"fmt"
	"math"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetSubnetHostEpochStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) (types.SubnetHostEpochStats, bool) {
	v, err := k.SubnetHostEpochStatsMap.Get(ctx, collections.Join(epochIndex, participant))
	if err != nil {
		return types.SubnetHostEpochStats{}, false
	}
	return v, true
}

func (k Keeper) AggregateSubnetHostStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress, slotStats types.SubnetSettlementHostStats) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.SubnetHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.Missed += slotStats.Missed
	existing.Invalid += slotStats.Invalid
	if existing.Cost > math.MaxUint64-slotStats.Cost {
		return fmt.Errorf("cost overflow aggregating subnet host stats")
	}
	existing.Cost += slotStats.Cost
	existing.RequiredValidations += slotStats.RequiredValidations
	existing.CompletedValidations += slotStats.CompletedValidations
	return k.SubnetHostEpochStatsMap.Set(ctx, key, existing)
}

func (k Keeper) IncrementSubnetHostEscrowCount(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.SubnetHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.EscrowCount++
	return k.SubnetHostEpochStatsMap.Set(ctx, key, existing)
}
