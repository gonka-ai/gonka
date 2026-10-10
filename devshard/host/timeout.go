package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"google.golang.org/protobuf/proto"

	"common/completionapi"

	"devshard/state"
	"devshard/types"
)

// FinishProposerVerifier checks a Finish's ProposerSig. *state.StateMachine
// implements this; do not reimplement the check here.
type FinishProposerVerifier interface {
	VerifyFinishProposerSig(msg *types.MsgFinishInference) error
}

// EvidenceVerifier checks a ConfirmStart or Finish before it can reject a
// refused or execution timeout. txs, applied in order to rec, must pass every
// check apply runs, so evidence that rejects a timeout is evidence the user can
// sequence. The check caches no warm binding. *state.StateMachine and *Host
// implement this. With a nil verifier nothing is evidence, and the timeout
// stands.
type EvidenceVerifier interface {
	CheckEvidence(rec *types.InferenceRecord, txs ...*types.DevshardTx) error
}

// maxEvidenceChecks bounds signature recoveries per mempool scan. An honest
// pool holds one ConfirmStart and at most one Finish per inference.
const maxEvidenceChecks = 4

// TimeoutArtifacts is evidence forwarded with an error-miss verification RPC.
// Required for MsgErrorMiss (finish_tx + response_payload). Unused for
// refused/execution timeout votes.
type TimeoutArtifacts struct {
	FinishTx           []byte
	ResponsePayload    []byte
	SiblingInferenceID uint64
}

func (h *Host) VerifyFinishProposerSig(msg *types.MsgFinishInference) error {
	return h.sm.VerifyFinishProposerSig(msg)
}

func (h *Host) CheckEvidence(rec *types.InferenceRecord, txs ...*types.DevshardTx) error {
	return h.sm.CheckEvidence(rec, txs...)
}

var (
	_ FinishProposerVerifier = (*Host)(nil)
	_ EvidenceVerifier       = (*Host)(nil)
)

// verifiedConfirm returns the first ConfirmStart for inferenceID in pool that
// would apply to rec. A non-nil receipt must equal its ExecutorSig.
func verifiedConfirm(ev EvidenceVerifier, inferenceID uint64, rec *types.InferenceRecord, receipt []byte, pool []*types.DevshardTx) *types.DevshardTx {
	if ev == nil || rec == nil {
		return nil
	}
	checks := 0
	for _, tx := range pool {
		cs := tx.GetConfirmStart()
		if cs == nil || cs.InferenceId != inferenceID || len(cs.ExecutorSig) == 0 {
			continue
		}
		if receipt != nil && !bytes.Equal(receipt, cs.ExecutorSig) {
			continue
		}
		if checks == maxEvidenceChecks {
			return nil
		}
		checks++
		if ev.CheckEvidence(rec, state.EvidenceSequence(rec, nil, tx)...) == nil {
			return tx
		}
	}
	return nil
}

// verifiedFinish returns the first Finish for inferenceID in pool that would
// apply to rec. A non-nil confirm is the ConfirmStart sequenced ahead of it
// when the record is still pending. Escrow, slot, and the signature are
// decided by CheckEvidence, the same check the gateway uses.
func verifiedFinish(ev EvidenceVerifier, escrowID string, inferenceID uint64, rec *types.InferenceRecord, confirm *types.DevshardTx, pool []*types.DevshardTx) *types.DevshardTx {
	if ev == nil || rec == nil {
		return nil
	}
	checks := 0
	for _, tx := range pool {
		fi := tx.GetFinishInference()
		// Escrow and slot are rejects CheckEvidence would return. Skipping them
		// here keeps a flood of other-escrow or other-slot finishes from using
		// up the signature budget and hiding a finish that would apply.
		if fi == nil || fi.InferenceId != inferenceID || fi.EscrowId != escrowID || fi.ExecutorSlot != rec.ExecutorSlot {
			continue
		}
		seq := state.EvidenceSequence(rec, confirm, tx)
		if len(seq) == 0 {
			continue
		}
		if checks == maxEvidenceChecks {
			return nil
		}
		checks++
		if ev.CheckEvidence(rec, seq...) == nil {
			return tx
		}
	}
	return nil
}

// ExecutorClient contacts the executor host to check inference status.
type ExecutorClient interface {
	// GetMempool returns the executor's pending transactions for an
	// already-bound session. It cannot CreateSession.
	GetMempool(ctx context.Context) ([]*types.DevshardTx, error)

	// ChallengeReceipt forwards creator-signed diffs to the executor.
	// The host CreateSession from the gateway signature in those diffs when
	// it has not bound the escrow, then applies missing diffs. With a payload
	// it verifies the payload, returns a signed receipt, and triggers
	// execution. A nil payload only binds and catches up: no receipt, no
	// execution. mempool is the executor pool after that (ConfirmStart and
	// FinishInference for this inference). A refused vote copies a ConfirmStart
	// only after the receipt verifies for this escrow, this inference, and
	// this executor. VerifyRefusedProgress sends the diffs after a verified
	// executor tip and retries a matching advance; VerifyRefusedTimeout
	// challenges once with no diffs. An execution vote challenges once, with
	// a nil payload and no diffs, and rejects only when the returned pool
	// contains a MsgFinishInference for this escrow and inference that would
	// apply to the started record. That finish is copied into the caller's
	// sink so the user can sequence it.
	ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, diffs []types.Diff) (receipt []byte, mempool []*types.DevshardTx, err error)
}

