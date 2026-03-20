package keeper_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"go.uber.org/mock/gomock"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// sha256Hash computes SHA256 hash and returns hex string
func sha256Hash(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

func advanceEpoch(ctx sdk.Context, k *keeper.Keeper, mocks *keeper2.InferenceMocks, blockHeight int64, epochGroupId uint64) (sdk.Context, error) {
	ctx = ctx.WithBlockHeight(blockHeight)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(10 * 60 * 1000 * 1000)) // 10 minutes later

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return ctx, types.ErrEffectiveEpochNotFound
	}
	// The genesis groups have already been created
	newEpoch := types.Epoch{Index: epochIndex + 1, PocStartBlockHeight: blockHeight}
	k.SetEpoch(ctx, &newEpoch)
	_ = k.SetEffectiveEpochIndex(ctx, newEpoch.Index)
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochGroupId)

	eg, err := k.CreateEpochGroup(ctx, uint64(newEpoch.PocStartBlockHeight), epochIndex+1)
	if err != nil {
		return ctx, err
	}
	err = eg.CreateGroup(ctx)
	if err != nil {
		return ctx, err
	}
	return ctx, nil
}

func StubModelSubgroup(t *testing.T, ctx context.Context, k keeper.Keeper, mocks *keeper2.InferenceMocks, model *types.Model) {
	eg, err := k.GetCurrentEpochGroup(ctx)
	require.NoError(t, err)
	mocks.ExpectAnyCreateGroupWithPolicyCall()
	_, err = eg.CreateSubGroup(ctx, model)

	require.NoError(t, err)
}

func TestMsgServer_FinishInference_DeveloperAccessRestricted(t *testing.T) {
	const (
		epochId = 1
	)

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)

	// Developer access gating should apply to FinishInference as well (gated by RequestedBy).
	originalParams, err := k.GetParams(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = k.SetParams(ctx, originalParams)
	})

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          9999999,
		AllowedDeveloperAddresses: []string{"gonka1someotherxxxxxxxxxxxxxxxxxxxxxx"},
	}
	_ = k.SetParams(ctx, params)
	participant := types.Participant{
		Address: testutil.Creator,
		Index:   testutil.Creator,
		Status:  types.ParticipantStatus_ACTIVE,
	}
	_ = k.SetParticipant(ctx, participant)
	_ = k.SetEffectiveEpochIndex(ctx, epochId)
	_ = k.SetActiveParticipants(ctx, ParticipantsToActive(epochId, participant))

	resp, err := inferenceHelper.MessageServer.FinishInference(ctx, &types.MsgFinishInference{
		Creator:     testutil.Creator,
		ExecutedBy:  testutil.Creator,
		InferenceId: "dummy",
		RequestedBy: testutil.Requester,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Contains(t, resp.ErrorMessage, types.ErrDeveloperNotAllowlisted.Error())
}

func TestMsgServer_FinishInference(t *testing.T) {
	const (
		epochId  = 1
		epochId2 = 2
	)

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)

	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()
	initialBlockTime := ctx.BlockTime().UnixMilli()
	initialBlockHeight := int64(10)
	// This should advance us to epoch 1 (the first after genesis)
	ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochId)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, initialBlockHeight, ctx.BlockHeight())

	modelId := "model1"
	model := types.Model{Id: modelId}
	k.SetModel(ctx, &model)

	expected, err := inferenceHelper.StartInference(
		"promptPayload",
		modelId,
		requestTimestamp,
		calculations.DefaultMaxTokens)
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, expected, &savedInference)

	_, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId)
	require.False(t, found)

	newBlockHeight := initialBlockTime + 10
	// This should advance us to epoch 2
	ctx, err = advanceEpoch(ctx, &k, inferenceHelper.Mocks, newBlockHeight, epochId2)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, newBlockHeight, ctx.BlockHeight())
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, &model)

	expectedFinished, err := inferenceHelper.FinishInference()
	require.NoError(t, err)

	savedInference, found = k.GetInference(ctx, expected.InferenceId)
	expectedFinished.EpochId = epochId2 // Update the EpochId to the new one
	expectedFinished.EpochPocStartBlockHeight = 0
	savedInference.EpochPocStartBlockHeight = 0
	require.True(t, found)
	require.Equal(t, expectedFinished, &savedInference)

	_, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId2)
	require.False(t, found)

	// Task III: validation-details creation is deferred to EndBlock.
	queuedInferenceIDs, err := k.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.Contains(t, queuedInferenceIDs, expected.InferenceId)

	_, found = k.GetInferenceValidationDetails(ctx, epochId2, expected.InferenceId)
	require.False(t, found)

}

