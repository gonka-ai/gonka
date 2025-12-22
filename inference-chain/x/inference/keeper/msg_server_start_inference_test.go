package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_StartInferenceWithUnregesteredParticipant(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
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
	devStat, found := k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId,
		InferenceIds: []string{expected.InferenceId},
	}, devStat)
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
	devStat, found := k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId,
		InferenceIds: []string{expected.InferenceId},
	}, devStat)
}

// TODO: Need a way to test that blockheight is set to newer values, but can't figure out how to change the
// test value of the blockheight
