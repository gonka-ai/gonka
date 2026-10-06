package keeper_test

import (
	"strings"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func challengeCommitMsg() *types.MsgPoCChallengeStoreCommit {
	return &types.MsgPoCChallengeStoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 50,
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:   mainnetPoCModelID,
			Count:     4,
			RootHash:  make([]byte, 32),
			TreeDepth: 24,
		}},
	}
}

func TestPoCChallengeStoreCommit_ValueOmitsKeyFields(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	k.SetModel(ctx, &types.Model{Id: mainnetPoCModelID})
	ms := keeper.NewMsgServerImpl(k)

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := ms.PoCChallengeStoreCommit(gctx, challengeCommitMsg())
	require.NoError(t, err)
	t.Logf("PoCChallengeStoreCommit gas: %d", gctx.GasMeter().GasConsumed())

	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	raw, err := k.PoCChallengeCommits.Get(ctx, collections.Join(addr, mainnetPoCModelID))
	require.NoError(t, err)
	require.Empty(t, raw.ParticipantAddress)
	require.Empty(t, raw.ModelId)
	require.Equal(t, int64(50), raw.PocStageStartBlockHeight)

	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, testutil.Executor, commits[0].ParticipantAddress)
	require.Equal(t, mainnetPoCModelID, commits[0].ModelId)
	require.Equal(t, int64(50), commits[0].PocStageStartBlockHeight)
	require.Equal(t, uint32(4), commits[0].Count)
	require.Equal(t, uint32(24), commits[0].TreeDepth)

	// A second commit for the same model reads the trimmed record back as existing.
	msg := challengeCommitMsg()
	msg.Entries[0].Count = 6
	_, err = ms.PoCChallengeStoreCommit(ctx.WithBlockHeight(101), msg)
	require.NoError(t, err)
	commits, err = k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, uint32(6), commits[0].Count)
	require.Equal(t, mainnetPoCModelID, commits[0].ModelId)
}

func TestListChallengeCommits_LegacyFullRecord(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	full := types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 50,
		Count:                    4,
		CommitBlockHeight:        60,
		ModelId:                  mainnetPoCModelID,
		TreeDepth:                24,
	}
	require.NoError(t, k.PoCChallengeCommits.Set(ctx, collections.Join(addr, mainnetPoCModelID), full))
	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Equal(t, []types.PoCV2StoreCommit{full}, commits)
}

func challengeVoteCtx(t *testing.T, k keeper.Keeper, ctx sdk.Context) sdk.Context {
	t.Helper()
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Validator), types.Participant{
		Index:   testutil.Validator,
		Address: testutil.Validator,
	}))
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
	return ctx.WithBlockHeight(event.GetValidationStart(params.EpochParams))
}

func submitChallengeVote(t *testing.T, k keeper.Keeper, ctx sdk.Context, participant string) storetypes.Gas {
	t.Helper()
	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := keeper.NewMsgServerImpl(k).SubmitPoCChallengeValidations(gctx, &types.MsgSubmitPoCChallengeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 50,
		Validations: []*types.PoCValidationEntryV2{{
			ParticipantAddress: participant,
			ModelId:            mainnetPoCModelID,
			ValidatedWeight:    4,
		}},
	})
	require.NoError(t, err)
	return gctx.GasMeter().GasConsumed()
}

func TestSubmitPoCChallengeValidations_ValueOmitsKeyFields(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	voteCtx := challengeVoteCtx(t, k, ctx)
	t.Logf("SubmitPoCChallengeValidations gas: %d", submitChallengeVote(t, k, voteCtx, testutil.Executor))

	target := sdk.MustAccAddressFromBech32(testutil.Executor)
	validator := sdk.MustAccAddressFromBech32(testutil.Validator)
	raw, err := k.PoCChallengeValidations.Get(ctx, collections.Join3(target, mainnetPoCModelID, validator))
	require.NoError(t, err)
	require.Equal(t, types.PoCValidationV2{PocStageStartBlockHeight: 50, ValidatedWeight: 4}, raw)

	vals, err := k.ListChallengeValidations(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Equal(t, []types.PoCValidationV2{{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    50,
		ValidatedWeight:             4,
		ModelId:                     mainnetPoCModelID,
	}}, vals)
}

func TestSubmitPoCChallengeValidations_NonCanonicalAddressKeepsFields(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	voteCtx := challengeVoteCtx(t, k, ctx)
	upper := strings.ToUpper(testutil.Executor)
	submitChallengeVote(t, k, voteCtx, upper)

	vals, err := k.ListChallengeValidations(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, vals, 1)
	require.Equal(t, upper, vals[0].ParticipantAddress)
	require.Equal(t, mainnetPoCModelID, vals[0].ModelId)
}

func TestPoCChallengeStoreCommit_NonCanonicalAddressKeepsFields(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	k.SetModel(ctx, &types.Model{Id: mainnetPoCModelID})
	upper := strings.ToUpper(testutil.Executor)
	msg := challengeCommitMsg()
	msg.Creator = upper
	_, err := keeper.NewMsgServerImpl(k).PoCChallengeStoreCommit(ctx, msg)
	require.NoError(t, err)

	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	raw, err := k.PoCChallengeCommits.Get(ctx, collections.Join(addr, mainnetPoCModelID))
	require.NoError(t, err)
	require.Equal(t, upper, raw.ParticipantAddress)
	require.Equal(t, mainnetPoCModelID, raw.ModelId)

	commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, upper, commits[0].ParticipantAddress)
}

func TestListChallengeValidations_LegacyFullRecord(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 100)
	full := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    50,
		ValidatedWeight:             4,
		ModelId:                     mainnetPoCModelID,
	}
	key := collections.Join3(sdk.MustAccAddressFromBech32(testutil.Executor), mainnetPoCModelID, sdk.MustAccAddressFromBech32(testutil.Validator))
	require.NoError(t, k.PoCChallengeValidations.Set(ctx, key, full))
	vals, err := k.ListChallengeValidations(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Equal(t, []types.PoCValidationV2{full}, vals)
}
