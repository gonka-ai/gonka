package app

import (
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	anteEpochModel   = "model-a"
	anteForeignModel = "model-b"
)

func seedEpochModelAnte(t *testing.T, k inferencemodulekeeper.Keeper, ctx sdk.Context, epochIndex uint64) {
	t.Helper()
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), inferencetypes.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	k.SetModel(ctx, &inferencetypes.Model{Id: anteEpochModel})
	k.SetModel(ctx, &inferencetypes.Model{Id: anteForeignModel})
	require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId: epochIndex,
		Participants: []*inferencetypes.ActiveParticipant{{
			Index:  testutil.Executor,
			Models: []string{anteEpochModel},
		}},
	}))
}

func challengeStoreCommitMsg(modelID string) *inferencetypes.MsgPoCChallengeStoreCommit {
	return &inferencetypes.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*inferencetypes.PoCV2CommitEntry{
			{ModelId: modelID, Count: 10, RootHash: make([]byte, 32), TreeDepth: 24},
		},
	}
}

func wrapInMsgExec(t *testing.T, msg sdk.Msg) *authztypes.MsgExec {
	t.Helper()
	inner, err := codectypes.NewAnyWithValue(msg)
	require.NoError(t, err)
	return &authztypes.MsgExec{Grantee: testutil.Creator, Msgs: []*codectypes.Any{inner}}
}

func TestPocPeriodValidationDecorator_ChallengeStoreCommitEpochModels(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	seedEpochModelAnte(t, k, ctx, 1)
	require.NoError(t, k.SetPoCChallenge(ctx, inferencetypes.PoCChallenge{
		EpochIndex:  1,
		Challenger:  testutil.Creator,
		Target:      testutil.Executor,
		StartHeight: 100,
	}))

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, challengeStoreCommitMsg(anteEpochModel))
	require.NoError(t, err)
	require.True(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, challengeStoreCommitMsg(anteForeignModel))
	require.ErrorIs(t, err, inferencetypes.ErrInvalidModel)
	require.False(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, wrapInMsgExec(t, challengeStoreCommitMsg(anteForeignModel)))
	require.ErrorIs(t, err, inferencetypes.ErrInvalidModel)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_ChallengeStoreCommitWithoutOpenChallengeLeftToHandler(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	seedEpochModelAnte(t, k, ctx, 1)

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, challengeStoreCommitMsg(anteForeignModel))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocPeriodValidationDecorator_ConfirmationStoreCommitEpochModels(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	seedEpochModelAnte(t, k, ctx, 1)
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, inferencetypes.ConfirmationPoCEvent{
		EpochIndex:            1,
		TriggerHeight:         500,
		GenerationStartHeight: 505,
		Phase:                 inferencetypes.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	ctx = ctx.WithBlockHeight(510)
	commit := func(modelID string) *inferencetypes.MsgPoCV2StoreCommit {
		msg := storeCommitMsg(testutil.Executor, modelID, 10)
		msg.PocStageStartBlockHeight = 500
		return msg
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, commit(anteEpochModel))
	require.NoError(t, err)
	require.True(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, commit(anteForeignModel))
	require.ErrorIs(t, err, inferencetypes.ErrInvalidModel)
	require.False(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, wrapInMsgExec(t, commit(anteForeignModel)))
	require.ErrorIs(t, err, inferencetypes.ErrInvalidModel)
	require.False(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx.WithIsReCheckTx(true), commit(anteForeignModel))
	require.ErrorIs(t, err, inferencetypes.ErrInvalidModel)
	require.False(t, nextCalled)
}
