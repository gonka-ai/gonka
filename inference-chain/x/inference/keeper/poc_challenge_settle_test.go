package keeper_test

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const challengeSettleEpoch = uint64(10)

func setupChallengeSettle(t *testing.T, targetStatus types.ParticipantStatus) (
	inferencekeeper.Keeper,
	sdk.Context,
	keepertest.InferenceMocks,
) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.BitcoinRewardParams.InitialEpochReward = 10000
	params.BitcoinRewardParams.GenesisEpoch = challengeSettleEpoch
	require.NoError(t, k.SetParams(ctx, params))

	target := types.Participant{
		Index:       testutil.Executor,
		Address:     testutil.Executor,
		CoinBalance: 1000,
		Status:      targetStatus,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
		},
	}
	other := types.Participant{
		Index:       testutil.Executor2,
		Address:     testutil.Executor2,
		CoinBalance: 1000,
		Status:      types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 100,
		},
	}
	require.NoError(t, k.SetParticipant(ctx, target))
	require.NoError(t, k.SetParticipant(ctx, other))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: challengeSettleEpoch,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: target.Address, Weight: 1000, ConfirmationWeight: 1000},
			{MemberAddress: other.Address, Weight: 1000, ConfirmationWeight: 1000},
		},
	})
	k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: challengeSettleEpoch,
		Participants: []*types.ActiveParticipant{
			{Index: target.Address},
			{Index: other.Address},
		},
	})
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           challengeSettleEpoch,
		Challenger:           testutil.Creator,
		Target:               testutil.Executor,
		ExpectedReward:       100,
		LockedPayment:        10,
		GenerationEndHeight:  50,
		FailReason:           types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 10,
			SealHeight:               40,
			Outcome:                  types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PASSED,
		}},
	}))
	return k, ctx, mocks
}

func TestSettleAccounts_PassVestsLockedPayment(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_ACTIVE)
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	payout, err := types.GetCoins(10)
	require.NoError(t, err)
	target, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, target, payout, "poc_challenge_pass").Return(nil)

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.NoError(t, err)

	settle, found := k.GetSettleAmount(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, uint64(1000), settle.WorkCoins)
	require.Equal(t, uint64(5000), settle.RewardCoins)
	_, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSettleAccounts_RejectedPaysCompensationAndReducesLeftover(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_INACTIVE)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           challengeSettleEpoch,
		Challenger:           testutil.Creator,
		Target:               testutil.Executor,
		ExpectedReward:       100,
		LockedPayment:        10,
		GenerationEndHeight:  50,
		FailReason:           types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED,
	}))
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	refund, err := types.GetCoins(10)
	require.NoError(t, err)
	comp, err := types.GetCoins(100)
	require.NoError(t, err)
	leftover, err := types.GetCoins(4900)
	require.NoError(t, err)
	challenger, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, challenger, refund, "poc_challenge_refund").Return(nil)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, challenger, comp, "poc_challenge_compensation").Return(nil)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", leftover, gomock.Any()).Return(nil)

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.NoError(t, err)
	other, found := k.GetSettleAmount(ctx, testutil.Executor2)
	require.True(t, found)
	require.Equal(t, uint64(5000), other.RewardCoins)
	_, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSettleAccounts_NoVoteRefundsOnly(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_INACTIVE)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:           challengeSettleEpoch,
		Challenger:           testutil.Creator,
		Target:               testutil.Executor,
		ExpectedReward:       100,
		LockedPayment:        10,
		GenerationEndHeight:  50,
		FailReason:           types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE,
	}))
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	refund, err := types.GetCoins(10)
	require.NoError(t, err)
	leftover, err := types.GetCoins(5000)
	require.NoError(t, err)
	challenger, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, challenger, refund, "poc_challenge_refund").Return(nil)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", leftover, gomock.Any()).Return(nil)

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.NoError(t, err)
	_, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSettleAccounts_PayoutErrorRollsBackSettlement(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_ACTIVE)
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("payout failed"))

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.Error(t, err)
	updated, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, int64(1000), updated.CoinBalance)
	_, found = k.GetSettleAmount(ctx, testutil.Executor)
	require.False(t, found)
	_, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
}

func TestSettleAccounts_ActiveCompensableFailSkipsBounty(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_ACTIVE)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:          challengeSettleEpoch,
		Challenger:          testutil.Creator,
		Target:              testutil.Executor,
		ExpectedReward:      100,
		LockedPayment:       10,
		GenerationEndHeight: 50,
		FailReason:          types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT,
	}))
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	refund, err := types.GetCoins(10)
	require.NoError(t, err)
	challenger, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, challenger, refund, "poc_challenge_refund").Return(nil)

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.NoError(t, err)
	settle, found := k.GetSettleAmount(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, uint64(5000), settle.RewardCoins)
	_, found, err = k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSettleAccounts_LeavesEarlierEpochChallenge(t *testing.T) {
	k, ctx, mocks := setupChallengeSettle(t, types.ParticipantStatus_INACTIVE)
	require.NoError(t, k.PoCChallenge.Set(ctx, types.PoCChallenge{
		EpochIndex:          challengeSettleEpoch - 1,
		Challenger:          testutil.Creator,
		Target:              testutil.Executor,
		ExpectedReward:      100,
		LockedPayment:       10,
		GenerationEndHeight: 50,
		FailReason:          types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED,
	}))
	mint, err := types.GetCoins(10000)
	require.NoError(t, err)
	leftover, err := types.GetCoins(5000)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, mint, gomock.Any()).Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), "inference", "gov", leftover, gomock.Any()).Return(nil)

	_, err = k.SettleAccounts(ctx, challengeSettleEpoch, 0)
	require.NoError(t, err)
	_, found, err := k.PoCChallenge.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
}
