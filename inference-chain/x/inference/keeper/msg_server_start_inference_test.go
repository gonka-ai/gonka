package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_StartInferenceWithUnregesteredParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	_ = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: testutil.Creator,
			},
		},
	})
	response, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.NoError(t, err)
	require.NotEmpty(t, response.ErrorMessage)
}

func TestMsgServer_StartInference_DeveloperNotAllowlisted(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	_ = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: testutil.Creator,
			},
		},
	})
	_ = k.SetModelCurrentPrice(ctx, "", calculations.PerTokenCost)

	// Enable gating at current height + 100, with an allowlist that does NOT include testutil.Requester.
	p, err := k.GetParams(ctx)
	require.NoError(t, err)
	p.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          ctx.BlockHeight() + 100,
		AllowedDeveloperAddresses: []string{"gonka1notallowlistedxxxxxxxxxxxxxxxxxxxxxx"},
	}
	require.NoError(t, k.SetParams(ctx, p))

	response, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.NoError(t, err)
	require.NotEmpty(t, response.ErrorMessage)
}

func TestMsgServer_StartInference(t *testing.T) {
	const (
		epochId = 1
	)
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()
	initialBlockHeight := int64(10)
	ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochId)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, initialBlockHeight, ctx.BlockHeight())

	expected, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp,
		calculations.DefaultMaxTokens)
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, expected, &savedInference)
	_, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.False(t, found)
}

func TestMsgServer_StartInferenceWithMaxTokens(t *testing.T) {
	const (
		epochId = 1
	)
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()
	initialBlockHeight := int64(10)
	ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochId)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, initialBlockHeight, ctx.BlockHeight())

	expected, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp,
		2000) // Using a custom max tokens value
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, expected, &savedInference)
	_, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.False(t, found)
}

func TestMsgServer_StartInference_DoesNotUpdateExecutorBeforeCompletion(t *testing.T) {
	inferenceHelper, k, _ := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()

	beforeExecutor, found := k.GetParticipant(inferenceHelper.context, testutil.Executor)
	require.True(t, found)
	if beforeExecutor.CurrentEpochStats == nil {
		beforeExecutor.CurrentEpochStats = &types.CurrentEpochStats{}
	}
	beforeEarned := beforeExecutor.CurrentEpochStats.EarnedCoins
	beforeInferenceCount := beforeExecutor.CurrentEpochStats.InferenceCount

	_, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp, calculations.DefaultMaxTokens)
	require.NoError(t, err)

	afterExecutor, found := k.GetParticipant(inferenceHelper.context, testutil.Executor)
	require.True(t, found)
	require.NotNil(t, afterExecutor.CurrentEpochStats)
	require.Equal(t, beforeEarned, afterExecutor.CurrentEpochStats.EarnedCoins)
	require.Equal(t, beforeInferenceCount, afterExecutor.CurrentEpochStats.InferenceCount)
}

func TestMsgServer_StartInference_ParamsCacheDoesNotLeakAcrossCalls(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	err := k.SetEffectiveEpochIndex(ctx, 1)
	require.NoError(t, err)
	err = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: testutil.Creator,
			},
		},
	})
	require.NoError(t, err)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          ctx.BlockHeight() + 100,
		AllowedDeveloperAddresses: []string{testutil.Requester},
	}
	require.NoError(t, k.SetParams(ctx, params))

	firstResp, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "cache-test-1",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.NoError(t, err)
	require.NotContains(t, firstResp.ErrorMessage, types.ErrDeveloperNotAllowlisted.Error())
	require.Contains(t, firstResp.ErrorMessage, types.ErrParticipantNotFound.Error())

	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          ctx.BlockHeight() + 100,
		AllowedDeveloperAddresses: []string{"gonka1notallowlistedxxxxxxxxxxxxxxxxxxxxxx"},
	}
	require.NoError(t, k.SetParams(ctx, params))

	secondResp, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "cache-test-2",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.NoError(t, err)
	require.Contains(t, secondResp.ErrorMessage, types.ErrDeveloperNotAllowlisted.Error())
}

// TODO: Need a way to test that blockheight is set to newer values, but can't figure out how to change the
// test value of the blockheight