// SessionTip is an executor client that can report its applied nonce and state root.
type SessionTip interface {
	SessionHead(ctx context.Context) (nonce uint64, stateRoot []byte, err error)
}

// RefusalJournal is the verifier's stored diff history for one escrow.
type RefusalJournal interface {
	// Anchor reports whether nonce is stored and its state hash equals root.
	Anchor(nonce uint64, root []byte) (bool, error)
	// Diffs returns the contiguous diffs in [from, to]. from > to is empty.
	Diffs(from, to uint64) ([]types.Diff, error)
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
//  3. Check local mempool for a verified MsgConfirmStart -- if found, reject.
//  4. Validate payload against on-chain record (same checks executor does).
//  5. Challenge the executor once, with the payload and the supplied diffs.
//  6. If the executor produces a receipt signed for this escrow and this
//     inference by the executor -> reject (it has the inference and will compute).
//  7. If the executor is unreachable, or the receipt does not verify -> accept.
func VerifyRefusedTimeout(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	payload *InferencePayload,
	localMempool []*types.DevshardTx,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
	config types.SessionConfig,
	nowUnix int64,
) (bool, error) {
	return verifyRefusedTimeout(ctx, st, inferenceID, payload, localMempool, nil, executorClient, ingest, ev, config, nowUnix)
}

type refusedChallenge int

const (
	refusedUnreachable refusedChallenge = iota
	refusedReceipt
	refusedNoReceipt
)

func verifyRefusedTimeout(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	payload *InferencePayload,
	localMempool []*types.DevshardTx,
	diffs []types.Diff,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
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

	// Fast path: a verified MsgConfirmStart in the local mempool. A finish
	// alone cannot apply to a pending record, so it is not evidence here.
	if verifiedConfirm(ev, inferenceID, rec, nil, localMempool) != nil {
		return false, nil
	}

	// Reject if no payload provided.
	if payload == nil {
		return false, fmt.Errorf("no payload for refused timeout verification")
	}

	// Verifier validates payload against on-chain record (same checks executor does).
	if err := VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
		return false, nil // bad payload -> reject timeout
	}

	if executorClient == nil {
		return true, nil
	}
	return finishRefusedChallenge(challengeRefused(ctx, st.EscrowID, inferenceID, rec, payload, diffs, executorClient, ingest, ev))
}

func finishRefusedChallenge(outcome refusedChallenge, callErr error) (bool, error) {
	if callErr != nil {
		return true, nil
	}
	return outcome != refusedReceipt, nil
}

func challengeRefused(
	ctx context.Context,
	escrowID string,
	inferenceID uint64,
	rec *types.InferenceRecord,
	payload *InferencePayload,
	diffs []types.Diff,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
) (refusedChallenge, error) {
	receipt, mempool, err := executorClient.ChallengeReceipt(ctx, inferenceID, payload, diffs)
	if err != nil {
		// The caller decides whether this error is an answer or no HTTP response.
		return refusedUnreachable, err
	}
	if len(receipt) == 0 {
		return refusedNoReceipt, nil
	}
	// The receipt counts only as the ExecutorSig of a queued ConfirmStart
	// that verifies for this escrow and this inference.
	confirmTx := verifiedConfirm(ev, inferenceID, rec, receipt, mempool)
	if confirmTx == nil {
		return refusedNoReceipt, nil
	}
	// Copy the verified txs. Same bytes the executor queued — do not mint a
	// new ConfirmStart from the receipt, and do not copy anything unverified.
	if ingest != nil {
		ingest.AddTx(confirmTx)
		if finishTx := verifiedFinish(ev, escrowID, inferenceID, rec, confirmTx, mempool); finishTx != nil {
			ingest.AddTx(finishTx)
		}
	}
	return refusedReceipt, nil
}

