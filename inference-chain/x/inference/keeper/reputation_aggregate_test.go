package keeper_test

import (
	"math/rand"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestReputationMissTotals_MatchesAllSummaries(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	participant := sdk.AccAddress([]byte("reputation_member__")).String()
	r := rand.New(rand.NewSource(7))
	params := types.DefaultParams().ValidationParams

	fromAll := func() int64 {
		summaries := k.GetEpochPerformanceSummariesByParticipant(ctx, participant)
		missRates := make([]decimal.Decimal, 0, len(summaries))
		for _, s := range summaries {
			missRates = append(missRates, calculations.EpochMissRate(s.InferenceCount, s.MissedRequests))
		}
		got := calculations.CalculateReputation(&calculations.ReputationContext{
			EpochCount:           int64(len(summaries)),
			EpochMissPercentages: missRates,
			ValidationParams:     params,
		})
		require.Equal(t, perEpochReputation(int64(len(summaries)), missRates, params), got)
		return got
	}

	epoch := uint64(0)
	for round := 0; round < 40; round++ {
		for i := r.Intn(4); i > 0; i-- {
			epoch += uint64(1 + r.Intn(3))
			require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
				EpochIndex:     epoch,
				ParticipantId:  participant,
				InferenceCount: uint64(r.Intn(40)),
				MissedRequests: uint64(r.Intn(8)),
			}))
		}
		if round == 20 {
			params.MissPercentageCutoff = types.DecimalFromFloat(0.2)
		}
		count, sum, err := k.ReputationMissTotals(ctx, participant, params.MissPercentageCutoff.ToDecimal())
		require.NoError(t, err)
		require.Equal(t, len(k.GetEpochPerformanceSummariesByParticipant(ctx, participant)), int(count))
		require.Equal(t, fromAll(), calculations.CalculateReputationFromMissSum(count, sum, params), "round %d", round)
	}
}

// After the first call, epoch formation reads only the summaries added since.
func TestReputationMissTotals_ReadsOnlyNewSummaries(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	participant := sdk.AccAddress([]byte("reputation_member__")).String()
	cutoff := types.DefaultParams().ValidationParams.MissPercentageCutoff.ToDecimal()
	for epoch := uint64(1); epoch <= 400; epoch++ {
		require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
			EpochIndex: epoch, ParticipantId: participant, InferenceCount: 100, MissedRequests: epoch % 3,
		}))
	}
	gasOf := func() uint64 {
		before := ctx.GasMeter().GasConsumed()
		_, _, err := k.ReputationMissTotals(ctx, participant, cutoff)
		require.NoError(t, err)
		return ctx.GasMeter().GasConsumed() - before
	}
	first := gasOf()
	require.NoError(t, k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
		EpochIndex: 401, ParticipantId: participant, InferenceCount: 100, MissedRequests: 5,
	}))
	next := gasOf()
	t.Logf("gas: first %d, next epoch %d", first, next)
	require.Less(t, next, first/10)
}

// perEpochReputation is the reputation formula before the aggregate: a miss cost per epoch.
func perEpochReputation(epochCount int64, missRates []decimal.Decimal, params *types.ValidationParams) int64 {
	epochsToMax := decimal.NewFromInt(params.EpochsToMax)
	cutoff := params.MissPercentageCutoff.ToDecimal()
	penalty := params.MissRequestsPenalty.ToDecimal()
	singleEpochValue := decimal.NewFromInt(1).Div(epochsToMax)
	missCost := decimal.Zero
	for _, m := range missRates {
		if m.GreaterThan(cutoff) {
			missCost = missCost.Add(m.Mul(singleEpochValue).Mul(penalty))
		}
	}
	actual := decimal.NewFromInt(epochCount).Sub(missCost.Mul(epochsToMax))
	if actual.GreaterThan(epochsToMax) {
		return 100
	}
	if actual.LessThanOrEqual(decimal.Zero) {
		return 0
	}
	return actual.Div(epochsToMax).Truncate(2).Mul(decimal.NewFromInt(100)).IntPart()
}
