package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_SubmitNewParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: "creator",
		Url:     "url",
		Models:  []string{"model1", "model2"},
	})
	require.NoError(t, err)
	savedParticipant, found := k.GetParticipant(ctx, "creator")
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Participant{
		Index:             "creator",
		Address:           "creator",
		Reputation:        0,
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      "url",
		Models:            []string{"model1", "model2"},
		Status:            types.ParticipantStatus_RAMPING,
		CompletionTokenCount: map[string]uint64{
			"model1": 0,
			"model2": 0,
		},
		PromptTokenCount: map[string]uint64{
			"model1": 0,
			"model2": 0,
		},
	}, savedParticipant)
}
