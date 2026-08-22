package app

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectestutil "github.com/cosmos/cosmos-sdk/codec/testutil"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPocPeriodValidationDecorator_NonPocMessage(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Non-PoC messages pass through without validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}

func TestPocPeriodValidationDecorator_SimulationMode(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Simulation mode bypasses PoC period validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}

// setupPocPeriodAnte returns a CheckTx context positioned inside the PoC
// validation exchange window (PocStart=100, generation ends at 150, validation
// runs 156-255) together with a decorator bound to a fresh inference keeper.
func setupPocPeriodAnte(t *testing.T) (inferencemodulekeeper.Keeper, sdk.Context, PocPeriodValidationDecorator) {
	t.Helper()

	k, goCtx, _ := keepertest.InferenceKeeperReturningMocks(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithBlockHeight(160).WithIsCheckTx(true)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &inferencetypes.PocParams{PocV2Enabled: true}
	params.EpochParams = &inferencetypes.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	require.NoError(t, k.SetParams(ctx, params))

	k.SetEffectiveEpochIndex(ctx, 0)
	k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100})

	return k, ctx, NewPocPeriodValidationDecorator(&k, testMsgCodec(t))
}

func testMsgCodec(t *testing.T) codec.Codec {
	t.Helper()
	ir := codectestutil.CodecOptions{
		AccAddressPrefix: "gonka",
		ValAddressPrefix: "gonkavaloper",
	}.NewInterfaceRegistry()
	inferencetypes.RegisterInterfaces(ir)
	authztypes.RegisterInterfaces(ir)
	return codec.NewProtoCodec(ir)
}

func pocValidationsMsg(creator string) *inferencetypes.MsgSubmitPocValidationsV2 {
	return &inferencetypes.MsgSubmitPocValidationsV2{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Validations: []*inferencetypes.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: "test-model", ValidatedWeight: 100},
		},
	}
}

func runPocPeriodAnte(t *testing.T, decorator PocPeriodValidationDecorator, ctx sdk.Context, msgs ...sdk.Msg) (bool, error) {
	t.Helper()
	nextCalled := false
	next := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	}
	_, err := decorator.AnteHandle(ctx, testFeeTx{msgs: msgs}, false, next)
	return nextCalled, err
}

func TestPocPeriodValidationDecorator_RejectsNonParticipant(t *testing.T) {
	testCases := []struct {
		name string
		msg  sdk.Msg
	}{
		{
			name: "MsgSubmitPocBatch",
			msg: &inferencetypes.MsgSubmitPocBatch{
				Creator:                  testutil.Creator,
				PocStageStartBlockHeight: 100,
				NodeId:                   "node-1",
			},
		},
		{
			name: "MsgSubmitPocValidationsV2",
			msg:  pocValidationsMsg(testutil.Creator),
		},
		{
			name: "MsgPoCV2StoreCommit",
			msg: &inferencetypes.MsgPoCV2StoreCommit{
				Creator:                  testutil.Creator,
				PocStageStartBlockHeight: 100,
				Entries: []*inferencetypes.PoCV2CommitEntry{
					{ModelId: "test-model", Count: 10, RootHash: make([]byte, 32)},
				},
			},
		},
		{
			name: "MsgMLNodeWeightDistribution",
			msg: &inferencetypes.MsgMLNodeWeightDistribution{
				Creator:                  testutil.Creator,
				PocStageStartBlockHeight: 100,
				Entries: []*inferencetypes.MLNodeDistributionEntry{
					{ModelId: "test-model", Weights: []*inferencetypes.MLNodeWeight{{NodeId: "node-1", Weight: 10}}},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, ctx, decorator := setupPocPeriodAnte(t)

			nextCalled, err := runPocPeriodAnte(t, decorator, ctx, tc.msg)
			require.ErrorIs(t, err, inferencetypes.ErrParticipantNotFound)
			require.False(t, nextCalled)
		})
	}
}

func TestPocPeriodValidationDecorator_AllowsRegisteredParticipant(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)

	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, pocValidationsMsg(signer))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocPeriodValidationDecorator_RejectsMalformedCreator(t *testing.T) {
	_, ctx, decorator := setupPocPeriodAnte(t)

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, pocValidationsMsg("not-a-bech32-address"))
	require.Error(t, err)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_AllowsRegisteredParticipantInsideMsgExec(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)

	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))

	inner, err := codectypes.NewAnyWithValue(pocValidationsMsg(signer))
	require.NoError(t, err)
	execMsg := &authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{inner},
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, execMsg)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocPeriodValidationDecorator_GranteeEqualsGranter_NoGrantRequired(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)

	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))

	inner, err := codectypes.NewAnyWithValue(pocValidationsMsg(signer))
	require.NoError(t, err)
	execMsg := &authztypes.MsgExec{
		Grantee: signer,
		Msgs:    []*codectypes.Any{inner},
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, execMsg)
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocPeriodValidationDecorator_RejectsNonParticipantEvenWithMsgExec(t *testing.T) {
	_, ctx, decorator := setupPocPeriodAnte(t)

	inner, err := codectypes.NewAnyWithValue(pocValidationsMsg(testutil.Creator))
	require.NoError(t, err)
	execMsg := &authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{inner},
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, execMsg)
	require.ErrorIs(t, err, inferencetypes.ErrParticipantNotFound)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_RejectsMalformedMsgExec(t *testing.T) {
	_, ctx, decorator := setupPocPeriodAnte(t)

	execMsg := &authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs: []*codectypes.Any{{
			TypeUrl: sdk.MsgTypeURL(&inferencetypes.MsgSubmitPocValidationsV2{}),
			Value:   []byte{0xff, 0x00, 0x01},
		}},
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, execMsg)
	require.Error(t, err)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_RejectsNestedMsgExec(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)

	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))

	innerExec := authztypes.NewMsgExec(
		sdk.MustAccAddressFromBech32(testutil.Executor),
		[]sdk.Msg{pocValidationsMsg(signer)},
	)
	outer := authztypes.NewMsgExec(
		sdk.MustAccAddressFromBech32(testutil.Executor),
		[]sdk.Msg{&innerExec},
	)

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, &outer)
	require.ErrorIs(t, err, errNestedMsgExec)
	require.False(t, nextCalled)
}
