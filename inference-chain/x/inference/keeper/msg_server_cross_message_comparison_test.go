package keeper_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"

	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// --- Helpers ---

func bogusSig() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 64))
}

type crossMsgTestSetup struct {
	helper           *MockInferenceHelper
	requestTimestamp int64
	originalHash     string
	promptHash       string
	inferenceID      string
	taSignature      string
	executorSig      string
}

func newCrossMsgSetup(t *testing.T) crossMsgTestSetup {
	t.Helper()
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)

	model := types.Model{Id: "model1"}
	k.SetModel(ctx, &model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, &model)

	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()
	originalHash, promptHash, inferenceID, taSig, execSig := buildInferenceSignatures(
		t,
		inferenceHelper.MockRequester,
		inferenceHelper.MockTransferAgent,
		inferenceHelper.MockExecutor,
		"promptPayload",
		requestTimestamp,
	)

	inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), inferenceHelper.MockRequester.GetBechAddress()).Return(inferenceHelper.MockRequester).AnyTimes()
	inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), inferenceHelper.MockTransferAgent.GetBechAddress()).Return(inferenceHelper.MockTransferAgent).AnyTimes()
	inferenceHelper.Mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()
	inferenceHelper.Mocks.BankKeeper.ExpectAny(inferenceHelper.context)

	return crossMsgTestSetup{
		helper:           inferenceHelper,
		requestTimestamp: requestTimestamp,
		originalHash:     originalHash,
		promptHash:       promptHash,
		inferenceID:      inferenceID,
		taSignature:      taSig,
		executorSig:      execSig,
	}
}

func buildInferenceSignatures(
	t *testing.T,
	requester *MockAccount,
	transferAgent *MockAccount,
	executor *MockAccount,
	promptPayload string,
	requestTimestamp int64,
) (string, string, string, string, string) {
	t.Helper()

	originalPromptHash := sha256Hash(promptPayload)
	promptHash := sha256Hash(promptPayload)

	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: transferAgent.address,
		ExecutorAddress: "",
	}
	inferenceId, err := calculations.Sign(requester, devComponents, calculations.Developer)
	require.NoError(t, err)

	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: transferAgent.address,
		ExecutorAddress: executor.address,
	}
	taSignature, err := calculations.Sign(transferAgent, taComponents, calculations.TransferAgent)
	require.NoError(t, err)
	executorSignature, err := calculations.Sign(executor, taComponents, calculations.ExecutorAgent)
	require.NoError(t, err)

	return originalPromptHash, promptHash, inferenceId, taSignature, executorSignature
}

func (s *crossMsgTestSetup) validFinishMsg() *types.MsgFinishInference {
	return &types.MsgFinishInference{
		Creator:              s.helper.MockExecutor.address,
		InferenceId:          s.inferenceID,
		ResponseHash:         "responseHash",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           s.helper.MockExecutor.address,
		TransferredBy:        s.helper.MockTransferAgent.address,
		RequestTimestamp:     s.requestTimestamp,
		TransferSignature:    s.taSignature,
		ExecutorSignature:    s.executorSig,
		RequestedBy:          s.helper.MockRequester.address,
		PromptHash:           s.promptHash,
		OriginalPromptHash:   s.originalHash,
		Model:                "model1",
	}
}

func (s *crossMsgTestSetup) validStartMsg() *types.MsgStartInference {
	return &types.MsgStartInference{
		InferenceId:        s.inferenceID,
		PromptHash:         s.promptHash,
		RequestedBy:        s.helper.MockRequester.address,
		Creator:            s.helper.MockTransferAgent.address,
		Model:              "model1",
		OriginalPromptHash: s.originalHash,
		RequestTimestamp:   s.requestTimestamp,
		TransferSignature:  s.taSignature,
		AssignedTo:         s.helper.MockExecutor.address,
		MaxTokens:          20,
	}
}

func (s *crossMsgTestSetup) mustFinishFirst(t *testing.T) {
	t.Helper()
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, s.validFinishMsg())
	require.NoError(t, err)
	require.Empty(t, resp.ErrorMessage)
}

func (s *crossMsgTestSetup) mustStartFirst(t *testing.T) {
	t.Helper()
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, s.validStartMsg())
	require.NoError(t, err)
	require.Empty(t, resp.ErrorMessage)
}

// ========================
// Happy path (both orders)
// ========================

func TestCrossMsg_FinishFirst_ThenStart_Succeeds(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)
	s.mustStartFirst(t)
}

func TestCrossMsg_StartFirst_ThenFinish_Succeeds(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, s.validFinishMsg())
	require.NoError(t, err)
	require.Empty(t, resp.ErrorMessage)
}

// ================================
// Dev signature on first message
// ================================

func TestCrossMsg_StartFirst_BadDevSignature_Rejected(t *testing.T) {
	s := newCrossMsgSetup(t)
	msg := s.validStartMsg()
	msg.InferenceId = bogusSig() // dev signature is the inferenceId
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInvalidSignature.Error())
}

func TestCrossMsg_FinishFirst_BadDevSignature_Rejected(t *testing.T) {
	s := newCrossMsgSetup(t)
	msg := s.validFinishMsg()
	msg.InferenceId = bogusSig()
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInvalidSignature.Error())
}