func TestMsgServer_FinishInference_UpdatesExecutorOnceOnCompletion(t *testing.T) {
	inferenceHelper, k, _ := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()

	_, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp, calculations.DefaultMaxTokens)
	require.NoError(t, err)

	beforeExecutor, found := k.GetParticipant(inferenceHelper.context, testutil.Executor)
	require.True(t, found)
	if beforeExecutor.CurrentEpochStats == nil {
		beforeExecutor.CurrentEpochStats = &types.CurrentEpochStats{}
	}
	beforeEarned := beforeExecutor.CurrentEpochStats.EarnedCoins
	beforeInferenceCount := beforeExecutor.CurrentEpochStats.InferenceCount

	expectedFinished, err := inferenceHelper.FinishInference()
	require.NoError(t, err)

	afterExecutor, found := k.GetParticipant(inferenceHelper.context, testutil.Executor)
	require.True(t, found)
	require.NotNil(t, afterExecutor.CurrentEpochStats)
	require.Equal(t, beforeEarned+uint64(expectedFinished.ActualCost), afterExecutor.CurrentEpochStats.EarnedCoins)
	require.Equal(t, beforeInferenceCount+1, afterExecutor.CurrentEpochStats.InferenceCount)
	require.Equal(t, expectedFinished.EndBlockTimestamp, afterExecutor.LastInferenceTime)
}

func TestMsgServer_FinishInference_ParamsCacheDoesNotLeakAcrossCalls(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	err := k.SetEffectiveEpochIndex(ctx, 1) // Set to non-zero epoch to avoid epoch not found error
	require.NoError(t, err)
	err = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: testutil.Creator,
			},
		},
	})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          ctx.BlockHeight() + 100,
		AllowedDeveloperAddresses: []string{testutil.Requester},
	}
	require.NoError(t, k.SetParams(ctx, params))

	AddParticipantToActive(ctx, &k, testutil.Executor, 1)
	firstResp, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId: "cache-test-finish-1",
		RequestedBy: testutil.Requester,
		ExecutedBy:  testutil.Executor,
		Creator:     testutil.Executor,
	})
	require.NoError(t, err)
	require.NotContains(t, firstResp.ErrorMessage, types.ErrDeveloperNotAllowlisted.Error())
	require.Contains(t, firstResp.ErrorMessage, types.ErrParticipantNotFound.Error())

	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight:          ctx.BlockHeight() + 100,
		AllowedDeveloperAddresses: []string{"gonka1notallowlistedxxxxxxxxxxxxxxxxxxxxxx"},
	}
	require.NoError(t, k.SetParams(ctx, params))

	secondResp, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId: "cache-test-finish-2",
		RequestedBy: testutil.Requester,
		ExecutedBy:  testutil.Executor,
		Creator:     testutil.Executor,
	})
	require.NoError(t, err)
	require.Contains(t, secondResp.ErrorMessage, types.ErrDeveloperNotAllowlisted.Error())
}

func MustAddParticipant(t *testing.T, ms types.MsgServer, ctx context.Context, mockAccount MockAccount) {
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      mockAccount.address,
		Url:          "url",
		ValidatorKey: mockAccount.GetPubKey().String(),
	})
	require.NoError(t, err)
}

func TestMsgServer_FinishInference_InferenceNotFound(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	k.SetEffectiveEpochIndex(ctx, 1) // Set to non-zero epoch to avoid epoch not found error
	k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: testutil.Executor,
			},
		},
	})
	response, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		Creator:              testutil.Executor,
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err)
	require.NotEmpty(t, response.ErrorMessage)
	_, found := k.GetInference(ctx, "inferenceId")
	require.False(t, found)
}

func TestMsgServer_FinishInferenceCreatorMustMatchExecutor(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_ = k.SetEffectiveEpochIndex(ctx, 1)
	AddParticipantToActive(ctx, &k, testutil.Creator, 1)
	resp, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		Creator:    testutil.Creator,
		ExecutedBy: testutil.Executor,
	})
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInferenceRoleMismatch.Error())
}

type MockAccount struct {
	address string
	key     *secp256k1.PrivKey
}

