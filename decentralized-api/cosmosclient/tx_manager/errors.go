package tx_manager

import (
	"errors"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	ErrBuildingUnsignedTx = errors.New("error building unsigned transaction")
	ErrFailedToSignTx     = errors.New("error signing transaction")
	ErrFailedToEncodeTx   = errors.New("error encoding transaction")
	ErrAccountNotFound    = errors.New("key not found")
	ErrTxTooLarge         = errors.New("tx too large")
	ErrDecodingTxHash     = errors.New("error decoding transaction hash")
	ErrInvalidAddress     = errors.New("invalid bech32 string")

	ErrTxFailedToBroadcastAndPutOnRetry = errors.New("failed to broadcast and put on retry")
	ErrTxNotFound                       = errors.New("tx not found")
)

// TxResponseAction defines the action to take after broadcast based on response classification
type TxResponseAction int

const (
	// TxActionObserve means the TX is pending/success, observe it via the observer queue
	TxActionObserve TxResponseAction = iota
	// TxActionRetry means a transient error occurred, retry later
	TxActionRetry
	// TxActionFail means a permanent failure occurred, fail immediately without retry
	TxActionFail
)

// retryablePatterns contains error patterns that indicate transient/infrastructure errors
// which should be retried. All other errors are treated as permanent business logic failures.
var retryablePatterns = []string{
	// Network/transport errors
	"connection refused",
	"connection reset",
	"i/o timeout",
	"context deadline exceeded",
	"broken pipe",
	"eof",
	"no such host",
	"network is unreachable",
	"no route to host",
	"certificate",
	// HTTP gateway errors
	"post failed",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	// RPC errors
	"rpc error",
	"aborted",
	// OS resource exhaustion
	"too many open files",
	// Cosmos SDK transient errors
	"mempool is full",
	// Sequence errors (safety net for non-unordered scenarios)
	"account sequence mismatch",
	"incorrect account sequence",

	"unordered transaction has a timeout_timestamp that has already passed",
	"unordered tx ttl exceeds",
}

// isRetryableRawLog checks if the raw log contains any retryable error patterns
func isRetryableRawLog(rawLog string) bool {
	lower := strings.ToLower(rawLog)
	for _, pattern := range retryablePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// isRetryableBroadcastError checks if a broadcast error is retryable
func isRetryableBroadcastError(err error) bool {
	if err == nil {
		return false
	}
	return isRetryableRawLog(err.Error())
}

// classifyBroadcastResponse determines the action to take based on the broadcast response
// - Code 0: success, observe the tx
// - Code 19 (ErrTxInMempoolCache): tx already in mempool, observe it
// - Code 20 (ErrMempoolIsFull): mempool full, retry
// - Other codes with retryable RawLog: retry
// - Other codes: permanent business logic failure, fail immediately
func classifyBroadcastResponse(resp *sdk.TxResponse) TxResponseAction {
	if resp == nil {
		return TxActionRetry
	}

	switch resp.Code {
	case 0: // Success
		return TxActionObserve
	case 19: // ErrTxInMempoolCache - tx is already pending in mempool
		return TxActionObserve
	case 20: // ErrMempoolIsFull - transient, retry
		return TxActionRetry
	default:
		// Check RawLog for transient patterns
		if isRetryableRawLog(resp.RawLog) {
			return TxActionRetry
		}
		return TxActionFail
	}
}

func isTxErrorCritical(err error) bool {
	errString := strings.ToLower(err.Error())
	if errors.Is(err, ErrBuildingUnsignedTx) || errors.Is(err, ErrFailedToSignTx) ||
		errors.Is(err, ErrFailedToEncodeTx) || strings.Contains(errString, ErrTxTooLarge.Error()) ||
		strings.Contains(errString, ErrAccountNotFound.Error()) || strings.Contains(errString, ErrInvalidAddress.Error()) {
		return true
	}
	return false
}