// VerifyExecutionTimeout checks if an execution timeout is valid.
//
// Flow:
//  1. Check local state: inference must be started (has receipt, no finish).
//  2. Check deadline has passed.
//  3. Check local mempool for a verified MsgFinishInference -- if found, reject.
//  4. Challenge the executor once, with a nil payload and no diffs.
//     A MsgFinishInference in the returned pool rejects the timeout only
//     when it is for this escrow and this inference and would apply to the
//     started record. That finish is copied into ingest so the reject reply
//     carries it and the user can sequence it. The nil payload does not sign
//     a receipt or start execution.
//  5. If the executor is unreachable, or the finish does not verify -> accept.
func VerifyExecutionTimeout(
	ctx context.Context,
	st types.EscrowState,
	inferenceID uint64,
	localMempool []*types.DevshardTx,
	executorClient ExecutorClient,
	ingest TxSink,
	ev EvidenceVerifier,
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

	// Fast path: a verified MsgFinishInference in the local mempool.
	if verifiedFinish(ev, st.EscrowID, inferenceID, rec, nil, localMempool) != nil {
		return false, nil
	}

	// One challenge, no journal. A nil payload does not sign a receipt or
	// start execution. A host that never applied the start has no finish
	// to return, and the timeout stands.
	if executorClient != nil {
		_, executorMempool, err := executorClient.ChallengeReceipt(ctx, inferenceID, nil, nil)
		if err == nil {
			if finishTx := verifiedFinish(ev, st.EscrowID, inferenceID, rec, nil, executorMempool); finishTx != nil {
				if ingest != nil {
					ingest.AddTx(finishTx)
				}
				return false, nil
			}
		}
		// An unreachable executor supports the timeout claim, and a finish
		// that does not verify is not evidence the work completed.
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
	ErrorTimeoutRejectClientFault  = "client_fault"
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
	return VerifyErrorMissWithSibling(st, inferenceID, finishTx, responsePayload, 0, localMempool, finishVerifier)
}

func VerifyErrorMissWithSibling(
	st types.EscrowState,
	inferenceID uint64,
	finishTx []byte,
	responsePayload []byte,
	siblingInferenceID uint64,
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

	msg := resolveFinishMessage(finishTx, localMempool, inferenceID, rec, st.EscrowID)
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
	if rec.Status == types.StatusFinished && !finishMatchesAppliedRecord(msg, rec, st.EscrowID) {
		return false, nil, ErrorTimeoutRejectHashMismatch, nil
	}

	if finishVerifier == nil {
		return false, nil, ErrorTimeoutRejectSig, nil
	}
	if err := finishVerifier.VerifyFinishProposerSig(msg); err != nil {
		return false, nil, ErrorTimeoutRejectSig, nil
	}

	if len(responsePayload) == 0 {
		return false, nil, ErrorTimeoutRejectNoPayload, nil
	}
	inputTokens, outputTokens := msg.InputTokens, msg.OutputTokens
	servedHash := msg.ServedHash
	if rec.Status == types.StatusFinished {
		inputTokens, outputTokens = rec.InputTokens, rec.OutputTokens
		servedHash = rec.ServedHash
	}
	sum := sha256.Sum256(responsePayload)
	if !bytes.Equal(sum[:], msg.ResponseHash) && !bytes.Equal(sum[:], servedHash) {
		return false, nil, ErrorTimeoutRejectHashMismatch, nil
	}

	if _, ok := completionapi.IsTerminalErrorResponse(responsePayload); !ok {
		return false, nil, ErrorTimeoutRejectNotErrorBody, nil
	}
	if completionapi.IsUnbilledClientFault(responsePayload, inputTokens, outputTokens) &&
		(siblingInferenceID == inferenceID || !SiblingServedPrompt(rec, st.Inferences[siblingInferenceID])) {
		return false, nil, ErrorTimeoutRejectClientFault, nil
	}
	return true, append([]byte(nil), msg.ResponseHash...), "", nil
}

func resolveFinishMessage(finishTx []byte, localMempool []*types.DevshardTx, inferenceID uint64, rec *types.InferenceRecord, escrowID string) *types.MsgFinishInference {
	if rec.Status == types.StatusFinished {
		for _, tx := range localMempool {
			if tx == nil {
				continue
			}
			if msg := tx.GetFinishInference(); msg != nil && msg.InferenceId == inferenceID && finishMatchesAppliedRecord(msg, rec, escrowID) {
				return msg
			}
		}
		if msg := DecodeFinishTx(finishTx); msg != nil && msg.InferenceId == inferenceID && finishMatchesAppliedRecord(msg, rec, escrowID) {
			return msg
		}
	}
	if msg := finishFromMempool(localMempool, inferenceID); msg != nil {
		return msg
	}
	if len(finishTx) == 0 {
		return nil
	}
	return DecodeFinishTx(finishTx)
}

func SiblingServedPrompt(rec, sibling *types.InferenceRecord) bool {
	return rec != nil && sibling != nil && rec != sibling && len(rec.PromptHash) > 0 &&
		(sibling.Status == types.StatusFinished || sibling.Status == types.StatusValidated) &&
		sibling.OutputTokens > 0 && bytes.Equal(sibling.PromptHash, rec.PromptHash)
}

func finishMatchesAppliedRecord(msg *types.MsgFinishInference, rec *types.InferenceRecord, escrowID string) bool {
	return msg.EscrowId == escrowID && msg.ExecutorSlot == rec.ExecutorSlot &&
		bytes.Equal(msg.ResponseHash, rec.ResponseHash) &&
		(len(rec.ServedHash) == 0 || bytes.Equal(msg.ServedHash, rec.ServedHash)) &&
		msg.InputTokens == rec.InputTokens && msg.OutputTokens == rec.OutputTokens
}

func DecodeFinishTx(finishTx []byte) *types.MsgFinishInference {
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
