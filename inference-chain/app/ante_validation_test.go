package app

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	testkeeper "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"
)

type testTx struct {
	msgs []sdk.Msg
}

func (t testTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t testTx) ValidateBasic() error                  { return nil }
func (t testTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func TestValidationEarlyRejectDecorator_DuplicateValidationRejectedInCheckTx(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)
	k.SetEffectiveEpochIndex(ctx, 0)

	modelID := "model-1"
	inferenceID := "inf-1"
	validator := testutil.Validator

	require.NoError(t, k.SetInferenceWithoutDevStatComputation(ctx, inferencetypes.Inference{
		Index:       inferenceID,
		InferenceId: inferenceID,
		Model:       modelID,
		EpochId:     0,
	}))

	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 0,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: validator, Weight: 1, Reputation: 100},
		},
	})

	require.NoError(t, k.SetEpochGroupValidations(ctx, inferencetypes.EpochGroupValidations{
		Participant: validator,
		EpochIndex:  0,
		// Intentionally unsorted: ante check should not rely on ordering.
		ValidatedInferences: []string{"zzz", inferenceID, "aaa"},
	}))

	decorator := NewValidationEarlyRejectDecorator(&k)
	tx := testTx{msgs: []sdk.Msg{&inferencetypes.MsgValidation{Creator: validator, InferenceId: inferenceID, Value: 0.5}}}

	ctx = ctx.WithIsCheckTx(true)
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.ErrorIs(t, err, inferencetypes.ErrDuplicateValidation)
}

func TestValidationEarlyRejectDecorator_NotInEpochRejectedInCheckTx(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)
	k.SetEffectiveEpochIndex(ctx, 0)

	modelID := "model-1"
	inferenceID := "inf-1"
	validator := testutil.Validator

	require.NoError(t, k.SetInferenceWithoutDevStatComputation(ctx, inferencetypes.Inference{
		Index:       inferenceID,
		InferenceId: inferenceID,
		Model:       modelID,
		EpochId:     0,
	}))

	// NOTE: validation weights intentionally do NOT include validator.
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 0,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: testutil.Validator2, Weight: 1, Reputation: 100},
		},
	})

	decorator := NewValidationEarlyRejectDecorator(&k)
	tx := testTx{msgs: []sdk.Msg{&inferencetypes.MsgValidation{Creator: validator, InferenceId: inferenceID, Value: 0.5}}}

	ctx = ctx.WithIsCheckTx(true)
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.ErrorIs(t, err, inferencetypes.ErrParticipantNotFound)
}

func TestValidationEarlyRejectDecorator_BypassesDeliverTx(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)
	k.SetEffectiveEpochIndex(ctx, 0)

	modelID := "model-1"
	inferenceID := "inf-1"
	validator := testutil.Validator

	require.NoError(t, k.SetInferenceWithoutDevStatComputation(ctx, inferencetypes.Inference{
		Index:       inferenceID,
		InferenceId: inferenceID,
		Model:       modelID,
		EpochId:     0,
	}))

	// Even if the message would fail (not in weights), DeliverTx bypasses this ante.
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 0,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: testutil.Validator2, Weight: 1, Reputation: 100},
		},
	})

	decorator := NewValidationEarlyRejectDecorator(&k)
	tx := testTx{msgs: []sdk.Msg{&inferencetypes.MsgValidation{Creator: validator, InferenceId: inferenceID, Value: 0.5}}}

	ctx = ctx.WithIsCheckTx(false)
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
}

func TestValidationEarlyRejectDecorator_DoesNotRejectInferenceFromNextEpochInCheckTx(t *testing.T) {
	t.Skip("TODO: This need to be re-enabled when we fixup the chain logic to use Inference Epoch, not current (bug)")
	k, ctx := testkeeper.InferenceKeeper(t)
	k.SetEffectiveEpochIndex(ctx, 0)

	modelID := "model-1"
	inferenceID := "inf-next-epoch"
	validator := testutil.Validator

	// Inference belongs to epoch 1, but local effective epoch is 0 (node lag scenario).
	require.NoError(t, k.SetInferenceWithoutDevStatComputation(ctx, inferencetypes.Inference{
		Index:       inferenceID,
		InferenceId: inferenceID,
		Model:       modelID,
		EpochId:     1,
	}))

	// Even if current epoch group data doesn't include the validator, we must not reject early.
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 0,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: testutil.Validator2, Weight: 1, Reputation: 100},
		},
	})

	decorator := NewValidationEarlyRejectDecorator(&k)
	tx := testTx{msgs: []sdk.Msg{&inferencetypes.MsgValidation{Creator: validator, InferenceId: inferenceID, Value: 0.5}}}

	ctx = ctx.WithIsCheckTx(true)
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
}
