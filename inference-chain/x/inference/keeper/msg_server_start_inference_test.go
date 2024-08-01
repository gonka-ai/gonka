package keeper_test

import (
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
		ReceivedBy:    "receivedBy",
		Creator:       "receivedBy",
	})
	require.Error(t, err)
}

func TestMsgServer_StartInference(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: "receivedBy",
		Url:     "url",
		Models:  []string{"model1", "model2"},
	})
	require.NoError(t, err)
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    "receivedBy",
		Creator:       "receivedBy",
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
		ReceivedBy:          "receivedBy",
		Status:              "STARTED",
		StartBlockHeight:    0,
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
	}, savedInference)
}

// TODO: Need a way to test that blockheight is set to newer values, but can't figure out how to change the
// test value of the blockheight
