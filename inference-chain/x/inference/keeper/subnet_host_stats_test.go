package keeper_test

import (
	"math"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestAggregateSubnetHostStats_OverflowProtection(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	participant := sdk.AccAddress(make([]byte, 20))
	participant[0] = 0x01
	epochIndex := uint64(5)
	key := collections.Join(epochIndex, participant)

	t.Run("Missed overflow", func(t *testing.T) {
		// Seed existing stats with Missed near max uint32
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
			Missed:      math.MaxUint32 - 1,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			Missed: 2, // would overflow: (MaxUint32-1) + 2 > MaxUint32
		})
		require.Error(t, err, "Missed overflow must be detected")
		require.Contains(t, err.Error(), "overflow")
	})

	t.Run("Invalid overflow", func(t *testing.T) {
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
			Invalid:     math.MaxUint32 - 1,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			Invalid: 2,
		})
		require.Error(t, err, "Invalid overflow must be detected")
		require.Contains(t, err.Error(), "overflow")
	})

	t.Run("RequiredValidations overflow", func(t *testing.T) {
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant:         participant.String(),
			EpochIndex:          epochIndex,
			RequiredValidations: math.MaxUint32 - 1,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			RequiredValidations: 2,
		})
		require.Error(t, err, "RequiredValidations overflow must be detected")
		require.Contains(t, err.Error(), "overflow")
	})

	t.Run("CompletedValidations overflow", func(t *testing.T) {
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant:          participant.String(),
			EpochIndex:           epochIndex,
			CompletedValidations: math.MaxUint32 - 1,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			CompletedValidations: 2,
		})
		require.Error(t, err, "CompletedValidations overflow must be detected")
		require.Contains(t, err.Error(), "overflow")
	})

	t.Run("Cost overflow still works", func(t *testing.T) {
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
			Cost:        math.MaxUint64 - 1,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			Cost: 2,
		})
		require.Error(t, err, "Cost overflow must be detected")
		require.Contains(t, err.Error(), "overflow")
	})

	t.Run("No overflow when within bounds", func(t *testing.T) {
		err := k.SubnetHostEpochStatsMap.Set(ctx, key, types.SubnetHostEpochStats{
			Participant:          participant.String(),
			EpochIndex:           epochIndex,
			Missed:               100,
			Invalid:              50,
			Cost:                 1000,
			RequiredValidations:  200,
			CompletedValidations: 150,
		})
		require.NoError(t, err)

		err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
			Missed:               10,
			Invalid:              5,
			Cost:                 100,
			RequiredValidations:  20,
			CompletedValidations: 15,
		})
		require.NoError(t, err, "aggregation within bounds must succeed")
	})
}
