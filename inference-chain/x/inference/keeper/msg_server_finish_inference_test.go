package keeper_test

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"go.uber.org/mock/gomock"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func advanceEpoch(ctx sdk.Context, k *keeper.Keeper, mocks *keeper2.InferenceMocks, blockHeight int64, epochGroupId uint64) (sdk.Context, error) {
	ctx = ctx.WithBlockHeight(blockHeight)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(10 * 60 * 1000 * 1000)) // 10 minutes later

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return ctx, types.ErrEffectiveEpochNotFound
	}
	newEpoch := types.Epoch{Index: epochIndex + 1, PocStartBlockHeight: blockHeight}
	k.SetEpoch(ctx, &newEpoch)
	k.SetEffectiveEpochIndex(ctx, newEpoch.Index)

	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochGroupId)

	eg, err := k.CreateEpochGroup(ctx, uint64(newEpoch.PocStartBlockHeight), epochIndex)
	if err != nil {
		return ctx, err
	}
	err = eg.CreateGroup(ctx)
	if err != nil {
		return ctx, err
	}

	return ctx, nil
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
	ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochId)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, initialBlockHeight, ctx.BlockHeight())

	expected, err := inferenceHelper.StartInference(
		"promptPayload",
		"model1",
		requestTimestamp,
		keeper.DefaultMaxTokens)
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

	newBlockHeight := initialBlockTime + 10
	ctx, err = advanceEpoch(ctx, &k, inferenceHelper.Mocks, newBlockHeight, 2)
	if err != nil {
		t.Fatalf("Failed to advance epoch: %v", err)
	}
	require.Equal(t, newBlockHeight, ctx.BlockHeight())

	expectedFinished, err := inferenceHelper.FinishInference()
	require.NoError(t, err)

	savedInference, found = k.GetInference(ctx, expected.InferenceId)
	expectedFinished.EpochId = epochId2 // Update the EpochId to the new one
	expectedFinished.EpochPocStartBlockHeight = 0
	savedInference.EpochPocStartBlockHeight = 0
	require.True(t, found)
	require.Equal(t, expectedFinished, &savedInference)

	devStat, found = k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	require.Equal(t, 1, len(devStat.InferenceIds))

	devStatUpdated, found := k.GetDevelopersStatsByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId2,
		InferenceIds: []string{expectedFinished.InferenceId}}, devStatUpdated)

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
	_, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           testutil.Executor,
	})
	require.Error(t, err)
	_, found := k.GetInference(ctx, "inferenceId")
	require.False(t, found)
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
}

func NewMockInferenceHelper(t *testing.T) (*MockInferenceHelper, keeper.Keeper, sdk.Context) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	mocks.StubForInitGenesis(ctx)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	requesterAccount := NewMockAccount(testutil.Requester)
	taAccount := NewMockAccount(testutil.Creator)
	executorAccount := NewMockAccount(testutil.Executor)
	MustAddParticipant(t, ms, ctx, *requesterAccount)
	MustAddParticipant(t, ms, ctx, *taAccount)
	MustAddParticipant(t, ms, ctx, *executorAccount)

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

func (h *MockInferenceHelper) StartInference(
	promptPayload string, model string, requestTimestamp int64, maxTokens uint64) (*types.Inference, error) {
	h.Mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any())
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(h.MockRequester)
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(h.MockTransferAgent)

	components := calculations.SignatureComponents{
		Payload:         promptPayload,
		Timestamp:       requestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: h.MockExecutor.address,
	}
	inferenceId, err := calculations.Sign(h.MockRequester, components, calculations.Developer)
	if err != nil {
		return nil, err
	}
	taSignature, err := calculations.Sign(h.MockTransferAgent, components, calculations.TransferAgent)
	if err != nil {
		return nil, err
	}
	startInferenceMsg := &types.MsgStartInference{
		InferenceId:       inferenceId,
		PromptHash:        "promptHash",
		PromptPayload:     promptPayload,
		RequestedBy:       h.MockRequester.address,
		Creator:           h.MockTransferAgent.address,
		Model:             model,
		OriginalPrompt:    promptPayload,
		RequestTimestamp:  requestTimestamp,
		TransferSignature: taSignature,
		AssignedTo:        h.MockExecutor.address,
	}
	if maxTokens != keeper.DefaultMaxTokens {
		startInferenceMsg.MaxTokens = maxTokens
	}
	_, err = h.MessageServer.StartInference(h.context, startInferenceMsg)
	h.previousInference = &types.Inference{
		Index:               inferenceId,
		InferenceId:         inferenceId,
		PromptHash:          "promptHash",
		PromptPayload:       promptPayload,
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
		OriginalPrompt:      promptPayload,
	}
	return h.previousInference, nil
}

func (h *MockInferenceHelper) FinishInference() (*types.Inference, error) {
	if h.previousInference == nil {
		return nil, types.ErrInferenceNotFound
	}
	h.Mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil)

	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockRequester.GetBechAddress()).Return(h.MockRequester)
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).Return(h.MockTransferAgent)
	h.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), h.MockExecutor.GetBechAddress()).Return(h.MockExecutor)
	components := calculations.SignatureComponents{
		Payload:         h.previousInference.PromptPayload,
		Timestamp:       h.previousInference.RequestTimestamp,
		TransferAddress: h.MockTransferAgent.address,
		ExecutorAddress: h.MockExecutor.address,
	}

	inferenceId, err := calculations.Sign(h.MockRequester, components, calculations.Developer)
	if err != nil {
		return nil, err
	}
	taSignature, err := calculations.Sign(h.MockTransferAgent, components, calculations.TransferAgent)
	if err != nil {
		return nil, err
	}
	eaSignature, err := calculations.Sign(h.MockExecutor, components, calculations.ExecutorAgent)
	if err != nil {
		return nil, err
	}

	h.Mocks.ExpectAnyCreateGroupWithPolicyCall()
	_, err = h.MessageServer.FinishInference(h.context, &types.MsgFinishInference{
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
		OriginalPrompt:       h.previousInference.OriginalPrompt,
	})
	if err != nil {
		return nil, err
	}
	return &types.Inference{
		Index:                    inferenceId,
		InferenceId:              inferenceId,
		PromptHash:               h.previousInference.PromptHash,
		PromptPayload:            h.previousInference.PromptPayload,
		RequestedBy:              h.MockRequester.address,
		Status:                   types.InferenceStatus_FINISHED,
		ResponseHash:             "responseHash",
		ResponsePayload:          "responsePayload",
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
		OriginalPrompt:           h.previousInference.OriginalPrompt,
		ExecutionSignature:       eaSignature,
	}, nil
}
