package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/header"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const challengeTestModel = "challenge-model"

func challengeEpochParams() *types.EpochParams {
	params := types.DefaultEpochParams()
	params.EpochLength = 2000
	params.ConfirmationPocSafetyWindow = 50
	params.PocStageDuration = 400
	params.PocExchangeDuration = 2
	params.PocValidationDelay = 2
	params.PocValidationDuration = 6
	params.SetNewValidatorsDelay = 1
	return params
}

func setupChallengeCreate(t *testing.T, height int64) (keeper.Keeper, sdk.Context, keepertest.InferenceMocks) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ctx = ctx.WithBlockHeight(height).WithHeaderInfo(header.Info{
		Height: height,
		Hash:   []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1, 2},
	})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams = challengeEpochParams()
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.7)
	params.BitcoinRewardParams.InitialEpochReward = 10000
	params.BitcoinRewardParams.GenesisEpoch = 2
	params.PocChallengeParams = types.DefaultPoCChallengeParams()
	params.PocChallengeParams.AllowedChallengers = []string{testutil.Creator}
	params.PocChallengeParams.PaymentRatio = types.DecimalFromFloat(0.1)
	params.PocParams.PocV2Enabled = true
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 0}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   2,
		EpochGroupId: 7,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Executor, Weight: 100},
			{MemberAddress: testutil.Validator, Weight: 900},
		},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
		Status:  types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
		Status:  types.ParticipantStatus_ACTIVE,
	}))
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(&group.QueryGroupMembersResponse{
		Members: []*group.GroupMember{{
			Member: &group.Member{Address: testutil.Executor, Weight: "100"},
		}},
	}, nil).AnyTimes()
	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	return k, ctx, mocks
}

func TestCreatePoCChallenge_WritesSeedAndLocksPayment(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 500)
	_, err := k.CreatePoCChallenge(ctx, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(2), ch.EpochIndex)
	require.Equal(t, int64(501), ch.StartHeight)
	require.Equal(t, sdk.UnwrapSDKContext(ctx).HeaderInfo().Hash, ch.Seed)
	require.Greater(t, ch.ExpectedReward, uint64(0))
	require.Greater(t, ch.LockedPayment, uint64(0))
	require.Equal(t, ch.ExpectedReward/10, ch.LockedPayment)
	require.Equal(t, types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET, ch.FailureKind)
	require.True(t, k.IsChallengeGenerating(ctx, testutil.Executor))
}

func TestCreatePoCChallenge_RejectsZeroAlpha(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 500)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0)
	require.NoError(t, k.SetParams(ctx, params))
	_, err = k.CreatePoCChallenge(ctx, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeWindow)
}

func TestCreatePoCChallenge_RejectsCompletedConfirmationEvent(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 500)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex: 2,
		Phase:      types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}))
	_, err := k.CreatePoCChallenge(ctx, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeWindow)
}

func seedOpenChallenge(t *testing.T, k keeper.Keeper, ctx sdk.Context, start int64) {
	t.Helper()
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Challenger:  testutil.Creator,
		Target:      testutil.Executor,
		StartHeight: start,
		Seed:        []byte{1, 2, 3},
	}))
}

func TestPoCChallengeStoreCommit_FreezesAtFinish(t *testing.T) {
	k, ctx, mocks := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	k.SetModel(ctx, &types.Model{Id: challengeTestModel})
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	ms := keeper.NewMsgServerImpl(k)
	msg := &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:   challengeTestModel,
			Count:     4,
			RootHash:  make([]byte, 32),
			TreeDepth: 24,
		}},
	}
	_, err := ms.PoCChallengeStoreCommit(ctx, msg)
	require.NoError(t, err)

	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, uint32(4), commits[0].Count)

	finish, err := k.ChallengeFinish(ctx, types.PoCChallenge{EpochIndex: 2, StartHeight: 50})
	require.NoError(t, err)
	frozen := ctx.WithBlockHeight(finish)
	_, err = ms.PoCChallengeStoreCommit(frozen, msg)
	require.Error(t, err)
	_ = mocks
}

func TestSubmitPoCChallengeValidations_WindowsAndSelfVote(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	ms := keeper.NewMsgServerImpl(k)
	vote := &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            challengeTestModel,
			ValidatedWeight:    4,
		}},
	}

	_, err := ms.SubmitPoCChallengeValidations(ctx, vote)
	require.Error(t, err)

	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex:            2,
		GenerationStartHeight: 80,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
	}))
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	event, ok, err := k.GetActiveConfirmationPoCEvent(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	voteHeight := event.GetValidationStart(params.EpochParams)
	voteCtx := ctx.WithBlockHeight(voteHeight)
	_, err = ms.SubmitPoCChallengeValidations(voteCtx, vote)
	require.NoError(t, err)
	vals, err := k.ListChallengeValidations(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, vals, 1)
	require.Equal(t, testutil.Executor, vals[0].ValidatorParticipantAddress)

	_, err = ms.SubmitPoCChallengeValidations(voteCtx, vote)
	require.NoError(t, err)
	vals, err = k.ListChallengeValidations(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, vals, 1)
}

func TestSubmitPoCChallengeValidations_RegularWindow(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Validator), types.Participant{
		Index:   testutil.Validator,
		Address: testutil.Validator,
	}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 3, PocStartBlockHeight: 2000}))
	ms := keeper.NewMsgServerImpl(k)
	upcoming := types.NewEpochContext(types.Epoch{Index: 3, PocStartBlockHeight: 2000}, *challengeEpochParams())
	height := upcoming.StartOfPoCValidation() + 1
	voteCtx := ctx.WithBlockHeight(height)
	_, err := ms.SubmitPoCChallengeValidations(voteCtx, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 50,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            challengeTestModel,
			ValidatedWeight:    4,
		}},
	})
	require.NoError(t, err)
}

