package pocchallenge

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func challengeAddrs() (challenger, target string) {
	return testutil.Creator, testutil.Executor
}

func baseParams(challenger string) types.Params {
	params := types.DefaultParams()
	params.EpochParams.EpochLength = 2000
	params.EpochParams.ConfirmationPocSafetyWindow = 50
	params.BitcoinRewardParams.InitialEpochReward = 10000
	params.PocChallengeParams = types.DefaultPoCChallengeParams()
	params.PocChallengeParams.AllowedChallengers = []string{challenger}
	params.PocChallengeParams.SliceBlocks = 500
	params.PocChallengeParams.PaymentRatio = types.DecimalFromFloat(0.1)
	return params
}

func baseChain(t *testing.T, height int64) (*fakeChain, sdk.Context, *Store) {
	t.Helper()
	challenger, target := challengeAddrs()
	store, ctx := newTestStore(t)
	_ = height
	params := baseParams(challenger)
	chain := &fakeChain{
		params: params,
		participants: map[string]types.Participant{
			target: {Index: target, Address: target, Status: types.ParticipantStatus_ACTIVE},
		},
		live: map[string]bool{target: true, testutil.Validator: true},
		root: types.EpochGroupData{
			EpochIndex: 2,
			ValidationWeights: []*types.ValidationWeight{
				{MemberAddress: target, Weight: 100},
				{MemberAddress: testutil.Validator, Weight: 900},
			},
		},
		groups: []types.EpochGroupData{{
			EpochIndex: 2,
			ModelId:    "m1",
			ValidationWeights: []*types.ValidationWeight{{
				MemberAddress: target,
				Weight:        100,
				MlNodes:       []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 10}},
			}},
		}},
		epoch:    &types.Epoch{Index: 2, PocStartBlockHeight: 0},
		haveSnap: true,
		snapshot: types.PoCValidationSnapshot{
			ModelVotingPowers: []*types.ModelVotingPowers{{
				ModelId: "m1",
				VotingPowers: []*types.VotingPowerEntry{{
					Address:     testutil.Validator,
					VotingPower: 900,
				}},
			}},
		},
	}
	return chain, ctx, store
}

func TestCreate_RejectsEmptyWhitelist(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.params.PocChallengeParams.AllowedChallengers = nil
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeNotAllowed)
}

func TestCreate_RejectsSelfTarget(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Creator,
	})
	require.Error(t, err)
}

func TestCreate_RejectsDuringConfirmationPoC(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.eventActive = true
	chain.event = &types.ConfirmationPoCEvent{
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		GenerationStartHeight: 80,
	}
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeWindow)
}

func TestCreate_RejectsUntilValidationEnd(t *testing.T) {
	chain, ctx, store := baseChain(t, 98)
	chain.eventActive = true
	chain.event = &types.ConfirmationPoCEvent{
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
		GenerationStartHeight: 80,
	}
	ctx = ctx.WithBlockHeight(98)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeWindow)
}

func TestCreate_AllowsAfterValidationEnd(t *testing.T) {
	chain, ctx, store := baseChain(t, 99)
	chain.eventActive = true
	chain.event = &types.ConfirmationPoCEvent{
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
		GenerationStartHeight: 80,
	}
	ctx = ctx.WithBlockHeight(99)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
}

func TestCreate_RejectsSafetyWindow(t *testing.T) {
	chain, ctx, store := baseChain(t, 1960)
	ctx = ctx.WithBlockHeight(1960)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeWindow)
}

func TestCreate_LocksPaymentAndMath(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(101), ch.ChallengeStartHeight)
	// E = floor((100/1000)*10000 * (1950-100)/(1950-19)) = floor(1000 * 1850/1931) = 958
	require.Equal(t, uint64(958), ch.ExpectedReward)
	require.Equal(t, uint64(95), ch.LockedPayment)
	require.True(t, store.IsChallengeGenerating(ctx, testutil.Executor))
	require.Contains(t, chain.sends, "lock:poc_challenge_lock")
}

func TestCreate_RejectsAlreadyOpen(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	_, err = Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrPoCChallengeAlreadyOpen)
}

