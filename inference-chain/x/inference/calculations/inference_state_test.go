package calculations

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// MockInferenceLogger is a mock implementation of the InferenceLogger interface
type MockInferenceLogger struct{}

func (m *MockInferenceLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
}
func (m *MockInferenceLogger) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
}
func (m *MockInferenceLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
}
func (m *MockInferenceLogger) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
}

func TestStartProcessed(t *testing.T) {
	tests := []struct {
		name      string
		inference *types.Inference
		expected  bool
	}{
		{
			name:      "Empty inference",
			inference: &types.Inference{},
			expected:  false,
		},
		{
			name: "Inference with PromptHash",
			inference: &types.Inference{
				PromptHash: "hash",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := startProcessed(tt.inference)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFinishedProcessed(t *testing.T) {
	tests := []struct {
		name      string
		inference *types.Inference
		expected  bool
	}{
		{
			name:      "Empty inference",
			inference: &types.Inference{},
			expected:  false,
		},
		{
			name: "Inference with ExecutedBy",
			inference: &types.Inference{
				ExecutedBy: "executor",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := finishedProcessed(tt.inference)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetMaxTokens(t *testing.T) {
	tests := []struct {
		name     string
		msg      *types.MsgStartInference
		expected uint64
	}{
		{
			name:     "Empty message",
			msg:      &types.MsgStartInference{},
			expected: DefaultMaxTokens,
		},
		{
			name: "Message with MaxTokens",
			msg: &types.MsgStartInference{
				MaxTokens: 1000,
			},
			expected: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getMaxTokens(tt.msg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name      string
		inference *types.Inference
		expected  int64
	}{
		{
			name:      "Empty inference",
			inference: &types.Inference{},
			expected:  0,
		},
		{
			name: "Legacy pricing - inference with tokens",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			expected: 30 * PerTokenCost,
		},
		{
			name: "Dynamic pricing - inference with custom per-token price",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        500,          // Custom dynamic price
				Model:                "test-model", // Complete inference object
			},
			expected: 30 * 500, // Should use dynamic price instead of legacy PerTokenCost
		},
		{
			name: "Dynamic pricing - zero price (grace period)",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				PerTokenPrice:        0,            // Free inference during grace period
				Model:                "test-model", // Complete inference object
			},
			expected: 0, // Should be free
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateCost(tt.inference)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateEscrow(t *testing.T) {
	tests := []struct {
		name         string
		inference    *types.Inference
		promptTokens uint64
		expected     int64
	}{
		{
			name:         "Empty inference",
			inference:    &types.Inference{},
			promptTokens: 0,
			expected:     0,
		},
		{
			name: "Legacy pricing - inference with MaxTokens",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			promptTokens: 50,
			expected:     150 * PerTokenCost,
		},
		{
			name: "Dynamic pricing - inference with custom per-token price",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: 750,          // Custom dynamic price
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 50,
			expected:     150 * 750, // Should use dynamic price
		},
		{
			name: "Dynamic pricing - zero price (grace period)",
			inference: &types.Inference{
				MaxTokens:     100,
				PerTokenPrice: 0,            // Free inference during grace period
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 50,
			expected:     0, // Should be free
		},
		{
			name: "Dynamic pricing - high utilization scenario",
			inference: &types.Inference{
				MaxTokens:     200,
				PerTokenPrice: 1200,         // High price due to network congestion
				Model:         "test-model", // Complete inference object
			},
			promptTokens: 100,
			expected:     300 * 1200, // Should use high dynamic price
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateEscrow(tt.inference, tt.promptTokens)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSetEscrowForFinished(t *testing.T) {
	tests := []struct {
		name            string
		inference       *types.Inference
		escrowAmount    int64
		payments        *Payments
		expectedActual  int64
		expectedEscrow  int64
		expectedPayment int64
	}{
		{
			name: "Actual cost less than escrow",
			inference: &types.Inference{
				PromptTokenCount:     10,
				CompletionTokenCount: 10,
				PerTokenPrice:        PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			escrowAmount:    30 * PerTokenCost,
			payments:        &Payments{},
			expectedActual:  20 * PerTokenCost,
			expectedEscrow:  20 * PerTokenCost,
			expectedPayment: 20 * PerTokenCost,
		},
		{
			name: "Actual cost more than escrow",
			inference: &types.Inference{
				PromptTokenCount:     20,
				CompletionTokenCount: 20,
				PerTokenPrice:        PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			escrowAmount:    30 * PerTokenCost,
			payments:        &Payments{},
			expectedActual:  30 * PerTokenCost,
			expectedEscrow:  30 * PerTokenCost,
			expectedPayment: 30 * PerTokenCost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEscrowForFinished(tt.inference, tt.escrowAmount, tt.payments)
			assert.Equal(t, tt.expectedActual, tt.inference.ActualCost)
			assert.Equal(t, tt.expectedEscrow, tt.payments.EscrowAmount)
			assert.Equal(t, tt.expectedPayment, tt.payments.ExecutorPayment)
		})
	}
}

func TestProcessStartInference(t *testing.T) {
	mockLogger := &MockInferenceLogger{}

	tests := []struct {
		name             string
		currentInference *types.Inference
		startMessage     *types.MsgStartInference
		blockContext     BlockContext
		expectError      bool
		expectedStatus   types.InferenceStatus
	}{
		{
			name:             "Nil inference",
			currentInference: nil,
			startMessage:     &types.MsgStartInference{InferenceId: "test-id"},
			blockContext:     BlockContext{},
			expectError:      true,
		},
		{
			name: "Existing inference from startInference (not from finishedInference)",
			currentInference: &types.Inference{
				InferenceId: "test-id",
				PromptHash:  "hash",
			},
			startMessage: &types.MsgStartInference{InferenceId: "test-id"},
			blockContext: BlockContext{},
			expectError:  true,
		},
		{
			name:             "New inference",
			currentInference: &types.Inference{},
			startMessage: &types.MsgStartInference{
				InferenceId:      "test-id",
				PromptHash:       "hash",
				PromptTokenCount: 10,
				RequestedBy:      "requester",
				Model:            "model",
				MaxTokens:        100,
				AssignedTo:       "assignee",
				NodeVersion:      "1.0",
			},
			blockContext: BlockContext{
				BlockHeight:    100,
				BlockTimestamp: 1000,
			},
			expectError:    false,
			expectedStatus: types.InferenceStatus_STARTED,
		},
		{
			name: "Finished inference",
			currentInference: &types.Inference{
				InferenceId: "test-id",
				ExecutedBy:  "executor",
			},
			startMessage: &types.MsgStartInference{
				InferenceId:      "test-id",
				PromptHash:       "hash",
				PromptTokenCount: 10,
				RequestedBy:      "requester",
				Model:            "model",
				MaxTokens:        100,
				AssignedTo:       "assignee",
				NodeVersion:      "1.0",
			},
			blockContext: BlockContext{
				BlockHeight:    100,
				BlockTimestamp: 1000,
			},
			expectError:    false,
			expectedStatus: types.InferenceStatus_STARTED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inference, payments, err := ProcessStartInference(
				tt.currentInference,
				tt.startMessage,
				tt.blockContext,
				mockLogger,
			)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, inference)
			assert.NotNil(t, payments)
			assert.Equal(t, tt.expectedStatus, inference.Status)
			assert.Equal(t, tt.startMessage.InferenceId, inference.InferenceId)
			assert.Equal(t, tt.startMessage.PromptHash, inference.PromptHash)
			// Phase 6: PromptPayload no longer stored on-chain
			// PromptTokenCount is not set in ProcessStartInference anymore - only used for escrow calculation
			// Real token count is set in ProcessFinishInference
			assert.Equal(t, tt.startMessage.RequestedBy, inference.RequestedBy)
			assert.Equal(t, tt.startMessage.Model, inference.Model)
			assert.Equal(t, tt.blockContext.BlockHeight, inference.StartBlockHeight)
			assert.Equal(t, tt.blockContext.BlockTimestamp, inference.StartBlockTimestamp)
			assert.Equal(t, tt.startMessage.AssignedTo, inference.AssignedTo)
			assert.Equal(t, tt.startMessage.NodeVersion, inference.NodeVersion)
		})
	}
}

func TestProcessFinishInference(t *testing.T) {
	mockLogger := &MockInferenceLogger{}

	tests := []struct {
		name             string
		currentInference *types.Inference
		finishMessage    *types.MsgFinishInference
		blockContext     BlockContext
		expectedStatus   types.InferenceStatus
	}{
		{
			name: "New inference from finish",
			currentInference: &types.Inference{
				PerTokenPrice: PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			finishMessage: &types.MsgFinishInference{
				InferenceId:          "test-id",
				ResponseHash:         "hash",
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				ExecutedBy:           "executor",
			},
			blockContext: BlockContext{
				BlockHeight:    100,
				BlockTimestamp: 1000,
			},
			expectedStatus: types.InferenceStatus_FINISHED,
		},
		{
			name: "Existing inference",
			currentInference: &types.Inference{
				InferenceId:   "test-id",
				PromptHash:    "hash",
				EscrowAmount:  50 * PerTokenCost,
				PerTokenPrice: PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			finishMessage: &types.MsgFinishInference{
				InferenceId:          "test-id",
				ResponseHash:         "hash",
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				ExecutedBy:           "executor",
			},
			blockContext: BlockContext{
				BlockHeight:    100,
				BlockTimestamp: 1000,
			},
			expectedStatus: types.InferenceStatus_FINISHED,
		},
		{
			name: "Zero prompt token count",
			currentInference: &types.Inference{
				InferenceId:      "test-id",
				PromptHash:       "hash",
				PromptTokenCount: 15,
				PerTokenPrice:    PerTokenCost, // Simulate dynamic pricing setting legacy fallback
			},
			finishMessage: &types.MsgFinishInference{
				InferenceId:          "test-id",
				ResponseHash:         "hash",
				PromptTokenCount:     0,
				CompletionTokenCount: 20,
				ExecutedBy:           "executor",
			},
			blockContext: BlockContext{
				BlockHeight:    100,
				BlockTimestamp: 1000,
			},
			expectedStatus: types.InferenceStatus_FINISHED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inference, payments := ProcessFinishInference(
				tt.currentInference,
				tt.finishMessage,
				tt.blockContext,
				mockLogger,
			)

			assert.NotNil(t, inference)
			assert.NotNil(t, payments)
			assert.Equal(t, tt.expectedStatus, inference.Status)
			assert.Equal(t, tt.finishMessage.InferenceId, inference.InferenceId)
			assert.Equal(t, tt.finishMessage.ResponseHash, inference.ResponseHash)
			// Phase 6: ResponsePayload no longer stored on-chain

			// Check if PromptTokenCount is preserved when finishMessage has zero
			if tt.finishMessage.PromptTokenCount == 0 && tt.currentInference.PromptTokenCount > 0 {
				assert.Equal(t, tt.currentInference.PromptTokenCount, inference.PromptTokenCount)
			} else {
				assert.Equal(t, tt.finishMessage.PromptTokenCount, inference.PromptTokenCount)
			}

			assert.Equal(t, tt.finishMessage.CompletionTokenCount, inference.CompletionTokenCount)
			assert.Equal(t, tt.finishMessage.ExecutedBy, inference.ExecutedBy)
			assert.Equal(t, tt.blockContext.BlockHeight, inference.EndBlockHeight)
			assert.Equal(t, tt.blockContext.BlockTimestamp, inference.EndBlockTimestamp)

			// Verify ActualCost calculation
			expectedCost := int64((inference.PromptTokenCount + inference.CompletionTokenCount) * PerTokenCost)
			assert.Equal(t, expectedCost, inference.ActualCost)
		})
	}
}
