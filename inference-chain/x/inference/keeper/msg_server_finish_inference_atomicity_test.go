package keeper_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// finishInferenceAtomicityHelper wraps MockInferenceHelper to control
// bank mock behavior for FinishInference atomicity tests.
type finishInferenceAtomicityHelper struct {
	helper *MockInferenceHelper
	k      keeper.Keeper
	ctx    sdk.Context
}

func newFinishInferenceAtomicityHelper(t *testing.T) *finishInferenceAtomicityHelper {
	h, k, ctx := NewMockInferenceHelper(t)
	return &finishInferenceAtomicityHelper{helper: h, k: k, ctx: ctx}
}

// startAndAdvanceEpoch creates an inference via StartInference and advances the epoch
// so FinishInference sees the inference in a valid state.
func (f *finishInferenceAtomicityHelper) startAndAdvanceEpoch(t *testing.T) *types.Inference {
	t.Helper()

	const (
		epochId  = 1
		epochId2 = 2
	)

	requestTimestamp := f.helper.context.BlockTime().UnixNano()
	initialBlockTime := f.ctx.BlockTime().UnixMilli()
	initialBlockHeight := int64(10)

	var err error
	f.ctx, err = advanceEpoch(f.ctx, &f.k, f.helper.Mocks, initialBlockHeight, epochId)
	require.NoError(t, err)
	// Sync the helper context and re-register active participants for the new epoch
	f.helper.context = f.ctx
	f.helper.EnsureActiveParticipants()

	modelId := "model1"
	model := types.Model{Id: modelId}
	f.k.SetModel(f.ctx, &model)

	inf, err := f.helper.StartInference(
		"promptPayload",
		modelId,
		requestTimestamp,
		calculations.DefaultMaxTokens)
	require.NoError(t, err)

	newBlockHeight := initialBlockTime + 10
	f.ctx, err = advanceEpoch(f.ctx, &f.k, f.helper.Mocks, newBlockHeight, epochId2)
	require.NoError(t, err)
	f.helper.context = f.ctx
	f.helper.EnsureActiveParticipants()

	StubModelSubgroup(t, f.ctx, f.k, f.helper.Mocks, &model)

	return inf
}

// buildFinishMsg constructs a valid FinishInference message with correct signatures.
func (f *finishInferenceAtomicityHelper) buildFinishMsg(t *testing.T) *types.MsgFinishInference {
	t.Helper()

	prev := f.helper.previousInference
	require.NotNil(t, prev, "must call startAndAdvanceEpoch first")

	originalPromptHash := sha256HashForAtomicity(f.helper.promptPayload)
	promptHash := prev.PromptHash

	// Dev signs original_prompt_hash
	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       prev.RequestTimestamp,
		TransferAddress: f.helper.MockTransferAgent.address,
		ExecutorAddress: "",
	}
	inferenceId, err := calculations.Sign(f.helper.MockRequester, devComponents, calculations.Developer)
	require.NoError(t, err)

	// TA and Executor sign prompt_hash
	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       prev.RequestTimestamp,
		TransferAddress: f.helper.MockTransferAgent.address,
		ExecutorAddress: f.helper.MockExecutor.address,
	}
	taSignature, err := calculations.Sign(f.helper.MockTransferAgent, taComponents, calculations.TransferAgent)
	require.NoError(t, err)
	eaSignature, err := calculations.Sign(f.helper.MockExecutor, taComponents, calculations.ExecutorAgent)
	require.NoError(t, err)

	// Setup account mock expectations for signature verification
	f.helper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), f.helper.MockRequester.GetBechAddress()).Return(f.helper.MockRequester).AnyTimes()
	f.helper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), f.helper.MockTransferAgent.GetBechAddress()).Return(f.helper.MockTransferAgent).AnyTimes()
	f.helper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), f.helper.MockExecutor.GetBechAddress()).Return(f.helper.MockExecutor).AnyTimes()

	return &types.MsgFinishInference{
		Creator:              f.helper.MockExecutor.address,
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           f.helper.MockExecutor.address,
		TransferredBy:        f.helper.MockTransferAgent.address,
		RequestTimestamp:      prev.RequestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    eaSignature,
		RequestedBy:          f.helper.MockRequester.address,
		OriginalPrompt:       f.helper.promptPayload,
		Model:                prev.Model,
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
	}
}