// ========================================
// TA signature on finish-first (verified)
// ========================================

func TestCrossMsg_FinishFirst_BadTASignature_Rejected(t *testing.T) {
	s := newCrossMsgSetup(t)
	msg := s.validFinishMsg()
	msg.TransferSignature = bogusSig()
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInvalidSignature.Error())
}

// TA signature is NOT checked on start-first
// When finish comes in we only care about field correctness
func TestCrossMsg_StartFirst_BadTASignature_Accepted(t *testing.T) {
	s := newCrossMsgSetup(t)
	msg := s.validStartMsg()
	msg.TransferSignature = bogusSig()
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Empty(t, resp.ErrorMessage)

	msgF := s.validFinishMsg()
	msgF.TransferSignature = bogusSig()
	respF, err := s.helper.MessageServer.FinishInference(s.helper.context, msgF)
	require.NoError(t, err)
	require.Empty(t, respF.ErrorMessage)
}

// ================================================
// Executor signature is NEVER checked (both paths)
// ================================================

func TestCrossMsg_FinishFirst_BadExecutorSignature_Accepted(t *testing.T) {
	s := newCrossMsgSetup(t)
	msg := s.validFinishMsg()
	msg.ExecutorSignature = bogusSig()
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Empty(t, resp.ErrorMessage)
}

// ============================================================
// Dev component mismatch on second message (both orderings)
// ============================================================

func TestCrossMsg_FinishFirst_StartSecond_OriginalPromptHashMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)

	msg := s.validStartMsg()
	msg.OriginalPromptHash = sha256Hash("different_prompt")
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrDevComponentMismatch.Error())
}

func TestCrossMsg_FinishFirst_StartSecond_RequestedByMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)

	other := NewMockAccount(testutil.Executor2)
	MustAddParticipant(t, s.helper.MessageServer, s.helper.context, *other)

	msg := s.validStartMsg()
	msg.RequestedBy = other.address
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrDevComponentMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_OriginalPromptHashMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	msg := s.validFinishMsg()
	msg.OriginalPromptHash = sha256Hash("different_prompt")
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrDevComponentMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_RequestedByMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	other := NewMockAccount(testutil.Executor2)
	MustAddParticipant(t, s.helper.MessageServer, s.helper.context, *other)

	msg := s.validFinishMsg()
	msg.RequestedBy = other.address
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrDevComponentMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_RequestTimestampMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	msg := s.validFinishMsg()
	msg.RequestTimestamp = s.requestTimestamp + 999
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrDevComponentMismatch.Error())
}

// =========================================================
// TA component mismatch on second message (both orderings)
// =========================================================

func TestCrossMsg_FinishFirst_StartSecond_PromptHashMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)

	msg := s.validStartMsg()
	msg.PromptHash = sha256Hash("different_prompt")
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrTAComponentMismatch.Error())
}

func TestCrossMsg_FinishFirst_StartSecond_ExecutorMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)

	other := NewMockAccount(testutil.Executor2)
	MustAddParticipant(t, s.helper.MessageServer, s.helper.context, *other)

	msg := s.validStartMsg()
	msg.AssignedTo = other.address
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrTAComponentMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_PromptHashMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	msg := s.validFinishMsg()
	msg.PromptHash = sha256Hash("different_prompt")
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrTAComponentMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_ExecutorMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	other := NewMockAccount(testutil.Executor2)
	MustAddParticipant(t, s.helper.MessageServer, s.helper.context, *other)
	AddParticipantToActive(s.helper.context, s.helper.keeper, other.address, 0)

	msg := s.validFinishMsg()
	msg.ExecutedBy = other.address
	msg.Creator = other.address // creator must == executed_by
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrTAComponentMismatch.Error())
}

func AddParticipantToActive(ctx context.Context, k *keeper.Keeper, address string, epochIndex uint64) {
	current, found := k.GetActiveParticipants(ctx, epochIndex)
	var currentParticipants []*types.ActiveParticipant
	if found {
		currentParticipants = current.Participants
	} else {
		currentParticipants = []*types.ActiveParticipant{}
	}
	currentParticipants = append(currentParticipants, &types.ActiveParticipant{
		Index: address,
	})
	newParticipants := types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: currentParticipants,
	}
	err := k.SetActiveParticipants(ctx, newParticipants)
	if err != nil {
		panic(err)
	}
}

// =============================================
// Model mismatch on second message (both ways)
// =============================================

func TestCrossMsg_FinishFirst_StartSecond_ModelMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustFinishFirst(t)

	msg := s.validStartMsg()
	msg.Model = "different_model"
	resp, err := s.helper.MessageServer.StartInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInferenceRoleMismatch.Error())
}

func TestCrossMsg_StartFirst_FinishSecond_ModelMismatch(t *testing.T) {
	s := newCrossMsgSetup(t)
	s.mustStartFirst(t)

	msg := s.validFinishMsg()
	msg.Model = "different_model"
	resp, err := s.helper.MessageServer.FinishInference(s.helper.context, msg)
	require.NoError(t, err)
	require.Contains(t, resp.ErrorMessage, types.ErrInferenceRoleMismatch.Error())
}
