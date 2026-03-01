package keeper

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestZeroParticipationNoReward verifies that a participant with weight > 0
// but InferenceCount=0 and MissedRequests=0 does NOT receive epoch rewards.
//
// Before this fix, CheckAndPunishForDowntime returned the full reward when
// total==0, allowing participants to maintain PoC weight (via preserved
// POC_SLOT nodes) while doing zero inference work and collecting subsidy
// rewards every epoch.
func TestZeroParticipationNoReward(t *testing.T) {
	bitcoinParams := &types.BitcoinRewardParams{
		InitialEpochReward: 285000000000000,
		DecayRate:          types.DecimalFromFloat(-0.000475),
		GenesisEpoch:       1,
	}

	epochGroupData := &types.EpochGroupData{
		EpochIndex: 100,
		ValidationWeights: []*types.ValidationWeight{
			createTestValidationWeight("honest_worker", 500, 100),
			createTestValidationWeight("free_rider", 500, 100),
		},
	}

	participants := []types.Participant{
		{
			Address:     "honest_worker",
			CoinBalance: 1000,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 5,
			},
		},
		{
			Address:     "free_rider",
			CoinBalance: 0,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 0,
				MissedRequests: 0,
			},
		},
	}

	logger := createTestLogger(t)

	results, _, err := CalculateParticipantBitcoinRewards(
		participants, epochGroupData, bitcoinParams, nil, nil, logger,
	)
	require.NoError(t, err)
	require.Equal(t, 2, len(results))

	honestReward := results[0].Settle.RewardCoins
	freeRiderReward := results[1].Settle.RewardCoins

	t.Logf("Honest worker RewardCoins: %d", honestReward)
	t.Logf("Free rider RewardCoins:    %d", freeRiderReward)

	require.Greater(t, honestReward, uint64(0), "honest worker should get rewards")
	require.Equal(t, uint64(0), freeRiderReward,
		"participant with zero participation should not receive rewards")
}

func TestCheckAndPunishForDowntime_ZeroTotal(t *testing.T) {
	p0 := &types.Decimal{Value: 100, Exponent: -3} // 0.10

	result := CheckAndPunishForDowntime(0, 0, 1000, p0)
	require.Equal(t, uint64(0), result,
		"zero participation should return zero reward")

	result = CheckAndPunishForDowntime(100, 50, 1000, p0)
	require.Equal(t, uint64(0), result,
		"50% miss rate should be punished")

	result = CheckAndPunishForDowntime(100, 5, 1000, p0)
	require.Equal(t, uint64(1000), result,
		"5% miss rate should pass with p0=0.10")
}