func NewMockAccount(address string) *MockAccount {
	return &MockAccount{address: address, key: secp256k1.GenPrivKey()}
}
func (m *MockAccount) GetBechAddress() sdk.AccAddress          { return sdk.MustAccAddressFromBech32(m.address) }
func (m *MockAccount) GetAddress() sdk.AccAddress              { return sdk.AccAddress(m.address) }
func (m *MockAccount) SetAddress(address sdk.AccAddress) error { return nil }
func (m *MockAccount) GetPubKey() cryptotypes.PubKey           { return m.key.PubKey() }
func (m *MockAccount) SetPubKey(key cryptotypes.PubKey) error  { return nil }
func (m *MockAccount) GetAccountNumber() uint64                { return 0 }
func (m *MockAccount) SetAccountNumber(accNumber uint64) error { return nil }
func (m *MockAccount) GetSequence() uint64                     { return 0 }
func (m *MockAccount) SetSequence(sequence uint64) error       { return nil }
func (m *MockAccount) String() string                          { return "" }
func (m *MockAccount) Reset()                                  {}
func (m *MockAccount) ProtoMessage()                           {}
func (m *MockAccount) SignBytes(msg []byte) (string, error) {
	signature, err := m.key.Sign(msg)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

type MockInferenceHelper struct {
	MockRequester     *MockAccount
	MockTransferAgent *MockAccount
	MockExecutor      *MockAccount
	testingT          *testing.T
	Mocks             *keeper2.InferenceMocks
	MessageServer     types.MsgServer
	keeper            *keeper.Keeper
	context           sdk.Context
	previousInference *types.Inference
	promptPayload     string // Phase 6: Store prompt for hash computation (not stored on-chain)
}

func NewMockInferenceHelper(t *testing.T) (*MockInferenceHelper, keeper.Keeper, sdk.Context) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	// Disable grace period for tests so we get actual pricing instead of 0
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DynamicPricingParams.GracePeriodEndEpoch = 0
	k.SetParams(ctx, params)

	requesterAccount := NewMockAccount(testutil.Requester)
	taAccount := NewMockAccount(testutil.Creator)
	executorAccount := NewMockAccount(testutil.Executor)
	MustAddParticipant(t, ms, ctx, *requesterAccount)
	MustAddParticipant(t, ms, ctx, *taAccount)
	MustAddParticipant(t, ms, ctx, *executorAccount)

	currentEpoch, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	err = k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: currentEpoch,
		Participants: []*types.ActiveParticipant{
			{
				Index: requesterAccount.address,
			},
			{
				Index: taAccount.address,
			},
			{
				Index: executorAccount.address,
			},
		},
	})
	require.NoError(t, err)

	return &MockInferenceHelper{
		MockRequester:     requesterAccount,
		MockTransferAgent: taAccount,
		MockExecutor:      executorAccount,
		testingT:          t,
		Mocks:             mocks,
		MessageServer:     ms,
		keeper:            &k,
		context:           ctx,
	}, k, ctx
}

func (h *MockInferenceHelper) EnsureActiveParticipants() {
	currentEpoch, found := h.keeper.GetEffectiveEpochIndex(h.context)
	require.True(h.testingT, found)
	err := h.keeper.SetActiveParticipants(h.context, types.ActiveParticipants{
		EpochId: currentEpoch,
		Participants: []*types.ActiveParticipant{
			{
				Index: h.MockRequester.address,
			},
			{
				Index: h.MockTransferAgent.address,
			},
			{
				Index: h.MockExecutor.address,
			},
			{
				Index: testutil.Validator,
			},
		},
	})
	require.NoError(h.testingT, err)
}
func (h *MockInferenceHelper) StartInference(
	promptPayload string, model string, requestTimestamp int64, maxTokens uint64) (*types.Inference, error) {
	h.Mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)
	h.Mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(true).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(h.MockRequester)
	h.Mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(true).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(h.MockTransferAgent).AnyTimes()
	h.Mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()
	h.EnsureActiveParticipants()

	// Phase 3: Compute hashes for signatures
	originalPromptHash := sha256Hash(promptPayload)
	promptHash := sha256Hash(promptPayload) // In real flow, this would be sha256(modified request with seed)

	// Phase 3: Dev signs original_prompt_hash (no executor address)
	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: "", // Dev doesn't include executor
	}
	inferenceId, err := calculations.Sign(h.MockRequester, devComponents, calculations.Developer)
	if err != nil {
		return nil, err
	}

	// Phase 3: TA signs prompt_hash (with executor address)
	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: h.MockExecutor.address,
	}
	taSignature, err := calculations.Sign(h.MockTransferAgent, taComponents, calculations.TransferAgent)
	if err != nil {
		return nil, err
	}
	startInferenceMsg := &types.MsgStartInference{
		InferenceId:        inferenceId,
		PromptHash:         promptHash,
		PromptPayload:      promptPayload,
		RequestedBy:        h.MockRequester.address,
		Creator:            h.MockTransferAgent.address,
		Model:              model,
		OriginalPrompt:     promptPayload,
		OriginalPromptHash: originalPromptHash,
		RequestTimestamp:   requestTimestamp,
		TransferSignature:  taSignature,
		AssignedTo:         h.MockExecutor.address,
	}
	if maxTokens != calculations.DefaultMaxTokens {
		startInferenceMsg.MaxTokens = maxTokens
	}
	_, err = h.MessageServer.StartInference(h.context, startInferenceMsg)
	h.promptPayload = promptPayload // Phase 6: Store for hash computation in FinishInference
	h.previousInference = &types.Inference{
		Index:               inferenceId,
		InferenceId:         inferenceId,
		PromptHash:          promptHash,
		OriginalPromptHash:  originalPromptHash,
		PromptPayload:       "", // Phase 6: Stored offchain
		RequestedBy:         h.MockRequester.address,
		Status:              types.InferenceStatus_STARTED,
		Model:               model,
		StartBlockHeight:    h.context.BlockHeight(),
		StartBlockTimestamp: h.context.BlockTime().UnixMilli(),
		MaxTokens:           maxTokens,
		EscrowAmount:        int64(maxTokens * calculations.PerTokenCost),
		AssignedTo:          h.MockExecutor.address,
		TransferredBy:       h.MockTransferAgent.address,
		TransferSignature:   taSignature,
		RequestTimestamp:    requestTimestamp,
		OriginalPrompt:      "",                        // Phase 6: Stored offchain
		PerTokenPrice:       calculations.PerTokenCost, // Set expected dynamic pricing value
	}
	return h.previousInference, err
}

