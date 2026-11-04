package app

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectestutil "github.com/cosmos/cosmos-sdk/codec/testutil"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
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
	blstypes.RegisterInterfaces(ir)
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

func seedLiveValidationShape(t *testing.T, k inferencemodulekeeper.Keeper, ctx sdk.Context, signer string) {
	t.Helper()
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))
	k.SetModel(ctx, &inferencetypes.Model{Id: "test-model"})
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(testutil.Executor), inferencetypes.Participant{
		Index:   testutil.Executor,
		Address: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, inferencetypes.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		Count:                    100,
		RootHash:                 make([]byte, 32),
		ModelId:                  "test-model",
	}))
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, inferencetypes.PoCValidationSnapshot{
		PocStageStartHeight: 100,
		SnapshotHeight:      155,
		TotalNetworkWeight:  100,
		ModelVotingPowers: []*inferencetypes.ModelVotingPowers{{
			ModelId: "test-model",
			VotingPowers: []*inferencetypes.VotingPowerEntry{
				{Address: signer, VotingPower: 100},
			},
		}},
	}))
}

func TestPocPeriodValidationDecorator_RawBoundsBeforeParticipantCheck(t *testing.T) {
	_, ctx, decorator := setupPocPeriodAnte(t)
	// Executor is not registered. Raw bounds run before the participant lookup.
	msg := overRawWeightDistribution(testutil.Executor)

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, msg)
	require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	require.NotErrorIs(t, err, inferencetypes.ErrParticipantNotFound)
	require.False(t, nextCalled)

	inner, packErr := codectypes.NewAnyWithValue(msg)
	require.NoError(t, packErr)
	execMsg := &authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{inner},
	}
	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, execMsg)
	require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	require.NotErrorIs(t, err, inferencetypes.ErrParticipantNotFound)
	require.False(t, nextCalled)
}

func overRawWeightDistribution(creator string) *inferencetypes.MsgMLNodeWeightDistribution {
	weights := make([]*inferencetypes.MLNodeWeight, inferencetypes.MaxMLNodeWeightRawRowsPerEntry+1)
	for i := range weights {
		weights[i] = &inferencetypes.MLNodeWeight{}
	}
	return &inferencetypes.MsgMLNodeWeightDistribution{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Entries: []*inferencetypes.MLNodeDistributionEntry{{
			ModelId: "test-model",
			Weights: weights,
		}},
	}
}

func TestPocPeriodValidationDecorator_RejectsEmptyBatchAndAcceptsLaterBatch(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	signer := testutil.Creator
	seedLiveValidationShape(t, k, ctx, signer)

	empty := pocValidationsMsg(signer)
	empty.Validations = nil
	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, empty)
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)

	require.NoError(t, k.SetPocValidationV2(ctx, inferencetypes.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: signer,
		PocStageStartBlockHeight:    100,
		ModelId:                     "test-model",
		ValidatedWeight:             1,
	}))

	nextCalled, err = runPocPeriodAnte(t, decorator, ctx, pocValidationsMsg(signer))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocPeriodValidationDecorator_RejectsTooManyValidations(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	signer := testutil.Creator
	seedLiveValidationShape(t, k, ctx, signer)

	msg := pocValidationsMsg(signer)
	msg.Validations = make([]*inferencetypes.PoCValidationEntryV2, inferencetypes.MaxValidationsPerMessage+1)
	for i := range msg.Validations {
		msg.Validations[i] = &inferencetypes.PoCValidationEntryV2{
			ParticipantAddress: testutil.Executor,
			ModelId:            "test-model",
			ValidatedWeight:    1,
		}
	}

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, msg)
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_AllowsRegisteredParticipant(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)

	signer := testutil.Creator
	seedLiveValidationShape(t, k, ctx, signer)

	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, pocValidationsMsg(signer))
	require.NoError(t, err)
	require.True(t, nextCalled)
}

