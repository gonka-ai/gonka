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

func hostStatsParticipant(seed byte) sdk.AccAddress {
	addr := sdk.AccAddress(make([]byte, 20))
	addr[0] = seed
	return addr
}


func TestAggregateSubnetHostStats_MissedOverflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x01)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  epochIndex,
		Missed:      math.MaxUint32,
	})
	require.NoError(t, err)

	err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{Missed: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missed overflow")
}

func TestAggregateSubnetHostStats_InvalidOverflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x02)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  epochIndex,
		Invalid:     math.MaxUint32,
	})
	require.NoError(t, err)

	err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{Invalid: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid overflow")
}

func TestAggregateSubnetHostStats_RequiredValidationsOverflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x03)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant:         participant.String(),
		EpochIndex:          epochIndex,
		RequiredValidations: math.MaxUint32,
	})
	require.NoError(t, err)

	err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{RequiredValidations: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required_validations overflow")
}

func TestAggregateSubnetHostStats_CompletedValidationsOverflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x04)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant:          participant.String(),
		EpochIndex:           epochIndex,
		CompletedValidations: math.MaxUint32,
	})
	require.NoError(t, err)

	err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{CompletedValidations: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "completed_validations overflow")
}

func TestIncrementSubnetHostEscrowCount_Overflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x05)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  epochIndex,
		EscrowCount: math.MaxUint32,
	})
	require.NoError(t, err)

	err = k.IncrementSubnetHostEscrowCount(ctx, epochIndex, participant)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escrow count overflow")
}

func TestAggregateSubnetHostStats_CostOverflow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x06)
	epochIndex := uint64(1)

	err := k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(epochIndex, participant), types.SubnetHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  epochIndex,
		Cost:        math.MaxUint64,
	})
	require.NoError(t, err)

	err = k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{Cost: 1})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cost overflow")
}

func TestAggregateSubnetHostStats_HappyPath(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	participant := hostStatsParticipant(0x07)
	epochIndex := uint64(1)

	err := k.AggregateSubnetHostStats(ctx, epochIndex, participant, types.SubnetSettlementHostStats{
		Missed:               10,
		Invalid:              5,
		Cost:                 1_000_000_000,
		RequiredValidations:  100,
		CompletedValidations: 95,
	})
	require.NoError(t, err)

	stats, found := k.GetSubnetHostEpochStats(ctx, epochIndex, participant)
	require.True(t, found)
	require.Equal(t, uint32(10), stats.Missed)
	require.Equal(t, uint32(5), stats.Invalid)
	require.Equal(t, uint64(1_000_000_000), stats.Cost)
	require.Equal(t, uint32(100), stats.RequiredValidations)
	require.Equal(t, uint32(95), stats.CompletedValidations)
}