func (h *MockInferenceHelper) FinishInference() (*types.Inference, error) {
	if h.previousInference == nil {
		return nil, types.ErrInferenceNotFound
	}
	h.Mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	h.Mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(true).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(h.MockRequester).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(true).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(h.MockTransferAgent).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), h.MockExecutor.GetBechAddress()).Return(true).AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockExecutor.GetBechAddress()).Return(h.MockExecutor).AnyTimes()
	h.EnsureActiveParticipants()

	// Phase 3: Compute hashes for signatures
	// Phase 6: Use stored promptPayload (not from inference struct, which is now empty)
	originalPromptHash := sha256Hash(h.promptPayload)
	promptHash := h.previousInference.PromptHash // Already computed in StartInference

	// Phase 3: Dev signs original_prompt_hash (no executor address)
	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       h.previousInference.RequestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: "", // Dev doesn't include executor
	}
	inferenceId, err := calculations.Sign(h.MockRequester, devComponents, calculations.Developer)
	if err != nil {
		return nil, err
	}

	// Phase 3: TA and Executor sign prompt_hash (with executor address)
	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       h.previousInference.RequestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: h.MockExecutor.address,
	}
	taSignature, err := calculations.Sign(h.MockTransferAgent, taComponents, calculations.TransferAgent)
	if err != nil {
		return nil, err
	}
	eaSignature, err := calculations.Sign(h.MockExecutor, taComponents, calculations.ExecutorAgent)
	if err != nil {
		return nil, err
	}

	_, err = h.MessageServer.FinishInference(h.context, &types.MsgFinishInference{
		Creator:              h.MockExecutor.address,
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           h.MockExecutor.address,
		TransferredBy:        h.MockTransferAgent.address,
		RequestTimestamp:     h.previousInference.RequestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    eaSignature,
		RequestedBy:          h.MockRequester.address,
		OriginalPrompt:       h.promptPayload, // Phase 6: Use stored prompt (not from inference struct)
		Model:                h.previousInference.Model,
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
	})
	if err != nil {
		return nil, err
	}
	return &types.Inference{
		Index:                    inferenceId,
		InferenceId:              inferenceId,
		PromptHash:               h.previousInference.PromptHash,
		OriginalPromptHash:       originalPromptHash,
		PromptPayload:            "", // Phase 6: Stored offchain
		RequestedBy:              h.MockRequester.address,
		Status:                   types.InferenceStatus_FINISHED,
		ResponseHash:             "responseHash",
		ResponsePayload:          "", // Phase 6: Stored offchain
		PromptTokenCount:         10,
		CompletionTokenCount:     20,
		EpochPocStartBlockHeight: h.previousInference.EpochPocStartBlockHeight,
		EpochId:                  h.previousInference.EpochId + 1,
		ExecutedBy:               h.MockExecutor.address,
		Model:                    h.previousInference.Model,
		StartBlockTimestamp:      h.previousInference.StartBlockTimestamp,
		StartBlockHeight:         h.previousInference.StartBlockHeight,
		EndBlockTimestamp:        h.context.BlockTime().UnixMilli(),
		EndBlockHeight:           h.context.BlockHeight(),
		MaxTokens:                h.previousInference.MaxTokens,
		EscrowAmount:             int64(h.previousInference.MaxTokens * calculations.PerTokenCost),
		ActualCost:               30 * calculations.PerTokenCost,
		AssignedTo:               h.previousInference.AssignedTo,
		TransferredBy:            h.previousInference.TransferredBy,
		TransferSignature:        h.previousInference.TransferSignature,
		RequestTimestamp:         h.previousInference.RequestTimestamp,
		OriginalPrompt:           "", // Phase 6: Stored offchain
		ExecutionSignature:       eaSignature,
		PerTokenPrice:            calculations.PerTokenCost, // Set expected dynamic pricing value
	}, nil
}
