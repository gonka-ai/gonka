package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"common/completionapi"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

// FinishProposerVerifier checks a Finish's ProposerSig. *state.StateMachine
// implements this; do not reimplement the check here.
type FinishProposerVerifier interface {
	VerifyFinishProposerSig(msg *types.MsgFinishInference) error
}

// TimeoutArtifacts is evidence forwarded with an error-miss verification RPC.
// Required for MsgErrorMiss (finish_tx + response_payload). Unused for
// refused/execution timeout votes.
type TimeoutArtifacts struct {
	FinishTx        []byte
	ResponsePayload []byte
}

func (h *Host) VerifyFinishProposerSig(msg *types.MsgFinishInference) error {
	return h.sm.VerifyFinishProposerSig(msg)
}

var _ FinishProposerVerifier = (*Host)(nil)

// ExecutorClient contacts the executor host to check inference status.
type ExecutorClient interface {
	// GetMempool returns the executor's pending transactions.
	// Used by VerifyExecutionTimeout to check for MsgFinishInference.
	GetMempool(ctx context.Context) ([]*types.DevshardTx, error)

	// ChallengeReceipt forwards diffs + payload to the executor.
	// The executor applies missing diffs, verifies the payload, and returns
	// a signed receipt if it can produce one. Also triggers execution so
	// the inference actually completes. Returns nil receipt if executor
	// cannot produce one (not the executor, inference not pending, etc).
	// mempool is a snapshot of the executor's pool after the challenge
	// (typically including MsgConfirmStart). Callers must copy those txs
	// rather than synthesizing ConfirmStart from the receipt.
	ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, diffs []types.Diff) (receipt []byte, mempool []*types.DevshardTx, err error)
}

// TxSink receives mempool txs copied from a challenge-receipt response.
type TxSink interface {
	AddTx(tx *types.DevshardTx)
}

// RecoveryTxsFor returns ConfirmStart and FinishInference txs for inferenceID.
// Challenge and verify-timeout recovery copy only these; the rest of a host
// mempool snapshot is not recovery-relevant.
func RecoveryTxsFor(txs []*types.DevshardTx, inferenceID uint64) []*types.DevshardTx {
	var out []*types.DevshardTx
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			out = append(out, tx)
			continue
		}
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			out = append(out, tx)
		}
	}
	return out
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
	localMempool []*types.DevshardTx,
	executorClient ExecutorClient,
	ingest TxSink,
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
		return false, nil //nolint:nilerr // the verdict is the bool: a bad payload rejects the timeout.
	}

	// Challenge executor: one call that applies diffs + verifies payload + returns receipt.
	if executorClient != nil {
		receipt, mempool, err := executorClient.ChallengeReceipt(ctx, inferenceID, payload, storedDiffs)
		if err != nil {
			// Executor unreachable or internal error -> accept timeout.
			return true, nil //nolint:nilerr // the verdict is the bool: an unreachable executor accepts the timeout.
		}
		if len(receipt) > 0 {
			// Copy executor recovery txs into the verifier pool. Same bytes as
			// the executor queued — do not mint a new ConfirmStart from receipt.
			if ingest != nil {
				for _, tx := range RecoveryTxsFor(mempool, inferenceID) {
					ingest.AddTx(tx)
				}
			}
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
	localMempool []*types.DevshardTx,
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

// Reject causes for VerifyErrorMiss. These are the verifier-side labels
// for devshard_gateway_error_miss_verify_rejects_total{cause}. Failures that
// are not a named check fold into the closest cause (no usable Finish →
// no_finish_tx; Finish exists but does not authenticate → sig).
const (
	ErrorTimeoutRejectNoFinishTx   = "no_finish_tx"
	ErrorTimeoutRejectNoPayload    = "no_payload"
	ErrorTimeoutRejectSig          = "sig"
	ErrorTimeoutRejectHashMismatch = "hash_mismatch"
	ErrorTimeoutRejectNotErrorBody = "not_error_body"
)

// VerifyErrorMiss checks whether a finished error/malformed body is a miss.
//
// Local computation only: no ctx, ExecutorClient, payload fetcher,
// SessionConfig, or clock. Gossip is disabled, so finishTx and
// responsePayload from the request are the evidence. All failures
// reject (accept=false); a verifier with no artifact must not vote.
// rejectCause is set on reject and empty on accept.
//
// finishVerifier is the executor-signature check (pass *state.StateMachine).
// It is the keyring, not evidence. On accept, the returned hash is
// msg.ResponseHash from the Finish this verifier authenticated.
func VerifyErrorMiss(
	st types.EscrowState,
	inferenceID uint64,
	finishTx []byte,
	responsePayload []byte,
	localMempool []*types.DevshardTx,
	finishVerifier FinishProposerVerifier,
) (bool, []byte, string, error) {
	rec, ok := st.Inferences[inferenceID]
	if !ok || rec == nil {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}
	if rec.Status != types.StatusStarted && rec.Status != types.StatusFinished {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}

	msg := resolveFinishMessage(finishTx, localMempool, inferenceID)
	if msg == nil {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}
	if msg.InferenceId != inferenceID {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}
	if msg.EscrowId != st.EscrowID {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}
	if msg.ExecutorSlot != rec.ExecutorSlot {
		return false, nil, ErrorTimeoutRejectNoFinishTx, nil
	}
	if rec.Status == types.StatusFinished && !bytes.Equal(msg.ResponseHash, rec.ResponseHash) {
		return false, nil, ErrorTimeoutRejectHashMismatch, nil
	}

	if finishVerifier == nil {
		return false, nil, ErrorTimeoutRejectSig, nil
	}
	if err := finishVerifier.VerifyFinishProposerSig(msg); err != nil {
		return false, nil, ErrorTimeoutRejectSig, nil //nolint:nilerr // the verdict is the bool plus a reject reason.
	}

	if len(responsePayload) == 0 {
		return false, nil, ErrorTimeoutRejectNoPayload, nil
	}
	sum := sha256.Sum256(responsePayload)
	if !bytes.Equal(sum[:], msg.ResponseHash) {
		return false, nil, ErrorTimeoutRejectHashMismatch, nil
	}

	if _, ok := completionapi.IsTerminalErrorResponse(responsePayload); !ok {
		return false, nil, ErrorTimeoutRejectNotErrorBody, nil
	}
	return true, append([]byte(nil), msg.ResponseHash...), "", nil
}

func resolveFinishMessage(finishTx []byte, localMempool []*types.DevshardTx, inferenceID uint64) *types.MsgFinishInference {
	if msg := finishFromMempool(localMempool, inferenceID); msg != nil {
		return msg
	}
	if len(finishTx) == 0 {
		return nil
	}
	return decodeFinishTx(finishTx)
}

func decodeFinishTx(finishTx []byte) *types.MsgFinishInference {
	tx := &types.DevshardTx{}
	if err := proto.Unmarshal(finishTx, tx); err != nil {
		return nil
	}
	return tx.GetFinishInference()
}

func finishFromMempool(mempool []*types.DevshardTx, inferenceID uint64) *types.MsgFinishInference {
	for _, tx := range mempool {
		if tx == nil {
			continue
		}
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return fi
		}
	}
	return nil
}
