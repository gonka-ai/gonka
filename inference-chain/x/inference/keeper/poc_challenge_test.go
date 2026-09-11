package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestRemoveFromEpochGroupsMarksUnrelatedLeave(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ctx = ctx.WithBlockHeight(200)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 10,
	}))
	err := k.RemoveFromEpochGroupsForTesting(ctx, &types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}, calculations.Downtime)
	require.Error(t, err) // no current epoch group in this fixture
	ch, found, getErr := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, getErr)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL, ch.FailReason)
	require.Equal(t, int64(200), ch.GenerationEndHeight)
}

func TestRemoveFromEpochGroupsDoesNotOverwriteChallengeFail(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 10,
		FailReason:           types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED,
		GenerationEndHeight:  50,
	}))
	_ = k.RemoveFromEpochGroupsForTesting(ctx, &types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}, calculations.FailedConfirmationPoC)
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED, ch.FailReason)
	require.Equal(t, int64(50), ch.GenerationEndHeight)
}

func TestIsMissedRequestWaivedDelegatesToStore(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		Target:               testutil.Executor,
		ChallengeStartHeight: 10,
		GenerationEndHeight:  0,
	}))
	require.True(t, k.IsMissedRequestWaived(ctx, testutil.Executor, 20))
	require.True(t, k.IsChallengeGenerating(ctx, testutil.Executor))
}

func TestHandleEndBlockFillsSeedAndSealsAtSafetyWindow(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.EpochParams.ConfirmationPocSafetyWindow = 50
	require.NoError(t, k.SetParams(ctx, params))
	k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 0})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))

	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 100,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 100,
		}},
	}))

	ctx = ctx.WithBlockHeight(100)
	require.NoError(t, pocchallenge.HandleEndBlock(ctx, &k, k.PoCChallenge))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, ch.Segments[0].SeedHash)
	require.True(t, k.IsChallengeGenerating(ctx, testutil.Executor))

	ctx = ctx.WithBlockHeight(1950)
	require.NoError(t, pocchallenge.HandleEndBlock(ctx, &k, k.PoCChallenge))
	ch, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1950), ch.Segments[0].SealHeight)
	require.Equal(t, int64(1950), ch.GenerationEndHeight)
	require.False(t, k.IsChallengeGenerating(ctx, testutil.Executor))
}

func TestFreezeChallengeUnpaidRewardShares_NoOpWithoutCompensation(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:        2,
		Target:            testutil.Executor,
		FailReason:        types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE,
		UnpaidRewardShare: 100,
	}))
	require.NoError(t, k.FreezeChallengeUnpaidRewardShares(ctx, 2))
	ch, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(100), ch.UnpaidRewardShare)
}