func sha256HashForAtomicity(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// TestFinishInference_PaymentFails_InferenceUnchanged proves that when
// the refund payment (SendCoinsFromModuleToAccount) fails during FinishInference,
// CacheContext rolls back all state changes. The inference remains in STARTED state
// and the executor's participant stats are unchanged.
func TestFinishInference_PaymentFails_InferenceUnchanged(t *testing.T) {
	f := newFinishInferenceAtomicityHelper(t)
	inf := f.startAndAdvanceEpoch(t)

	// Capture pre-FinishInference state
	savedInference, found := f.k.GetInference(f.ctx, inf.InferenceId)
	require.True(t, found)
	originalStatus := savedInference.Status

	executor, found := f.k.GetParticipant(f.ctx, f.helper.MockExecutor.address)
	require.True(t, found)
	originalInferenceCount := executor.CurrentEpochStats.InferenceCount

	msg := f.buildFinishMsg(t)

	// Mock: refund payment (SendCoinsFromModuleToAccount) fails.
	// processInferencePayments calls IssueRefund -> PayParticipantFromEscrow ->
	// SendCoinsFromModuleToAccount when there's excess escrow to refund.
	f.helper.Mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(fmt.Errorf("insufficient funds in module account"))

	resp, err := f.helper.MessageServer.FinishInference(f.helper.context, msg)

	// FinishInference returns (response, nil) -- errors go in ErrorMessage
	require.NoError(t, err, "FinishInference returns nil error by convention")
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.ErrorMessage, "response should contain error message")

	// KEY ASSERTION: inference state must be unchanged (CacheContext rolled back)
	afterInference, found := f.k.GetInference(f.ctx, inf.InferenceId)
	require.True(t, found, "inference must still exist")
	require.Equal(t, originalStatus, afterInference.Status,
		"inference status must be unchanged after payment failure")

	// Executor stats must not have been incremented
	afterExecutor, found := f.k.GetParticipant(f.ctx, f.helper.MockExecutor.address)
	require.True(t, found)
	require.Equal(t, originalInferenceCount, afterExecutor.CurrentEpochStats.InferenceCount,
		"executor inference count must be unchanged after payment failure")
}

// TestFinishInference_HappyPath_AllStateCommitted proves that when all mutations
// succeed, CacheContext commits and the inference is marked completed with
// correct participant stats.
func TestFinishInference_HappyPath_AllStateCommitted(t *testing.T) {
	f := newFinishInferenceAtomicityHelper(t)
	inf := f.startAndAdvanceEpoch(t)

	msg := f.buildFinishMsg(t)

	// Mock: all bank calls succeed
	f.helper.Mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(nil)

	resp, err := f.helper.MessageServer.FinishInference(f.helper.context, msg)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Empty(t, resp.ErrorMessage, "happy path should have no error")

	// Inference should be marked as finished
	afterInference, found := f.k.GetInference(f.ctx, inf.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, afterInference.Status,
		"inference should be FINISHED after successful completion")
}

// -- Alternate approach using InferenceKeeperReturningMocks for simpler setup --

