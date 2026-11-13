package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	epochModelsTrigger    int64 = 500
	epochModelsGeneration int64 = 505
)

func setupEpochModelsCommitTest(t *testing.T, epochModels ...string) (keeper.Keeper, sdk.Context, types.MsgServer) {
	t.Helper()
	k, ctx, ms := setupPoCV2StoreCommitTest(t, epochModelsGeneration+5, nil, testPoCModelID, testPoCModelID2)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex:            1,
		TriggerHeight:         epochModelsTrigger,
		GenerationStartHeight: epochModelsGeneration,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: epochModels,
		}},
	}))
	return k, ctx, ms
}

func confirmationCommit(entries ...*types.PoCV2CommitEntry) *types.MsgPoCV2StoreCommit {
	return &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: epochModelsTrigger,
		Entries:                  entries,
	}
}

func TestPoCV2StoreCommit_ConfirmationAcceptsEpochModel(t *testing.T) {
	k, ctx, ms := setupEpochModelsCommitTest(t, testPoCModelID)

	_, err := ms.PoCV2StoreCommit(ctx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID, 10, 1)))
	require.NoError(t, err)

	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, epochModelsTrigger)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, uint32(10), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
}

func TestPoCV2StoreCommit_ConfirmationRejectsModelOutsideEpoch(t *testing.T) {
	k, ctx, ms := setupEpochModelsCommitTest(t, testPoCModelID)

	_, err := ms.PoCV2StoreCommit(ctx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID2, 10, 1)))
	require.ErrorIs(t, err, types.ErrInvalidModel)

	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, epochModelsTrigger)
	require.NoError(t, err)
	require.Empty(t, commits)
}

func TestPoCV2StoreCommit_ConfirmationRejectsMixedEntriesWithoutPartialWrite(t *testing.T) {
	k, ctx, ms := setupEpochModelsCommitTest(t, testPoCModelID)

	_, err := ms.PoCV2StoreCommit(ctx, confirmationCommit(
		makePoCV2CommitEntry(testPoCModelID, 10, 1),
		makePoCV2CommitEntry(testPoCModelID2, 10, 2),
	))
	require.ErrorIs(t, err, types.ErrInvalidModel)

	commits, err := k.GetAllPoCV2StoreCommitsForStage(ctx, epochModelsTrigger)
	require.NoError(t, err)
	require.Empty(t, commits)
}

func TestPoCV2StoreCommit_ConfirmationRejectsCreatorOutsideEpoch(t *testing.T) {
	k, ctx, ms := setupEpochModelsCommitTest(t, testPoCModelID)
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Creator,
			Models: []string{testPoCModelID},
		}},
	}))

	_, err := ms.PoCV2StoreCommit(ctx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID, 10, 1)))
	require.ErrorIs(t, err, types.ErrParticipantNotFound)
}

func TestPoCV2StoreCommit_RegularPoCAcceptsModelOutsideCurrentEpoch(t *testing.T) {
	k, ctx, ms := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID, testPoCModelID2)
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 0,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: []string{testPoCModelID},
		}},
	}))

	_, err := ms.PoCV2StoreCommit(ctx, &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(testPoCModelID2, 10, 1)},
	})
	require.NoError(t, err)
}

func TestMLNodeWeightDistribution_ConfirmationRejectsModelWithoutAcceptedCommit(t *testing.T) {
	_, ctx, ms := setupEpochModelsCommitTest(t, testPoCModelID)

	_, err := ms.PoCV2StoreCommit(ctx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID2, 10, 1)))
	require.ErrorIs(t, err, types.ErrInvalidModel)

	_, err = ms.MLNodeWeightDistribution(ctx, &types.MsgMLNodeWeightDistribution{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: epochModelsTrigger,
		Entries: []*types.MLNodeDistributionEntry{{
			ModelId: testPoCModelID2,
			Weights: []*types.MLNodeWeight{{NodeId: "node-b", Weight: 10}},
		}},
	})
	require.ErrorContains(t, err, "no store commit found")
}

func TestCheckPoCV2StoreCommitMempool_ConfirmationRejectsModelOutsideEpoch(t *testing.T) {
	k, ctx, _ := setupEpochModelsCommitTest(t, testPoCModelID)
	checkCtx := ctx.WithIsCheckTx(true)

	require.NoError(t, k.CheckPoCV2StoreCommitMempool(checkCtx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID, 10, 1))))
	require.ErrorIs(t,
		k.CheckPoCV2StoreCommitMempool(checkCtx, confirmationCommit(makePoCV2CommitEntry(testPoCModelID2, 10, 1))),
		types.ErrInvalidModel,
	)
}

func TestPoCChallengeStoreCommit_RejectsModelOutsideEpoch(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	k.SetModel(ctx, &types.Model{Id: challengeTestModel})
	k.SetModel(ctx, &types.Model{Id: testPoCModelID2})
	registerPoCParticipant(t, k, ctx, testutil.Executor)
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 2,
		Participants: []*types.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: []string{challengeTestModel},
		}},
	}))
	ms := keeper.NewMsgServerImpl(k)

	_, err := ms.PoCChallengeStoreCommit(ctx, &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(testPoCModelID2, 4, 1)},
	})
	require.ErrorIs(t, err, types.ErrInvalidModel)

	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Empty(t, commits)
}
