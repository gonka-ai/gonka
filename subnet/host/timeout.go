package host

import (
	"context"
	"fmt"

	"subnet/types"
)

// ExecutorClient contacts the executor host to check inference status.
type ExecutorClient interface {
	// GetMempool returns the executor's pending transactions.
	// Used by VerifyExecutionTimeout to check for MsgFinishInference.
	GetMempool(ctx context.Context) ([]*types.SubnetTx, error)

	// ChallengeReceipt forwards diffs + payload to the executor.
	// The executor applies missing diffs, verifies the payload, and returns
	// a signed receipt if it can produce one. Also triggers execution so
	// the inference actually completes. Returns nil receipt if executor
	// cannot produce one (not the executor, inference not pending, etc).
	ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, diffs []types.Diff) (receipt []byte, err error)
}

// VerifyRefusedTimeout checks if a refused timeout is valid.
//
// Flow:
//  1. Check local state: inference must be pending (no receipt).
//  2. Check deadline has passed.
//  3. Check local mempool for MsgConfirmStart -- if found, reject.
//  4. Validate payload against on-chain record (same checks executor does).
//  5. Challenge executor: forward diffs + payload in one call.
//  6. If executor produces receipt -> reject (it received data and will compute).
//  7. If executor unreachable or no receipt -> accept.
func VerifyRefusedTimeout(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	payload *InferencePayload,
	storedDiffs []types.Diff,
	localMempool []*types.SubnetTx,
	executorClient ExecutorClient,
	config types.SessionConfig,
	nowUnix int64,
) (bool, error) {
	rec, ok := st.Inferences[inferenceID]
	if !ok {
		return false, fmt.Errorf("inference %d not found", inferenceID)
	}
	if rec.Status != types.StatusPending {
		return false, fmt.Errorf("inference %d: expected pending, got %d", inferenceID, rec.Status)
	}

	// Reject if refusal timeout deadline has not passed.
	if nowUnix-rec.StartedAt < config.RefusalTimeout {
		return false, nil
	}

	// Fast path: check local mempool for MsgConfirmStart or MsgFinishInference.
	for _, tx := range localMempool {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			return false, nil // executor already confirmed
		}
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return false, nil // executor already finished
		}
	}

	// Reject if no payload provided.
	if payload == nil {
		return false, fmt.Errorf("no payload for refused timeout verification")
	}

	// Verifier validates payload against on-chain record (same checks executor does).
	if err := VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
		return false, nil // bad payload -> reject timeout
	}

	// Challenge executor: one call that applies diffs + verifies payload + returns receipt.
	if executorClient != nil {
		receipt, err := executorClient.ChallengeReceipt(ctx, inferenceID, payload, storedDiffs)
		if err != nil {
			// Executor unreachable or internal error -> accept timeout.
			return true, nil
		}
		if len(receipt) > 0 {
			return false, nil // executor produced receipt -> reject timeout
		}
		// Executor reachable but no receipt (refusing to work) -> accept timeout.
	}

	return true, nil
}

// VerifyExecutionTimeout checks if an execution timeout is valid.
//
// Flow:
//  1. Check local state: inference must be started (has receipt, no finish).
//  2. Check deadline has passed.
//  3. Check local mempool for MsgFinishInference -- if found, reject.
//  4. Check executor mempool for MsgFinishInference -- if found, reject.
//  5. If executor unreachable or no result -> accept.
func VerifyExecutionTimeout(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	localMempool []*types.SubnetTx,
	executorClient ExecutorClient,
	config types.SessionConfig,
	nowUnix int64,
) (bool, error) {
	rec, ok := st.Inferences[inferenceID]
	if !ok {
		return false, fmt.Errorf("inference %d not found", inferenceID)
	}
	if rec.Status != types.StatusStarted {
		return false, fmt.Errorf("inference %d: expected started, got %d", inferenceID, rec.Status)
	}

	// Reject if execution timeout deadline has not passed.
	// Anchored to ConfirmedAt (executor-signed wall clock), not StartedAt (user-controlled).
	if nowUnix-rec.ConfirmedAt < config.ExecutionTimeout {
		return false, nil
	}

	// Fast path: check local mempool for MsgFinishInference.
	for _, tx := range localMempool {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return false, nil // executor already finished
		}
	}

	// Contact executor.
	if executorClient != nil {
		executorMempool, err := executorClient.GetMempool(ctx)
		if err == nil {
			for _, tx := range executorMempool {
				if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
					return false, nil // executor has the finish, reject timeout
				}
			}
		}
		// err != nil means executor unreachable, which supports the timeout claim.
	}

	return true, nil
}