func TestPocVoterEligibilityDecorator_RejectsParticipantWithoutSnapshotVotingPower(t *testing.T) {
	k, ctx, _ := setupPocPeriodAnte(t)
	decorator := NewPocVoterEligibilityDecorator(&k, testMsgCodec(t))

	eligibleSigner := testutil.Creator
	seedLiveValidationShape(t, k, ctx, eligibleSigner)

	sybil := testutil.Validator2
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(sybil), inferencetypes.Participant{
		Index:   sybil,
		Address: sybil,
		Weight:  -1,
	}))

	runEligibility := func(msgs ...sdk.Msg) (bool, error) {
		nextCalled := false
		next := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			nextCalled = true
			return ctx, nil
		}
		_, err := decorator.AnteHandle(ctx, testFeeTx{msgs: msgs}, false, next)
		return nextCalled, err
	}

	nextCalled, err := runEligibility(pocValidationsMsg(sybil))
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)

	// Enabling sybil as a genesis guardian allows its tiebreaker vote through CheckTx.
	sybilAcc := sdk.MustAccAddressFromBech32(sybil)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.GenesisGuardianParams = &inferencetypes.GenesisGuardianParams{
		GuardianAddresses: []string{sdk.ValAddress(sybilAcc).String()},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &inferencetypes.GenesisOnlyParams{
		GenesisGuardianEnabled: true,
	}))

	nextCalled, err = runEligibility(pocValidationsMsg(sybil))
	require.NoError(t, err)
	require.True(t, nextCalled)

	// Repeated messages for the same (stage, signer) in one transaction reuse the
	// cached eligibility result instead of reading the snapshot again.
	oneMsgCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = decorator.AnteHandle(oneMsgCtx, testFeeTx{msgs: []sdk.Msg{pocValidationsMsg(eligibleSigner)}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	oneMsgGas := oneMsgCtx.GasMeter().GasConsumed()
	require.Greater(t, oneMsgGas, storetypes.Gas(0))

	inner1, err := codectypes.NewAnyWithValue(pocValidationsMsg(eligibleSigner))
	require.NoError(t, err)
	inner2, err := codectypes.NewAnyWithValue(pocValidationsMsg(eligibleSigner))
	require.NoError(t, err)
	execTwo := &authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{inner1, inner2},
	}
	twoMsgCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = decorator.AnteHandle(twoMsgCtx, testFeeTx{msgs: []sdk.Msg{execTwo}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, oneMsgGas, twoMsgCtx.GasMeter().GasConsumed(), "second inner message for same (stage, signer) must reuse cached eligibility without additional KV reads")
}

func mixedModelValidationMsg(creator string, models ...string) *inferencetypes.MsgSubmitPocValidationsV2 {
	rows := make([]*inferencetypes.PoCValidationEntryV2, 0, len(models))
	for _, modelID := range models {
		rows = append(rows, &inferencetypes.PoCValidationEntryV2{
			ParticipantAddress: testutil.Executor,
			ModelId:            modelID,
			ValidatedWeight:    -1,
		})
	}
	return &inferencetypes.MsgSubmitPocValidationsV2{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Validations:              rows,
	}
}

func TestPocVoterEligibilityDecorator_AdmitsMixedModelBatch(t *testing.T) {
	k, ctx, _ := setupPocPeriodAnte(t)
	decorator := NewPocVoterEligibilityDecorator(&k, testMsgCodec(t))
	signer := testutil.Creator
	seedLiveValidationShape(t, k, ctx, signer)

	runEligibility := func(msgs ...sdk.Msg) (bool, error) {
		nextCalled := false
		_, err := decorator.AnteHandle(ctx, testFeeTx{msgs: msgs}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			nextCalled = true
			return ctx, nil
		})
		return nextCalled, err
	}

	nextCalled, err := runEligibility(mixedModelValidationMsg(signer, "test-model", "model-b"))
	require.NoError(t, err)
	require.True(t, nextCalled)

	nextCalled, err = runEligibility(mixedModelValidationMsg(signer, "model-b"))
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)

	inner, err := codectypes.NewAnyWithValue(mixedModelValidationMsg(signer, "test-model", "model-b"))
	require.NoError(t, err)
	nextCalled, err = runEligibility(&authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{inner},
	})
	require.NoError(t, err)
	require.True(t, nextCalled)

	onlyB, err := codectypes.NewAnyWithValue(mixedModelValidationMsg(signer, "model-b"))
	require.NoError(t, err)
	nextCalled, err = runEligibility(&authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{onlyB},
	})
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)

	oneMsgCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = decorator.AnteHandle(oneMsgCtx, testFeeTx{msgs: []sdk.Msg{mixedModelValidationMsg(signer, "test-model", "model-b")}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	oneMsgGas := oneMsgCtx.GasMeter().GasConsumed()

	first, err := codectypes.NewAnyWithValue(mixedModelValidationMsg(signer, "test-model", "model-b"))
	require.NoError(t, err)
	second, err := codectypes.NewAnyWithValue(mixedModelValidationMsg(signer, "test-model", "model-b"))
	require.NoError(t, err)
	twoMsgCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = decorator.AnteHandle(twoMsgCtx, testFeeTx{msgs: []sdk.Msg{&authztypes.MsgExec{
		Grantee: testutil.Executor,
		Msgs:    []*codectypes.Any{first, second},
	}}}, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, oneMsgGas, twoMsgCtx.GasMeter().GasConsumed())
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
	seedLiveValidationShape(t, k, ctx, signer)

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
	seedLiveValidationShape(t, k, ctx, signer)

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

func storeCommitMsg(creator, modelID string, count uint32) *inferencetypes.MsgPoCV2StoreCommit {
	return &inferencetypes.MsgPoCV2StoreCommit{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Entries: []*inferencetypes.PoCV2CommitEntry{
			{ModelId: modelID, Count: count, RootHash: make([]byte, 32)},
		},
	}
}

func TestPocPeriodValidationDecorator_StoreCommitNextBlockClosed(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))

	k.SetModel(ctx, &inferencetypes.Model{Id: "test-model"})

	msg := storeCommitMsg(signer, "test-model", 10)
	nextCalled, err := runPocPeriodAnte(t, decorator, ctx, msg)
	require.NoError(t, err)
	require.True(t, nextCalled)

	deadlineCtx := ctx.WithBlockHeight(170)
	nextCalled, err = runPocPeriodAnte(t, decorator, deadlineCtx, msg)
	require.Error(t, err)
	require.ErrorIs(t, err, inferencetypes.ErrPocTooLate)
	require.False(t, nextCalled)
}

func TestPocPeriodValidationDecorator_StoreCommitRecheckModelOverlap(t *testing.T) {
	k, ctx, decorator := setupPocPeriodAnte(t)
	signer := testutil.Creator
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), inferencetypes.Participant{
		Index:   signer,
		Address: signer,
	}))
	k.SetModel(ctx, &inferencetypes.Model{Id: "test-model"})

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, inferencetypes.PoCV2StoreCommit{
		ParticipantAddress:       signer,
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        ctx.BlockHeight(),
		ModelId:                  "test-model",
	}))

	recheckCtx := ctx.WithIsReCheckTx(true)
	nextCalled, err := runPocPeriodAnte(t, decorator, recheckCtx, storeCommitMsg(signer, "test-model", 10))
	require.Error(t, err)
	require.ErrorIs(t, err, inferencetypes.ErrIllegalState)
	require.False(t, nextCalled)

	nextCalled, err = runPocPeriodAnte(t, decorator, recheckCtx, storeCommitMsg(signer, "test-model", 12))
	require.NoError(t, err)
	require.True(t, nextCalled)
}
