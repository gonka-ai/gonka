package tx_manager

import (
	"errors"
	"fmt"
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

	// Retry queue accepted the tx.
	ErrTxQueuedForRetry = errors.New("tx queued for retry")
	// Retryable tx could not be enqueued.
	ErrTxRetryEnqueueFailed = errors.New("failed to enqueue tx for retry")
	ErrTxNotFound           = errors.New("tx not found")

	// CheckTx rejected the tx with a transient code (mempool full, OOG, …).
	// Callers that do not enqueue retries should resubmit later.
	ErrTxCheckTxRetry = errors.New("checktx failed, retry")
	// CheckTx rejected the tx because attached fees were too low. Refresh
	// FeeParams and retry the same payload; this is not a permanent failure.
	ErrTxCheckTxInsufficientFee = errors.New("checktx insufficient fee")
	// CheckTx rejected the tx with a permanent code. Do not resubmit the same payload.
	ErrTxCheckTxFail = errors.New("checktx failed permanently")
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
	// Out-of-gas: the per-msg-type estimate underestimated this batch's
	// real consumption. Retry with a bumped gasWanted (estimateBatchGas
	// applies a multiplier per attempt, see gas_estimate.go).
	"out of gas",
	// Stale fee-tree / gov price bump. Retry the same payload after refresh.
	"insufficient fee",
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

// errorFromCheckTx maps BroadcastTxSync's (resp, rpcErr) onto an error the
// NoRetry path can return. Code 0 / 19 are admission success.
func errorFromCheckTx(resp *sdk.TxResponse, broadcastErr error) error {
	if broadcastErr != nil {
		return broadcastErr
	}
	switch classifyBroadcastResponse(resp) {
	case TxActionObserve:
		return nil
	case TxActionRetry:
		if resp != nil && isInsufficientFeeRawLog(resp.RawLog) {
			if txErr := NewTransactionErrorFromResponse(resp); txErr != nil {
				return fmt.Errorf("%w: %w", ErrTxCheckTxInsufficientFee, txErr)
			}
			return ErrTxCheckTxInsufficientFee
		}
		if txErr := NewTransactionErrorFromResponse(resp); txErr != nil {
			return fmt.Errorf("%w: %w", ErrTxCheckTxRetry, txErr)
		}
		return ErrTxCheckTxRetry
	case TxActionFail:
		if txErr := NewTransactionErrorFromResponse(resp); txErr != nil {
			return fmt.Errorf("%w: %w", ErrTxCheckTxFail, txErr)
		}
		return ErrTxCheckTxFail
	default:
		return fmt.Errorf("unexpected checktx classification")
	}
}

func isInsufficientFeeRawLog(rawLog string) bool {
	return strings.Contains(strings.ToLower(rawLog), "insufficient fee")
}

// IsPermanentCheckTxError reports a CheckTx rejection that should not be retried
// with the same payload.
func IsPermanentCheckTxError(err error) bool {
	return err != nil && errors.Is(err, ErrTxCheckTxFail)
}

// IsInsufficientFeeCheckTxError reports a CheckTx rejection caused by attached
// fees being too low for the current min gas price.
func IsInsufficientFeeCheckTxError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTxCheckTxInsufficientFee) {
		return true
	}
	return isInsufficientFeeRawLog(err.Error())
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