func TestRemoveFromEpochGroupsMarksAborted(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ctx = ctx.WithBlockHeight(200)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 10,
	}))
	err := k.RemoveFromEpochGroupsForTesting(ctx, &types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}, calculations.Downtime)
	require.Error(t, err)
	ch, found, getErr := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, getErr)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_ABORTED, ch.FailureKind)
}

func TestRemoveFromEpochGroupsDoesNotOverwriteChallengeFail(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 10,
		FailureKind: types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_CHALLENGE_FAILED,
	}))
	_ = k.RemoveFromEpochGroupsForTesting(ctx, &types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}, calculations.FailedConfirmationPoC)
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_CHALLENGE_FAILED, ch.FailureKind)
}

func TestIsChallengeGenerating_FalseInSafetyWindow(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 1960)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))
	require.False(t, k.IsChallengeGenerating(ctx, testutil.Executor))
	require.True(t, k.HasActiveChallengeRecord(ctx, testutil.Executor))
}

func TestPayAndDeleteOldChallenges_PassAndRefund(t *testing.T) {
	k, ctx, mocks := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:    2,
		Challenger:    testutil.Creator,
		Target:        testutil.Executor,
		LockedPayment: 10,
		StartHeight:   50,
	}))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	_, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:    2,
		Challenger:    testutil.Creator,
		Target:        testutil.Executor,
		LockedPayment: 10,
		FailureKind:   types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_CHALLENGE_FAILED,
	}))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	_, found, err = k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPayAndDeleteOldChallenges_RetriesAfterError(t *testing.T) {
	k, ctx, mocks := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:    2,
		Challenger:    testutil.Creator,
		Target:        testutil.Executor,
		LockedPayment: 10,
		FailureKind:   types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_ABORTED,
	}))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(collections.ErrNotFound).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	_, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	_, found, err = k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestWaiveDevshardMissesForActiveChallenge(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 10,
	}))
	adjusted, assigned := k.WaiveDevshardMissesForActiveChallenge(ctx, testutil.Executor, types.DevshardSettlementHostStats{
		Missed:  4,
		Invalid: 2,
	}, 10)
	require.Equal(t, uint32(0), adjusted.Missed)
	require.Equal(t, uint32(2), adjusted.Invalid)
	require.Equal(t, uint64(6), assigned)
}

func TestSameEpochChallengeTargetsIncludesFailed(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		FailureKind: types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_CHALLENGE_FAILED,
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor2,
		FailureKind: types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_ABORTED,
	}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  3,
		Target:      testutil.Creator,
		FailureKind: types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET,
	}))
	set, err := k.SameEpochChallengeTargets(ctx, 2)
	require.NoError(t, err)
	_, ok := set[testutil.Executor]
	require.True(t, ok)
	_, ok = set[testutil.Executor2]
	require.True(t, ok)
	_, ok = set[testutil.Creator]
	require.False(t, ok)
}

func TestChallengeFinish_IgnoresOtherEpochEvent(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	ch := types.PoCChallenge{EpochIndex: 2, StartHeight: 50}
	safety, err := k.ChallengeSafetyFinish(ctx, ch)
	require.NoError(t, err)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex:            3,
		GenerationStartHeight: 80,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	finish, err := k.ChallengeFinish(ctx, ch)
	require.NoError(t, err)
	require.Equal(t, safety, finish)
}

func TestPoCChallengeStoreCommit_RejectsOldEpoch(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  1,
		Target:      testutil.Executor,
		StartHeight: 50,
	}))
	k.SetModel(ctx, &types.Model{Id: challengeTestModel})
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	ms := keeper.NewMsgServerImpl(k)
	_, err := ms.PoCChallengeStoreCommit(ctx, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:   challengeTestModel,
			Count:     4,
			RootHash:  make([]byte, 32),
			TreeDepth: 24,
		}},
	})
	require.Error(t, err)
}

func TestHasActiveChallengeRecord_FalseAfterEpochFlip(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:  2,
		Target:      testutil.Executor,
		StartHeight: 10,
	}))
	require.True(t, k.HasActiveChallengeRecord(ctx, testutil.Executor))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 3))
	require.False(t, k.HasActiveChallengeRecord(ctx, testutil.Executor))
}

func TestPayAndDeleteOldChallenges_OldUnsetPassNotReevaluated(t *testing.T) {
	k, ctx, mocks := setupChallengeCreate(t, 100)
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{
		EpochIndex:    2,
		Challenger:    testutil.Creator,
		Target:        testutil.Executor,
		LockedPayment: 10,
		StartHeight:   50,
	}))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(collections.ErrNotFound).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET, ch.FailureKind)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 3))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 3, PocStartBlockHeight: 2000}))
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex:            3,
		GenerationStartHeight: 2100,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	finish, err := k.ChallengeFinish(ctx, ch)
	require.NoError(t, err)
	safety, err := k.ChallengeSafetyFinish(ctx, ch)
	require.NoError(t, err)
	require.Equal(t, safety, finish)
	require.False(t, k.HasActiveChallengeRecord(ctx, testutil.Executor))

	k.SetModel(ctx, &types.Model{Id: challengeTestModel})
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), types.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	ms := keeper.NewMsgServerImpl(k)
	_, err = ms.PoCChallengeStoreCommit(ctx, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:   challengeTestModel,
			Count:     4,
			RootHash:  make([]byte, 32),
			TreeDepth: 24,
		}},
	})
	require.Error(t, err)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	k.PayAndDeleteOldChallenges(ctx, 3)
	_, found, err = k.GetPoCChallenge(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}