// TestFinishInference_RefundError_Propagated verifies that the fixed IssueRefund
// error propagation in processInferencePayments actually returns an error
// (instead of swallowing it) which causes the CacheContext to NOT commit.
func TestFinishInference_RefundError_Propagated(t *testing.T) {
	k, ctx, mocks := keeper2.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Setup: full MockInferenceHelper flow
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DynamicPricingParams.GracePeriodEndEpoch = 0
	k.SetParams(ctx, params)

	requester := NewMockAccount(testutil.Requester)
	ta := NewMockAccount(testutil.Creator)
	executor := NewMockAccount(testutil.Executor)
	MustAddParticipant(t, ms, ctx, *requester)
	MustAddParticipant(t, ms, ctx, *ta)
	MustAddParticipant(t, ms, ctx, *executor)

	// Register all participants as active for epoch 0 (genesis) so CheckPermission passes
	currentEpoch, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	_ = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: currentEpoch,
		Participants: []*types.ActiveParticipant{
			{Index: requester.address},
			{Index: ta.address},
			{Index: executor.address},
		},
	})

	// Start inference (needs escrow payment to succeed)
	mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), requester.GetBechAddress()).Return(requester).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), ta.GetBechAddress()).Return(ta).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), executor.GetBechAddress()).Return(executor).AnyTimes()
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()

	requestTimestamp := ctx.BlockTime().UnixNano()
	promptPayload := "test-prompt"
	originalPromptHash := sha256HashForAtomicity(promptPayload)
	promptHash := sha256HashForAtomicity(promptPayload)
	modelId := "model1"

	// Advance to epoch 1
	ctx = ctx.WithBlockHeight(10)
	epoch1 := types.Epoch{Index: 1, PocStartBlockHeight: 10}
	k.SetEpoch(ctx, &epoch1)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	_ = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      1,
		Participants: []*types.ActiveParticipant{
			{Index: requester.address}, {Index: ta.address}, {Index: executor.address},
		},
	})
	mocks.ExpectCreateGroupWithPolicyCall(ctx, 1)
	eg, err := k.CreateEpochGroup(ctx, 10, 1)
	require.NoError(t, err)
	require.NoError(t, eg.CreateGroup(ctx))

	model := types.Model{Id: modelId}
	k.SetModel(ctx, &model)

	// Sign StartInference
	devComponents := calculations.SignatureComponents{
		Payload: originalPromptHash, Timestamp: requestTimestamp,
		TransferAddress: ta.address, ExecutorAddress: "",
	}
	inferenceId, err := calculations.Sign(requester, devComponents, calculations.Developer)
	require.NoError(t, err)

	taComponents := calculations.SignatureComponents{
		Payload: promptHash, Timestamp: requestTimestamp,
		TransferAddress: ta.address, ExecutorAddress: executor.address,
	}
	taSignature, err := calculations.Sign(ta, taComponents, calculations.TransferAgent)
	require.NoError(t, err)

	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:        inferenceId,
		PromptHash:         promptHash,
		PromptPayload:      promptPayload,
		RequestedBy:        requester.address,
		Creator:            ta.address,
		Model:              modelId,
		OriginalPrompt:     promptPayload,
		OriginalPromptHash: originalPromptHash,
		RequestTimestamp:    requestTimestamp,
		TransferSignature:  taSignature,
		AssignedTo:         executor.address,
	})
	require.NoError(t, err)

	// Verify inference is STARTED
	savedInf, found := k.GetInference(ctx, inferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_STARTED, savedInf.Status)

	// Advance to epoch 2
	newBlockHeight := ctx.BlockTime().UnixMilli() + 10
	ctx = ctx.WithBlockHeight(newBlockHeight)
	epoch2 := types.Epoch{Index: 2, PocStartBlockHeight: newBlockHeight}
	k.SetEpoch(ctx, &epoch2)
	_ = k.SetEffectiveEpochIndex(ctx, 2)
	_ = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      2,
		Participants: []*types.ActiveParticipant{
			{Index: requester.address}, {Index: ta.address}, {Index: executor.address},
		},
	})
	mocks.ExpectCreateGroupWithPolicyCall(ctx, 2)
	eg2, err := k.CreateEpochGroup(ctx, uint64(newBlockHeight), 2)
	require.NoError(t, err)
	require.NoError(t, eg2.CreateGroup(ctx))

	// Create model subgroup for epoch 2
	eg2Current, err := k.GetCurrentEpochGroup(ctx)
	require.NoError(t, err)
	mocks.ExpectAnyCreateGroupWithPolicyCall()
	_, err = eg2Current.CreateSubGroup(ctx, &model)
	require.NoError(t, err)

	// Sign FinishInference
	eaSignature, err := calculations.Sign(executor, taComponents, calculations.ExecutorAgent)
	require.NoError(t, err)

	// Mock: refund SendCoinsFromModuleToAccount FAILS
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(fmt.Errorf("module account has insufficient funds"))

	resp, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		Creator:              executor.address,
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           executor.address,
		TransferredBy:        ta.address,
		RequestTimestamp:      requestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    eaSignature,
		RequestedBy:          requester.address,
		OriginalPrompt:       promptPayload,
		Model:                modelId,
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
	})

	require.NoError(t, err, "handler returns nil error by convention")
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.ErrorMessage, "should have error message for failed refund")

	// KEY ASSERTION: inference must still be STARTED (CacheContext rollback)
	afterInf, found := k.GetInference(sdk.UnwrapSDKContext(ctx), inferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_STARTED, afterInf.Status,
		"inference status must remain STARTED when refund fails -- CacheContext should roll back all mutations")
}
