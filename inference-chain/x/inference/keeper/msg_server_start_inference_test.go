package keeper_test

import (
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_StartInferenceWithUnregesteredParticipant(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.Error(t, err)
}

func TestMsgServer_StartInference(t *testing.T) {
	const (
		epochId     = 1
		inferenceId = "inferenceId"
	)

	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	k.SetEpoch(ctx, &types.Epoch{Index: epochId, PocStartBlockHeight: epochId * 10})
	k.SetEffectiveEpochIndex(ctx, epochId)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Creator,
		Url:     "url",
	})
	require.NoError(t, err)
	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Requester,
		Url:     "url",
	})
	mocks.BankKeeper.ExpectPay(sdkCtx, testutil.Requester, keeper.DefaultMaxTokens*calculations.PerTokenCost)
	require.NoError(t, err)
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   inferenceId,
		Model:         "model1",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		// MaxTokens is not set, should use default
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Inference{
		Index:               inferenceId,
		InferenceId:         inferenceId,
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		StartBlockHeight:    0,
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
		MaxTokens:           keeper.DefaultMaxTokens,
		EscrowAmount:        keeper.DefaultMaxTokens * calculations.PerTokenCost,
		Model:               "model1",
	}, savedInference)

	devStat, found := k.GetDevelopersStatsByEpoch(ctx2, savedInference.RequestedBy, epochId)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId,
		InferenceIds: []string{inferenceId},
	}, devStat)
}

func TestMsgServer_StartInferenceWithMaxTokens(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Creator,
		Url:     "url",
	})
	require.NoError(t, err)
	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Requester,
		Url:     "url",
	})

	// Custom max tokens value
	customMaxTokens := uint64(2000)
	mocks.BankKeeper.ExpectPay(sdkCtx, testutil.Requester, customMaxTokens*calculations.PerTokenCost)
	require.NoError(t, err)
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		MaxTokens:     customMaxTokens, // Set custom max tokens
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Inference{
		Index:               "inferenceId",
		InferenceId:         "inferenceId",
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		StartBlockHeight:    0,
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
		MaxTokens:           customMaxTokens,                                    // Should use custom max tokens
		EscrowAmount:        int64(customMaxTokens * calculations.PerTokenCost), // Escrow should be based on custom max tokens
	}, savedInference)
}

// TODO: Need a way to test that blockheight is set to newer values, but can't figure out how to change the
// test value of the blockheight