func TestStoreCommit_CurrentSliceOnly(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)

	ctx = ctx.WithBlockHeight(101)
	root := make([]byte, 32)
	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               1,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    10,
			RootHash: root,
		}},
	})
	require.Error(t, err)

	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               0,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    10,
			RootHash: root,
		}},
	})
	require.NoError(t, err)
}

func TestStoreCommit_AfterSealLastSliceUntilVote(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	root := make([]byte, 32)
	ctx = ctx.WithBlockHeight(101)
	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               0,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    10,
			RootHash: root,
		}},
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 901, 500))

	ctx = ctx.WithBlockHeight(902)
	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               0,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    10,
			RootHash: root,
		}},
	})
	require.Error(t, err)

	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               1,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    10,
			RootHash: root,
		}},
	})
	require.NoError(t, err)

	_, err = SubmitValidations(ctx, chain, store, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 101,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            "m1",
			ValidatedWeight:    10,
		}},
	})
	require.NoError(t, err)

	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               1,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m1",
			Count:    11,
			RootHash: root,
		}},
	})
	require.Error(t, err)
}

func TestSealLeavesGeneratingUntilSafetyWindow(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 500, 500))
	require.True(t, store.IsChallengeGenerating(ctx, testutil.Executor))

	require.NoError(t, LeaveGenerating(ctx, store, testutil.Executor, 1950))
	require.False(t, store.IsChallengeGenerating(ctx, testutil.Executor))
	require.True(t, store.HasOpenChallenge(ctx, testutil.Executor))
}

func TestEvaluateSkipWhenNoVotesKeepsPending(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 901, 500))
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	seg := store.OpenSegment(ch)
	require.Nil(t, seg)
	pending := store.SegmentByStart(ch, 101)
	require.NotNil(t, pending)
	require.Equal(t, types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING, pending.Outcome)
	require.Equal(t, int64(0), pending.FirstVoteHeight)
}

func TestPay_PassVestsLockedPayment(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 200, 500))
	require.NoError(t, LeaveGenerating(ctx, store, testutil.Executor, 1950))
	chain.summaries = map[string]types.EpochPerformanceSummary{
		testutil.Executor: {EpochIndex: 2, ParticipantId: testutil.Executor, RewardedCoins: 0},
	}
	require.NoError(t, SettleChallengePayments(ctx, chain, store, 2))
	require.Contains(t, chain.sends, "pay:poc_challenge_pass")
	_, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPay_FailRefundsAndCompensation(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, FailChallenge(ctx, store, testutil.Executor, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT, 500))
	chain.summaries = map[string]types.EpochPerformanceSummary{
		testutil.Executor: {EpochIndex: 2, ParticipantId: testutil.Executor, RewardedCoins: 10},
	}
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	ch.UnpaidRewardShare = 200
	require.NoError(t, store.Set(ctx, ch))
	require.NoError(t, SettleChallengePayments(ctx, chain, store, 2))
	require.Contains(t, chain.sends, "refund:poc_challenge_refund")
	require.Contains(t, chain.sends, "comp:poc_challenge_compensation")
	require.Contains(t, chain.sends, "pay:poc_challenge_compensation")
}

func TestPay_SkipsWhenSettleSummaryMissing(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 200, 500))
	require.NoError(t, LeaveGenerating(ctx, store, testutil.Executor, 1950))
	require.NoError(t, SettleChallengePayments(ctx, chain, store, 2))
	_, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
}

func TestWaiveDevshardMissesWhileGenerating(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		Target:               testutil.Executor,
		ChallengeStartHeight: 50,
		GenerationEndHeight:  0,
	}))
	hs, assigned := WaiveDevshardMissesWhileGenerating(store, ctx, types.DevshardEscrow{
		CreateBlockHeight: 40,
	}, types.DevshardSettlementHostStats{Missed: 7}, 10, 80, testutil.Executor)
	require.Equal(t, uint32(0), hs.Missed)
	require.Equal(t, uint64(3), assigned)
}

func TestIsMissedRequestWaived(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		Target:               testutil.Executor,
		ChallengeStartHeight: 50,
		GenerationEndHeight:  80,
	}))
	require.True(t, IsMissedRequestWaived(ctx, store, testutil.Executor, 79))
	require.False(t, IsMissedRequestWaived(ctx, store, testutil.Executor, 80))
	require.False(t, IsMissedRequestWaived(ctx, store, testutil.Executor, 10))
}

