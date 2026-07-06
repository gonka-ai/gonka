package keeper_test

import (
	"testing"

	"cosmossdk.io/errors"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const classicInferenceDeprecatedText = "classic inference is deprecated"

func TestMsgServer_ClassicStartInferenceDeprecated(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	AddParticipantToActive(ctx, &k, testutil.Creator, 1)

	resp, err := ms.StartInference(ctx, &types.MsgStartInference{
		Creator:     testutil.Creator,
		InferenceId: "classic-start",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "classic-start", resp.InferenceIndex)
	require.Contains(t, resp.ErrorMessage, classicInferenceDeprecatedText)
	_, found := k.GetInference(ctx, "classic-start")
	require.False(t, found)
}

func TestMsgServer_ClassicFinishInferenceDeprecated(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	AddParticipantToActive(ctx, &k, testutil.Creator, 1)

	resp, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		Creator:     testutil.Creator,
		ExecutedBy:  testutil.Creator,
		InferenceId: "classic-finish",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "classic-finish", resp.InferenceIndex)
	require.Contains(t, resp.ErrorMessage, classicInferenceDeprecatedText)
	_, found := k.GetInference(ctx, "classic-finish")
	require.False(t, found)
}

func TestMsgServer_ClassicValidationDeprecated(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	AddParticipantToActive(ctx, &k, testutil.Validator, 1)

	_, err := ms.Validation(ctx, &types.MsgValidation{
		Creator:     testutil.Validator,
		InferenceId: "classic-validation",
	})

	require.True(t, errors.IsOf(err, types.ErrDeprecated), "err=%v", err)
	require.Contains(t, err.Error(), classicInferenceDeprecatedText)
}

func TestMsgServer_ClassicInvalidateInferenceDeprecated(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)

	_, err := ms.InvalidateInference(ctx, &types.MsgInvalidateInference{
		Creator:     testutil.Requester,
		InferenceId: "classic-invalidate",
	})

	require.True(t, errors.IsOf(err, types.ErrDeprecated), "err=%v", err)
	require.Contains(t, err.Error(), classicInferenceDeprecatedText)
}

func TestMsgServer_ClassicRevalidateInferenceDeprecated(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)

	_, err := ms.RevalidateInference(ctx, &types.MsgRevalidateInference{
		Creator:     testutil.Requester,
		InferenceId: "classic-revalidate",
	})

	require.True(t, errors.IsOf(err, types.ErrDeprecated), "err=%v", err)
	require.Contains(t, err.Error(), classicInferenceDeprecatedText)
}
