package keeper_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/calculations"
	inference "github.com/productscience/inference/x/inference/module"
	"go.uber.org/mock/gomock"

	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_OutOfOrderInference(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	mockRequester := NewMockAccount(testutil.Requester)
	mockTransferAgent := NewMockAccount(testutil.Creator)
	mockExecutor := NewMockAccount(testutil.Executor)
	MustAddParticipant(t, ms, ctx, *mockRequester)
	MustAddParticipant(t, ms, ctx, *mockTransferAgent)
	MustAddParticipant(t, ms, ctx, *mockExecutor)

	mocks.StubForInitGenesis(ctx)

	// For escrow calls
	mocks.BankKeeper.ExpectAny(ctx)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), mockRequester.GetBechAddress()).Return(mockRequester).Times(2)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), mockTransferAgent.GetBechAddress()).Return(mockTransferAgent).Times(2)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), mockExecutor.GetBechAddress()).Return(mockExecutor).Times(1)

	// For GranteesByMessageType calls (used by both FinishInference and StartInference)
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()

	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	// Disable grace period for tests so we get actual pricing instead of 0
	params := k.GetParams(ctx)
	params.DynamicPricingParams.GracePeriodEndEpoch = 0
	k.SetParams(ctx, params)

	payload := "promptPayload"
	requestTimestamp := ctx.BlockTime().UnixNano()

	// Phase 3/6: Compute hashes for signatures
	originalPromptHash := sha256Hash(payload)
	promptHash := sha256Hash(payload) // In real flow, would be sha256(canonical request)

	// Phase 3: Dev signs original_prompt_hash
	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: mockTransferAgent.address,
		ExecutorAddress: "", // Dev doesn't include executor
	}
	inferenceId, err := calculations.Sign(mockRequester, devComponents, calculations.Developer)
	require.NoError(t, err)

	// Phase 3: TA and Executor sign prompt_hash
	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: mockTransferAgent.address,
		ExecutorAddress: mockExecutor.address,
	}
	taSignature, err := calculations.Sign(mockTransferAgent, taComponents, calculations.TransferAgent)
	require.NoError(t, err)
	eaSignature, err := calculations.Sign(mockExecutor, taComponents, calculations.ExecutorAgent)
	require.NoError(t, err)

	// First, try to finish an inference that hasn't been started yet
	// With our fix, this should now succeed
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           mockExecutor.address,
		TransferredBy:        mockTransferAgent.address,
		RequestTimestamp:     requestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    eaSignature,
		RequestedBy:          mockRequester.address,
		OriginalPrompt:       payload,
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
		Model:                "model1",
	})
	require.NoError(t, err) // Now this should succeed

	// Verify the inference was created with FINISHED status
	savedInference, found := k.GetInference(ctx, inferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, savedInference.Status)
	require.Equal(t, "responseHash", savedInference.ResponseHash)
	require.Equal(t, "", savedInference.ResponsePayload) // Phase 6: Stored offchain
	require.Equal(t, uint64(10), savedInference.PromptTokenCount)
	require.Equal(t, uint64(20), savedInference.CompletionTokenCount)
	require.Equal(t, testutil.Executor, savedInference.ExecutedBy)

	model := types.Model{Id: "model1"}
	StubModelSubgroup(t, ctx, k, mocks, &model)

	// Now start the inference
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:        inferenceId,
		PromptHash:         promptHash,
		PromptPayload:      payload,
		RequestedBy:        testutil.Requester,
		Creator:            testutil.Creator,
		Model:              "model1",
		OriginalPrompt:     payload,
		OriginalPromptHash: originalPromptHash,
		RequestTimestamp:   requestTimestamp,
		TransferSignature:  taSignature,
		AssignedTo:         testutil.Executor,
	})
	require.NoError(t, err)

	// Verify the inference was updated correctly
	// It should still be in FINISHED state, but now have the start information as well
	savedInference, found = k.GetInference(ctx, inferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, savedInference.Status)
	require.Equal(t, promptHash, savedInference.PromptHash)
	require.Equal(t, "", savedInference.PromptPayload) // Phase 6: Stored offchain
	require.Equal(t, testutil.Requester, savedInference.RequestedBy)
	require.Equal(t, "model1", savedInference.Model)

	// The finish information should still be there
	require.Equal(t, "responseHash", savedInference.ResponseHash)
	require.Equal(t, "", savedInference.ResponsePayload) // Phase 6: Stored offchain
	require.Equal(t, uint64(10), savedInference.PromptTokenCount)
	require.Equal(t, uint64(20), savedInference.CompletionTokenCount)
	require.Equal(t, testutil.Executor, savedInference.ExecutedBy)

	// Verify that the escrow amount is based on the actual token counts, not the MaxTokens
	// The actual cost should be (10 + 20) * PerTokenCost = 30 * PerTokenCost
	expectedActualCost := int64(30 * calculations.PerTokenCost)
	require.Equal(t, expectedActualCost, savedInference.ActualCost)

	// The escrow amount should be the same as the actual cost
	require.Equal(t, expectedActualCost, savedInference.EscrowAmount)
}
