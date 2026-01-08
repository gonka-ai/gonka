package tx_manager

import (
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestClassifyBroadcastResponse(t *testing.T) {
	tests := []struct {
		name     string
		resp     *sdk.TxResponse
		expected TxResponseAction
	}{
		{
			name:     "nil response should retry",
			resp:     nil,
			expected: TxActionRetry,
		},
		{
			name: "Code 0 (success) should observe",
			resp: &sdk.TxResponse{
				Code:   0,
				TxHash: "ABC123",
			},
			expected: TxActionObserve,
		},
		{
			name: "Code 19 (tx already in mempool) should observe",
			resp: &sdk.TxResponse{
				Code:   19,
				RawLog: "tx already in mempool",
			},
			expected: TxActionObserve,
		},
		{
			name: "Code 20 (mempool full) should retry",
			resp: &sdk.TxResponse{
				Code:   20,
				RawLog: "mempool is full",
			},
			expected: TxActionRetry,
		},
		{
			name: "Code 1143 (ErrDuplicateValidation) should fail",
			resp: &sdk.TxResponse{
				Code:      1143,
				Codespace: "inference",
				RawLog:    "participant has already validated this inference",
			},
			expected: TxActionFail,
		},
		{
			name: "Code 1103 (ErrParticipantNotFound) should fail",
			resp: &sdk.TxResponse{
				Code:      1103,
				Codespace: "inference",
				RawLog:    "participant not found",
			},
			expected: TxActionFail,
		},
		{
			name: "Code 1146 (ErrInferenceFinishProcessed) should fail",
			resp: &sdk.TxResponse{
				Code:      1146,
				Codespace: "inference",
				RawLog:    "inference has already finished processed",
			},
			expected: TxActionFail,
		},
		{
			name: "Code 1147 (ErrInferenceStartProcessed) should fail",
			resp: &sdk.TxResponse{
				Code:      1147,
				Codespace: "inference",
				RawLog:    "inference has already started processed",
			},
			expected: TxActionFail,
		},
		{
			name: "Unknown code with connection refused in RawLog should retry",
			resp: &sdk.TxResponse{
				Code:   99,
				RawLog: "connection refused: dial tcp 127.0.0.1:26657",
			},
			expected: TxActionRetry,
		},
		{
			name: "Unknown code with timeout in RawLog should retry",
			resp: &sdk.TxResponse{
				Code:   99,
				RawLog: "i/o timeout",
			},
			expected: TxActionRetry,
		},
		{
			name: "Unknown code with non-retryable RawLog should fail",
			resp: &sdk.TxResponse{
				Code:   99,
				RawLog: "some unknown error",
			},
			expected: TxActionFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyBroadcastResponse(tt.resp)
			if result != tt.expected {
				t.Errorf("classifyBroadcastResponse() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsRetryableRawLog(t *testing.T) {
	tests := []struct {
		name     string
		rawLog   string
		expected bool
	}{
		// Network/transport errors - should be retryable
		{name: "connection refused", rawLog: "connection refused", expected: true},
		{name: "connection reset", rawLog: "connection reset by peer", expected: true},
		{name: "i/o timeout", rawLog: "read tcp: i/o timeout", expected: true},
		{name: "context deadline exceeded", rawLog: "context deadline exceeded", expected: true},
		{name: "broken pipe", rawLog: "write: broken pipe", expected: true},
		{name: "eof", rawLog: "unexpected eof", expected: true},
		{name: "no such host", rawLog: "dial tcp: no such host", expected: true},
		{name: "network is unreachable", rawLog: "network is unreachable", expected: true},
		{name: "no route to host", rawLog: "no route to host", expected: true},
		{name: "certificate error", rawLog: "x509: certificate signed by unknown authority", expected: true},

		// HTTP gateway errors - should be retryable
		{name: "post failed", rawLog: "post failed: connection refused", expected: true},
		{name: "bad gateway", rawLog: "bad gateway", expected: true},
		{name: "service unavailable", rawLog: "service unavailable", expected: true},
		{name: "gateway timeout", rawLog: "gateway timeout", expected: true},

		// RPC errors - should be retryable
		{name: "rpc error", rawLog: "rpc error: code = Unavailable", expected: true},
		{name: "aborted", rawLog: "transaction aborted", expected: true},

		// OS resource errors - should be retryable
		{name: "too many open files", rawLog: "too many open files", expected: true},

		// Cosmos SDK transient errors - should be retryable
		{name: "mempool is full", rawLog: "mempool is full", expected: true},

		// Sequence errors - should be retryable
		{name: "account sequence mismatch", rawLog: "account sequence mismatch, expected 5, got 4", expected: true},
		{name: "incorrect account sequence", rawLog: "incorrect account sequence", expected: true},

		// Business logic errors - should NOT be retryable
		{name: "participant not found", rawLog: "participant not found", expected: false},
		{name: "duplicate validation", rawLog: "participant has already validated this inference", expected: false},
		{name: "inference not found", rawLog: "inference with id not found", expected: false},
		{name: "inference already processed", rawLog: "inference has already finished processed", expected: false},
		{name: "invalid signature", rawLog: "invalid keys provided for StartInference", expected: false},
		{name: "empty string", rawLog: "", expected: false},
		{name: "random error", rawLog: "something went wrong", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableRawLog(tt.rawLog)
			if result != tt.expected {
				t.Errorf("isRetryableRawLog(%q) = %v, want %v", tt.rawLog, result, tt.expected)
			}
		})
	}
}

func TestIsRetryableBroadcastError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error should not be retryable",
			err:      nil,
			expected: false,
		},
		{
			name:     "connection refused error should be retryable",
			err:      errors.New("dial tcp 127.0.0.1:26657: connection refused"),
			expected: true,
		},
		{
			name:     "timeout error should be retryable",
			err:      errors.New("context deadline exceeded"),
			expected: true,
		},
		{
			name:     "mempool full error should be retryable",
			err:      errors.New("mempool is full"),
			expected: true,
		},
		{
			name:     "business logic error should not be retryable",
			err:      errors.New("participant not found"),
			expected: false,
		},
		{
			name:     "unknown error should not be retryable",
			err:      errors.New("something unexpected happened"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableBroadcastError(tt.err)
			if result != tt.expected {
				t.Errorf("isRetryableBroadcastError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestIsTxErrorCritical(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "ErrBuildingUnsignedTx should be critical",
			err:      ErrBuildingUnsignedTx,
			expected: true,
		},
		{
			name:     "ErrFailedToSignTx should be critical",
			err:      ErrFailedToSignTx,
			expected: true,
		},
		{
			name:     "ErrFailedToEncodeTx should be critical",
			err:      ErrFailedToEncodeTx,
			expected: true,
		},
		{
			name:     "tx too large should be critical",
			err:      errors.New("tx too large"),
			expected: true,
		},
		{
			name:     "key not found should be critical",
			err:      errors.New("key not found"),
			expected: true,
		},
		{
			name:     "invalid bech32 string should be critical",
			err:      errors.New("invalid bech32 string"),
			expected: true,
		},
		{
			name:     "random error should not be critical",
			err:      errors.New("some random error"),
			expected: false,
		},
		{
			name:     "connection refused should not be critical",
			err:      errors.New("connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTxErrorCritical(tt.err)
			if result != tt.expected {
				t.Errorf("isTxErrorCritical(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