func TestVotesRejectedUntilCountedCommitsExist(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 901, 500))
	ctx = ctx.WithBlockHeight(902)
	_, err = SubmitValidations(ctx, chain, store, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 101,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            "m1",
			ValidatedWeight:    10,
		}},
	})
	require.Error(t, err)
}

func TestVotesSkipWhenFailReasonSet(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, FailChallenge(ctx, store, testutil.Executor, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL, 200))
	_, err = SubmitValidations(ctx, chain, store, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 101,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            "m1",
			ValidatedWeight:    10,
		}},
	})
	require.NoError(t, err)
	has, err := store.HasValidation(ctx, testutil.Executor, 101, "m1", testutil.Validator)
	require.NoError(t, err)
	require.False(t, has)
}

func TestHasRequiredCommitsPerAssignedModel(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.SetCommit(ctx, types.PoCChallengeCommit{
		Target: testutil.Executor, PocStageStartBlockHeight: 101, ModelId: "m1", SliceIndex: 0, Count: 1, RootHash: make([]byte, 32),
	}))
	counted := []SliceRange{{Index: 0}, {Index: 1}}
	ok, err := HasRequiredCommits(ctx, store, testutil.Executor, 101, counted, []string{"m1"})
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, store.SetCommit(ctx, types.PoCChallengeCommit{
		Target: testutil.Executor, PocStageStartBlockHeight: 101, ModelId: "m1", SliceIndex: 1, Count: 1, RootHash: make([]byte, 32),
	}))
	ok, err = HasRequiredCommits(ctx, store, testutil.Executor, 101, counted, []string{"m1", "m2"})
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = HasRequiredCommits(ctx, store, testutil.Executor, 101, counted, []string{"m1"})
	require.NoError(t, err)
	require.True(t, ok)
}

func TestFailReasonNotOverwritten(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		Target:              testutil.Executor,
		FailReason:          types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE,
		GenerationEndHeight: 50,
		ExpectedReward:      100,
		UnpaidRewardShare:   200,
	}))
	require.NoError(t, FailChallenge(ctx, store, testutil.Executor, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT, 80))
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE, ch.FailReason)
	require.False(t, CompensationReasons(ch.FailReason))
}

func TestPay_NoCompensationForUnrelatedRemoval(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, FailChallenge(ctx, store, testutil.Executor, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL, 200))
	chain.summaries = map[string]types.EpochPerformanceSummary{
		testutil.Executor: {EpochIndex: 2, ParticipantId: testutil.Executor, RewardedCoins: 10},
	}
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	ch.UnpaidRewardShare = 200
	require.NoError(t, store.Set(ctx, ch))
	require.NoError(t, SettleChallengePayments(ctx, chain, store, 2))
	require.Contains(t, chain.sends, "refund:poc_challenge_refund")
	require.NotContains(t, chain.sends, "comp:poc_challenge_compensation")
	require.NotContains(t, chain.sends, "pay:poc_challenge_compensation")
}

func TestStoreCommit_RejectsUnassignedModel(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	ctx = ctx.WithBlockHeight(101)
	_, err = StoreCommit(ctx, chain, store, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 101,
		SliceIndex:               0,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "m2",
			Count:    10,
			RootHash: make([]byte, 32),
		}},
	})
	require.Error(t, err)
}

func TestPay_SettlesEarlierEpoch(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, FailChallenge(ctx, store, testutil.Executor, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT, 500))
	chain.summaries = map[string]types.EpochPerformanceSummary{
		testutil.Executor: {EpochIndex: 2, ParticipantId: testutil.Executor, RewardedCoins: 0},
	}
	require.NoError(t, SettleChallengePayments(ctx, chain, store, 3))
	require.Contains(t, chain.sends, "refund:poc_challenge_refund")
	_, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.False(t, found)
}

func TestHandleEndBlock_SealsAfterSafetyHeight(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	ctx = ctx.WithBlockHeight(1960)
	require.NoError(t, HandleEndBlock(ctx, chain, store))
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1950), ch.Segments[0].SealHeight)
	require.Equal(t, int64(1950), ch.GenerationEndHeight)
}

