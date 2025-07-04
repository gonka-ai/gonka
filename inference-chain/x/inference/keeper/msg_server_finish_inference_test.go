package keeper_test

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"go.uber.org/mock/gomock"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func advanceEpoch(ctx sdk.Context, k *keeper.Keeper, mocks *keeper2.InferenceMocks, blockHeight int64, epochGroupId uint64) (sdk.Context, error) {
	ctx = ctx.WithBlockHeight(blockHeight)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(10 * 60 * 1000 * 1000)) // 10 minutes later

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return ctx, types.ErrEffectiveEpochNotFound
	}
	newEpoch := types.Epoch{Index: epochIndex + 1, PocStartBlockHeight: blockHeight}
	k.SetEpoch(ctx, &newEpoch)
	k.SetEffectiveEpochIndex(ctx, newEpoch.Index)

	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochGroupId)

	eg, err := k.CreateEpochGroup(ctx, uint64(newEpoch.PocStartBlockHeight), epochIndex)
	if err != nil {
		return ctx, err
	}
	err = eg.CreateGroup(ctx)
	if err != nil {
		return ctx, err
	}

	return ctx, nil
}

func TestMsgServer_FinishInference(t *testing.T) {
	const (
		epochId  = 1
		epochId2 = 2

		inferenceId = "inferenceId"
	)

	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	initialBlockHeight := int64(10)
	ctx, err := advanceEpoch(ctx, &k, mocks, initialBlockHeight, epochId)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, initialBlockHeight, ctx.BlockHeight())
	initialBlockTime := ctx.BlockTime().UnixMilli()

	MustAddParticipant(t, ms, ctx, testutil.Requester)
	MustAddParticipant(t, ms, ctx, testutil.Creator)
	MustAddParticipant(t, ms, ctx, testutil.Executor)
	mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any())
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)

	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   inferenceId,
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, inferenceId)

	require.True(t, found)
	expectedInference := types.Inference{
		Index:               inferenceId,
		InferenceId:         inferenceId,
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		Model:               "model1",
		StartBlockHeight:    initialBlockHeight,
		StartBlockTimestamp: ctx.BlockTime().UnixMilli(),
		MaxTokens:           keeper.DefaultMaxTokens,
		EscrowAmount:        keeper.DefaultMaxTokens * calculations.PerTokenCost,
	}
	require.Equal(t, expectedInference, savedInference)
	devStat, found := k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId,
		InferenceIds: []string{expectedInference.InferenceId},
	}, devStat)

	newBlockHeight := initialBlockTime + 10
	ctx, err = advanceEpoch(ctx, &k, mocks, newBlockHeight, 2)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, newBlockHeight, ctx.BlockHeight())

	mocks.ExpectAnyCreateGroupWithPolicyCall()
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err)
	savedInference, found = k.GetInference(ctx, inferenceId)
	require.True(t, found)

	expectedInference2 := types.Inference{
		Index:                    inferenceId,
		InferenceId:              inferenceId,
		PromptHash:               "promptHash",
		PromptPayload:            "promptPayload",
		RequestedBy:              testutil.Requester,
		Status:                   types.InferenceStatus_FINISHED,
		ResponseHash:             "responseHash",
		ResponsePayload:          "responsePayload",
		PromptTokenCount:         10,
		CompletionTokenCount:     20,
		EpochPocStartBlockHeight: uint64(newBlockHeight),
		EpochId:                  epochId2,
		ExecutedBy:               testutil.Executor,
		Model:                    "model1",
		StartBlockTimestamp:      initialBlockTime,
		StartBlockHeight:         initialBlockHeight,
		EndBlockTimestamp:        ctx.BlockTime().UnixMilli(),
		EndBlockHeight:           newBlockHeight,
		MaxTokens:                keeper.DefaultMaxTokens,
		EscrowAmount:             keeper.DefaultMaxTokens * calculations.PerTokenCost,
		ActualCost:               30 * calculations.PerTokenCost,
	}

	require.Equal(t, expectedInference2, savedInference)

	participantState, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Weight:            -1,
		JoinTime:          initialBlockTime,
		JoinHeight:        initialBlockHeight,
		LastInferenceTime: ctx.BlockTime().UnixMilli(),
		InferenceUrl:      "url",
		Status:            types.ParticipantStatus_ACTIVE,
		CoinBalance:       30 * calculations.PerTokenCost,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			EarnedCoins:    30 * keeper.TokenCost,
		},
	}, participantState)

	devStat, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	require.Equal(t, 1, len(devStat.InferenceIds))

	devStatUpdated, found := k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId2,
		InferenceIds: []string{expectedInference2.InferenceId}}, devStatUpdated)
}

func MustAddParticipant(t *testing.T, ms types.MsgServer, ctx context.Context, address string) {
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: address,
		Url:     "url",
	})
	require.NoError(t, err)
}

func TestMsgServer_FinishInference_InferenceNotFound(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           testutil.Executor,
	})
	require.Error(t, err)
	_, found := k.GetInference(ctx, "inferenceId")
	require.False(t, found)
}