func TestCreate_RejectsMaintenanceBeforeEpochSwitch(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.maintState = map[string]types.MaintenanceState{
		testutil.Executor: {ScheduledReservationId: 7},
	}
	chain.reservations = map[uint64]types.MaintenanceReservation{
		7: {StartHeight: 2000},
	}
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.Error(t, err)
}

func TestDeleteSkipSetRemovesTriggerPrefix(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		Target:              testutil.Executor,
		GenerationEndHeight: 0,
	}))
	_, err := store.SnapshotSkip(ctx, 70)
	require.NoError(t, err)
	set, err := store.ConfirmationEvaluationSkipSet(ctx, 70)
	require.NoError(t, err)
	require.Contains(t, set, testutil.Executor)
	require.NoError(t, store.DeleteSkipSet(ctx, 70))
	set, err = store.ConfirmationEvaluationSkipSet(ctx, 70)
	require.NoError(t, err)
	require.Empty(t, set)
}

func TestCreate_RejectsZeroAssignedPocWeight(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.groups[0].ValidationWeights[0].MlNodes[0].PocWeight = 0
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.ErrorIs(t, err, types.ErrIllegalState)
	require.Contains(t, err.Error(), "confirmation-weight hardware")
}

func TestSubmitValidations_SkipsIneligibleLiveVoter(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.live[testutil.Validator2] = true
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 901, 500))
	ctx = ctx.WithBlockHeight(902)
	_, err = SubmitValidations(ctx, chain, store, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator2,
		PocStageStartBlockHeight: 101,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            "m1",
			ValidatedWeight:    10,
		}},
	})
	require.NoError(t, err)
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(0), ch.Segments[0].FirstVoteHeight)
	has, err := store.HasValidation(ctx, testutil.Executor, 101, "m1", testutil.Validator2)
	require.NoError(t, err)
	require.False(t, has)
}

func TestSubmitValidations_GuardianSkippedWithoutSnapshot(t *testing.T) {
	chain, ctx, store := baseChain(t, 100)
	chain.haveSnap = false
	chain.guardianEnabled = true
	chain.guardians = []string{testutil.Validator}
	ctx = ctx.WithBlockHeight(100)
	_, err := Create(ctx, chain, store, &types.MsgCreatePoCChallenge{
		Creator: testutil.Creator,
		Target:  testutil.Executor,
	})
	require.NoError(t, err)
	require.NoError(t, SealOpenSegment(ctx, store, testutil.Executor, 901, 500))
	ctx = ctx.WithBlockHeight(902)
	_, err = SubmitValidations(ctx, chain, store, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 101,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: testutil.Executor,
			ModelId:            "m1",
			ValidatedWeight:    10,
		}},
	})
	require.NoError(t, err)
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(0), ch.Segments[0].FirstVoteHeight)
	has, err := store.HasValidation(ctx, testutil.Executor, 101, "m1", testutil.Validator)
	require.NoError(t, err)
	require.False(t, has)
}

func TestFailUnrelatedLeave_SkipsOtherEpoch(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		EpochIndex:           1,
		Target:               testutil.Executor,
		ChallengeStartHeight: 10,
	}))
	require.NoError(t, FailUnrelatedLeave(ctx, store, testutil.Executor, 200, 2, true))
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET, ch.FailReason)
}

func TestFailUnrelatedLeave_SkipsReadyToPay(t *testing.T) {
	_, ctx, store := baseChain(t, 100)
	require.NoError(t, store.Set(ctx, types.PoCChallenge{
		EpochIndex:           2,
		Target:               testutil.Executor,
		ChallengeStartHeight: 10,
		GenerationEndHeight:  50,
		Segments: []*types.PoCChallengeSegment{{
			PocStageStartBlockHeight: 10,
			SealHeight:               40,
			Outcome:                  types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PASSED,
		}},
	}))
	require.NoError(t, FailUnrelatedLeave(ctx, store, testutil.Executor, 200, 2, true))
	ch, found, err := store.Get(ctx, testutil.Executor)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET, ch.FailReason)
}
