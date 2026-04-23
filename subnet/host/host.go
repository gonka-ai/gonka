package host

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/gossip"
	"subnet/logging"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/types"
)

// InferencePayload carries the actual request data for the current inference.
// The host verifies these against the signed MsgStartInference in the diff.
type InferencePayload struct {
	Prompt      []byte
	Model       string
	InputLength uint64
	MaxTokens   uint64
	StartedAt   int64
}

// HostRequest carries diffs from the user to a host.
type HostRequest struct {
	Diffs   []types.Diff
	Nonce   uint64            // nonce of the current request
	Payload *InferencePayload // nil if no new inference (e.g., Finalize, empty diffs)
}

// HostResponse carries the host's reply back to the user.
type HostResponse struct {
	StateSig  []byte // nil = withheld
	StateHash []byte // always set after applying diffs
	Nonce     uint64 // current nonce after applying diffs
	Receipt     []byte // executor receipt sig, nil if not executor
	ConfirmedAt int64  // executor wall-clock timestamp, 0 if not executor
	Mempool     []*types.SubnetTx
	ExecutionJob *subnet.ExecuteRequest // non-nil if this host is the executor and execution is deferred
	CachedResponseBody []byte // non-nil when reconnecting to a completed inference
}

// AcceptanceChecker is an optional hook that lets the host withhold its
// signature when a diff contains content the host considers unacceptable
// (e.g. suspicious timestamps, insufficient max_cost). Return a non-nil
// error to withhold; nil to allow signing.
type AcceptanceChecker interface {
	Check(st types.EscrowState, applied []*types.SubnetTx) error
}

// Host processes user requests: applies diffs, executes inference, signs state.
type Host struct {
	mu       sync.Mutex
	sm       *state.StateMachine
	signer   signing.Signer
	verifier signing.Verifier
	engine   subnet.InferenceEngine
	validator subnet.ValidationEngine // optional, nil = no validation
	escrowID string
	slotIDs  map[uint32]bool
	group    []types.SlotAssignment
	mempool  *Mempool
	checker  AcceptanceChecker
	store    storage.Storage  // optional, nil = no persistence
	gsp      *gossip.Gossip   // optional, nil = no gossip pruning

	// Lookup maps built from group at construction time.
	slotToAddr  map[uint32]string   // slotID -> validator address
	addrToSlots map[string][]uint32 // address -> all slotIDs owned

	sortedSlots        []uint32            // deterministic slot order for this host
	executing          map[uint64]struct{} // inference IDs with in-flight execution
	validating         map[uint64]struct{} // inference IDs with in-flight validation
	completedResponses map[uint64][]byte   // inference ID -> cached ML response body
	ownSeed            int64               // deterministic seed derived from signer + escrowID
}

func NewHost(
	sm *state.StateMachine,
	signer signing.Signer,
	engine subnet.InferenceEngine,
	escrowID string,
	group []types.SlotAssignment,
	checker AcceptanceChecker,
	opts ...HostOption,
) (*Host, error) {
	if err := types.ValidateGroup(group); err != nil {
		return nil, err
	}
	addr := signer.Address()
	slotIDs := make(map[uint32]bool)
	slotToAddr := make(map[uint32]string, len(group))
	addrToSlots := make(map[string][]uint32, len(group))
	for _, s := range group {
		slotToAddr[s.SlotID] = s.ValidatorAddress
		addrToSlots[s.ValidatorAddress] = append(addrToSlots[s.ValidatorAddress], s.SlotID)
		if s.ValidatorAddress == addr {
			slotIDs[s.SlotID] = true
		}
	}

	// Check state's WarmKeys for existing bindings, then try the warm key
	// resolver directly (without caching in SM state, which would change
	// the state root before any diffs are applied).
	if len(slotIDs) == 0 {
		warmKeys := sm.WarmKeys()
		for slotID, warmAddr := range warmKeys {
			if warmAddr == addr {
				slotIDs[slotID] = true
			}
		}
	}
	if len(slotIDs) == 0 {
		for _, s := range group {
			if sm.CheckWarmKey(addr, s.ValidatorAddress) {
				slotIDs[s.SlotID] = true
			}
		}
	}

	if len(slotIDs) == 0 {
		return nil, fmt.Errorf("%w: %s", types.ErrHostNotInGroup, addr)
	}

	sortedSlots := slices.Sorted(maps.Keys(slotIDs))

	// Derive deterministic seed from signer + escrowID.
	seedSig, err := signer.Sign([]byte(escrowID))
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}
	ownSeed, err := state.DeriveSeed(seedSig)
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}

	h := &Host{
		sm:          sm,
		signer:      signer,
		engine:      engine,
		escrowID:    escrowID,
		slotIDs:     slotIDs,
		group:       group,
		mempool:     NewMempool(),
		checker:     checker,
		slotToAddr:  slotToAddr,
		addrToSlots: addrToSlots,
		sortedSlots:        sortedSlots,
		executing:          make(map[uint64]struct{}),
		validating:         make(map[uint64]struct{}),
		completedResponses: make(map[uint64][]byte),
		ownSeed:            ownSeed,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// HostMempool returns the host's mempool. Use this to construct a
// StalenessChecker after host creation, then set it via WithChecker option
// or pass it during construction.
func (h *Host) HostMempool() *Mempool { return h.mempool }

// HostOption configures optional Host behavior.
type HostOption func(*Host)

// WithStorage sets the storage backend for diff persistence.
func WithStorage(s storage.Storage) HostOption {
	return func(h *Host) { h.store = s }
}

// WithVerifier sets the signature verifier for gossip sig accumulation.
func WithVerifier(v signing.Verifier) HostOption {
	return func(h *Host) { h.verifier = v }
}

// WithGossip sets the gossip instance for pruning on finalization.
func WithGossip(g *gossip.Gossip) HostOption {
	return func(h *Host) { h.gsp = g }
}

// WithValidator sets the validation engine for validating other hosts' inferences.
func WithValidator(v subnet.ValidationEngine) HostOption {
	return func(h *Host) { h.validator = v }
}

// WithGrace adds a StalenessChecker to the host's acceptance chain.
// If a checker was already set via the constructor, both are composed
// via CompositeChecker.
func WithGrace(grace uint64) HostOption {
	return func(h *Host) {
		sc := NewStalenessChecker(h.mempool, grace)
		if h.checker != nil {
			h.checker = NewCompositeChecker(sc, h.checker)
		} else {
			h.checker = sc
		}
	}
}

func (h *Host) StateRoot() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.ComputeStateRoot()
}

func (h *Host) MempoolTxs() []*types.SubnetTx {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mempool.Txs()
}

func (h *Host) EscrowID() string            { return h.escrowID }
func (h *Host) Group() []types.SlotAssignment { return h.group }
func (h *Host) SlotIDs() map[uint32]bool     { return h.slotIDs }

// PrimarySlot returns the lowest slot ID owned by this host.
// Deterministic: derived from sortedSlots which is sorted at construction time.
func (h *Host) PrimarySlot() uint32 { return h.sortedSlots[0] }

// IsGroupMemberAddr returns true if addr is a group member (owns at least one slot).
// Safe to call without locking -- addrToSlots is immutable after construction.
func (h *Host) IsGroupMemberAddr(addr string) bool {
	_, ok := h.addrToSlots[addr]
	return ok
}

// SnapshotState returns a deep copy of the current state.
func (h *Host) SnapshotState() types.EscrowState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.SnapshotState()
}

// IsWarmKeyAddress returns true if addr is a known warm key in the current state.
func (h *Host) IsWarmKeyAddress(addr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.IsWarmKeyAddress(addr)
}

// IsWarmKeyForSlot returns true if addr is an authorized warm key for the
// given slot, either via existing state bindings or via the bridge resolver.
func (h *Host) IsWarmKeyForSlot(addr string, slotID uint32) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	warmKeys := h.sm.WarmKeys()
	if warmKeys[slotID] == addr {
		return true
	}
	expected, ok := h.slotToAddr[slotID]
	return ok && h.sm.CheckWarmKey(addr, expected)
}

func (h *Host) Signer() signing.Signer { return h.signer }

func (h *Host) HandleRequest(ctx context.Context, req HostRequest) (*HostResponse, error) {
	h.mu.Lock()

	// (a) Apply all new diffs.
	var lastAppliedTxs []*types.SubnetTx
	for _, diff := range req.Diffs {
		if err := h.applyAndPersist(diff); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		lastAppliedTxs = diff.Txs
	}

	// (b) Sign executor receipt (sync, under mutex).
	receipt, confirmedAt, job, cachedBody, err := h.signReceipt(req)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}

	// (c) Sign state (with acceptance check + mempool staleness).
	stateSig, root, nonce, err := h.signIfAccepted(lastAppliedTxs)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}

	// (d) Produce MsgRevealSeed if finalizing and not already revealed.
	h.maybeRevealSeed()

	// (e) Collect validation candidates under mutex.
	validationJobs := h.collectValidationJobs()

	h.mu.Unlock()

	// (f) Execution job for caller to run via RunExecution.
	// Execution is always deferred so the caller can send the receipt
	// before inference starts (SSE flow).

	// (g) Validate other hosts' inferences outside mutex.
	for _, vj := range validationJobs {
		go h.validateAsync(context.Background(), vj)
	}

	return &HostResponse{
		StateSig:           stateSig,
		StateHash:          root,
		Nonce:              nonce,
		Receipt:            receipt,
		ConfirmedAt:        confirmedAt,
		Mempool:            h.mempool.Txs(),
		ExecutionJob:       job,
		CachedResponseBody: cachedBody,
	}, nil
}

// applyAndPersist applies a diff, removes included txs from mempool, and persists.
// Captures WarmKeyDelta (new warm key bindings introduced by this diff) for replay.
// Caller must hold h.mu.
func (h *Host) applyAndPersist(diff types.Diff) error {
	currentNonce := h.sm.LatestNonce()
	if diff.Nonce <= currentNonce {
		return nil
	}
	var warmBefore map[uint32]string
	if h.store != nil {
		warmBefore = h.sm.WarmKeys()
	}
	root, err := h.sm.ApplyDiff(diff)
	if err != nil {
		return fmt.Errorf("apply diff nonce %d: %w", diff.Nonce, err)
	}
	h.mempool.RemoveIncluded(diff.Txs)

	// Evict cached responses for finalized or timed-out inferences.
	for _, tx := range diff.Txs {
		if fi := tx.GetFinishInference(); fi != nil {
			delete(h.completedResponses, fi.InferenceId)
		}
		if ti := tx.GetTimeoutInference(); ti != nil {
			delete(h.completedResponses, ti.InferenceId)
		}
	}

	if h.store != nil {
		warmAfter := h.sm.WarmKeys()
		delta := types.ComputeWarmKeyDelta(warmBefore, warmAfter)
		rec := types.DiffRecord{Diff: diff, StateHash: root, WarmKeyDelta: delta}
		if err := h.store.AppendDiff(h.escrowID, rec); err != nil {
			return fmt.Errorf("persist diff nonce %d: %w", diff.Nonce, err)
		}
	}
	return nil
}

// ApplyCatchUpDiffs applies diffs the host hasn't seen yet.
// Already-applied diffs (nonce <= current) are silently skipped.
func (h *Host) ApplyCatchUpDiffs(diffs []types.Diff) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, diff := range diffs {
		_ = h.applyAndPersist(diff)
	}
}

// signIfAccepted computes state root, checks acceptance, signs if allowed,
// stores sig and checks finalization. Caller must hold h.mu.
func (h *Host) signIfAccepted(applied []*types.SubnetTx) (stateSig, root []byte, nonce uint64, err error) {
	nonce = h.sm.LatestNonce()
	root, err = h.sm.ComputeStateRoot()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("compute state root: %w", err)
	}

	if h.checker != nil {
		if err := h.checker.Check(h.sm.SnapshotState(), applied); err != nil {
			return nil, root, nonce, nil // withhold
		}
	}

	sig, err := h.signState(nonce, root)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("sign state root: %w", err)
	}
	stateSig = sig

	if h.store != nil {
		for slotID := range h.slotIDs {
			if err := h.store.AddSignature(h.escrowID, nonce, slotID, sig); err != nil {
				logging.Debug("store own sig failed", "subsystem", "host", "nonce", nonce, "error", err)
			}
		}
		h.checkFinalization(nonce)
	}

	return stateSig, root, nonce, nil
}

func (h *Host) findDiff(diffs []types.Diff, nonce uint64) *types.Diff {
	for i := range diffs {
		if diffs[i].Nonce == nonce {
			return &diffs[i]
		}
	}
	return nil
}

// signReceipt verifies the payload and signs the executor receipt (sync, under mutex).
// Returns the receipt sig, confirmed_at timestamp, an ExecuteRequest if this host is the executor,
// and cached response body if the inference already completed (reconnect case).
// Caller must hold h.mu.
func (h *Host) signReceipt(req HostRequest) ([]byte, int64, *subnet.ExecuteRequest, []byte, error) {
	if req.Payload == nil {
		return nil, 0, nil, nil, nil
	}
	targetDiff := h.findDiff(req.Diffs, req.Nonce)
	if targetDiff == nil {
		return nil, 0, nil, nil, nil
	}

	for _, tx := range targetDiff.Txs {
		start := tx.GetStartInference()
		if start == nil {
			continue
		}
		executorSlot := h.group[start.InferenceId%uint64(len(h.group))].SlotID
		if !h.slotIDs[executorSlot] {
			continue
		}

		// Verify payload matches signed diff.
		if err := VerifyPayload(req.Payload, start.PromptHash, start.Model, start.InputLength, start.MaxTokens, start.StartedAt); err != nil {
			return nil, 0, nil, nil, err
		}

		// Sign executor receipt with wall-clock confirmed_at.
		confirmedAt := time.Now().Unix()
		receiptContent := &types.ExecutorReceiptContent{
			InferenceId: start.InferenceId,
			PromptHash:  start.PromptHash,
			Model:       start.Model,
			InputLength: start.InputLength,
			MaxTokens:   start.MaxTokens,
			StartedAt:   start.StartedAt,
			EscrowId:    h.escrowID,
			ConfirmedAt: confirmedAt,
		}
		receiptData, err := proto.Marshal(receiptContent)
		if err != nil {
			return nil, 0, nil, nil, fmt.Errorf("marshal executor receipt: %w", err)
		}
		sig, err := h.signer.Sign(receiptData)
		if err != nil {
			return nil, 0, nil, nil, fmt.Errorf("sign executor receipt: %w", err)
		}

		// Add MsgConfirmStart to mempool so it survives HTTP failures.
		// If the response is lost (e.g. 503), the next request delivers it via mempool.
		h.mempool.Add(MempoolEntry{
			Tx: &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: start.InferenceId,
				ExecutorSig: sig,
				ConfirmedAt: confirmedAt,
			}}},
			ProposedAt: h.sm.LatestNonce(),
		})

		// Dedup: return receipt (proves executor alive) but skip execution.
		if _, dup := h.executing[start.InferenceId]; dup {
			return sig, confirmedAt, nil, nil, nil
		}

		// Already completed: execution finished, response cached.
		if cached, ok := h.completedResponses[start.InferenceId]; ok {
			return sig, confirmedAt, nil, cached, nil
		}

		h.executing[start.InferenceId] = struct{}{}

		job := &subnet.ExecuteRequest{
			InferenceID: start.InferenceId,
			Model:       start.Model,
			Prompt:      req.Payload.Prompt,
			PromptHash:  start.PromptHash,
			InputLength: start.InputLength,
			MaxTokens:   start.MaxTokens,
			EscrowID:    h.escrowID,
		}
		return sig, confirmedAt, job, nil, nil
	}
	return nil, 0, nil, nil, nil
}

// executeAsync runs inference and adds MsgFinishInference to the mempool.
// Delegates to RunExecution which also caches the response body for reconnection.
func (h *Host) executeAsync(ctx context.Context, job *subnet.ExecuteRequest) {
	_, _ = h.RunExecution(ctx, job)
}

// RunExecution executes an inference job and adds MsgFinishInference to the mempool.
// This is the deferred execution path -- used when DeferExecution=true in HandleRequest.
// The caller typically streams results to the client before calling this.
func (h *Host) RunExecution(ctx context.Context, job *subnet.ExecuteRequest) (*subnet.ExecuteResult, error) {
	// Find the internal job metadata for cleanup/mempool.
	inferenceID := job.InferenceID
	executorSlot := h.group[inferenceID%uint64(len(h.group))].SlotID
	diffNonce := h.LatestNonce()

	defer func() {
		h.mu.Lock()
		delete(h.executing, inferenceID)
		h.mu.Unlock()
	}()

	result, err := h.engine.Execute(ctx, *job)
	if err != nil {
		logging.Error("execute failed", "subsystem", "host", "inference_id", inferenceID, "error", err)
		return nil, err
	}

	// Cache response body for reconnection replay.
	if len(result.ResponseBody) > 0 {
		h.mu.Lock()
		h.completedResponses[inferenceID] = result.ResponseBody
		h.mu.Unlock()
	}

	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: result.ResponseHash,
		InputTokens:  result.InputTokens,
		OutputTokens: result.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     h.escrowID,
	}
	proposerSig, err := h.signProposer(finishMsg)
	if err != nil {
		logging.Error("sign finish msg failed", "subsystem", "host", "inference_id", inferenceID, "error", err)
		return result, err
	}
	finishMsg.ProposerSig = proposerSig

	h.mempool.Add(MempoolEntry{
		Tx: &types.SubnetTx{Tx: &types.SubnetTx_FinishInference{
			FinishInference: finishMsg,
		}},
		ProposedAt: diffNonce,
	})

	return result, nil
}

// maybeRevealSeed produces a MsgRevealSeed if the session is finalizing and
// this host's address has not yet revealed. Caller must hold h.mu.
func (h *Host) maybeRevealSeed() {
	if h.sm.Phase() != types.PhaseFinalizing {
		return
	}

	// Check if we already have a reveal in the mempool.
	for _, tx := range h.mempool.Txs() {
		if rs := tx.GetRevealSeed(); rs != nil {
			if h.slotIDs[rs.SlotId] {
				return
			}
		}
	}

	// Check if already revealed in state.
	for slot := range h.slotIDs {
		if h.sm.IsSlotRevealed(slot) {
			return
		}
	}

	// Pick first owned slot as representative (deterministic via sorted order).
	repSlot := h.sortedSlots[0]

	// Sign escrowID bytes to derive the seed signature.
	seedSig, err := h.signer.Sign([]byte(h.escrowID))
	if err != nil {
		logging.Error("sign seed failed", "subsystem", "host", "error", err)
		return
	}

	msg := &types.MsgRevealSeed{
		SlotId:    repSlot,
		Signature: seedSig,
		EscrowId:  h.escrowID,
	}
	proposerSig, err := h.signProposer(msg)
	if err != nil {
		logging.Error("sign reveal seed failed", "subsystem", "host", "error", err)
		return
	}
	msg.ProposerSig = proposerSig

	seedTx := &types.SubnetTx{Tx: &types.SubnetTx_RevealSeed{RevealSeed: msg}}
	h.mempool.Add(MempoolEntry{
		Tx:         seedTx,
		ProposedAt: h.sm.LatestNonce(),
	})

	// Eager gossip: broadcast seed reveal to ALL peers so no host can
	// suppress it. Uses BroadcastTxs which sends to every peer (not K random).
	if h.gsp != nil {
		go h.gsp.BroadcastTxs(context.Background(), []*types.SubnetTx{seedTx})
	}
}

// validateJob captures data needed to run validateAsync outside the mutex.
type validateJob struct {
	inferenceID     uint64
	validatorSlot   uint32
	model           string
	promptHash      []byte
	responseHash    []byte
	inputTokens     uint64
	outputTokens    uint64
	escrowID        string
	executorAddress string
	epochID         uint64
}

// collectValidationJobs finds finished inferences that this host should validate.
// Caller must hold h.mu.
func (h *Host) collectValidationJobs() []validateJob {
	if h.validator == nil {
		return nil
	}

	st := h.sm.SnapshotState()
	var jobs []validateJob

	for infID, rec := range st.Inferences {
		if rec.Status != types.StatusFinished {
			continue
		}

		// Skip if this host is the executor (no self-validation).
		if h.slotIDs[rec.ExecutorSlot] {
			continue
		}

		// Skip if already validated by any of this host's slots.
		alreadyValidated := false
		for slot := range h.slotIDs {
			if rec.ValidatedBy.IsSet(slot) {
				alreadyValidated = true
				break
			}
		}
		if alreadyValidated {
			continue
		}

		// Skip if already validating or has a validation in mempool.
		if _, ok := h.validating[infID]; ok {
			continue
		}
		if h.hasMempoolValidation(infID) {
			continue
		}

		// Probabilistic check: should this host validate this inference?
		mySlotCount := uint32(len(h.slotIDs))
		executorAddr := h.slotToAddr[rec.ExecutorSlot]
		executorSlotCount := h.sm.AddressSlotCount(executorAddr)
		totalSlots := h.sm.TotalSlots()

		if !state.ShouldValidate(h.ownSeed, infID, mySlotCount, executorSlotCount, totalSlots, st.Config.ValidationRate) {
			continue
		}

		// Pick first owned slot as the validator slot (deterministic).
		validatorSlot := h.sortedSlots[0]

		h.validating[infID] = struct{}{}
		jobs = append(jobs, validateJob{
			inferenceID:     infID,
			validatorSlot:   validatorSlot,
			model:           rec.Model,
			promptHash:      rec.PromptHash,
			responseHash:    rec.ResponseHash,
			inputTokens:     rec.InputTokens,
			outputTokens:    rec.OutputTokens,
			escrowID:        h.escrowID,
			executorAddress: executorAddr,
			epochID:         0, // ValidationAdapter will use its own phaseTracker
		})
	}

	return jobs
}

// hasMempoolValidation returns true if a MsgValidation for infID from this host
// is already in the mempool. Caller must hold h.mu.
func (h *Host) hasMempoolValidation(infID uint64) bool {
	for _, tx := range h.mempool.Txs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == infID {
			if h.slotIDs[v.ValidatorSlot] {
				return true
			}
		}
	}
	return false
}

// validateAsync runs validator.Validate, builds MsgValidation, signs it, and
// adds it to the mempool. Called outside the mutex.
func (h *Host) validateAsync(ctx context.Context, job validateJob) {
	defer func() {
		h.mu.Lock()
		delete(h.validating, job.inferenceID)
		h.mu.Unlock()
	}()

	result, err := h.validator.Validate(ctx, subnet.ValidateRequest{
		InferenceID:     job.inferenceID,
		Model:           job.model,
		PromptHash:      job.promptHash,
		ResponseHash:    job.responseHash,
		InputTokens:     job.inputTokens,
		OutputTokens:    job.outputTokens,
		EscrowID:        job.escrowID,
		ExecutorAddress: job.executorAddress,
		EpochID:         job.epochID,
	})
	if err != nil {
		logging.Error("validate failed", "subsystem", "host", "inference_id", job.inferenceID, "error", err)
		return
	}

	msg := &types.MsgValidation{
		InferenceId:   job.inferenceID,
		ValidatorSlot: job.validatorSlot,
		Valid:         result.Valid,
		EscrowId:      h.escrowID,
	}
	proposerSig, err := h.signProposer(msg)
	if err != nil {
		logging.Error("sign validation msg failed", "subsystem", "host", "inference_id", job.inferenceID, "error", err)
		return
	}
	msg.ProposerSig = proposerSig

	h.mu.Lock()
	h.mempool.Add(MempoolEntry{
		Tx: &types.SubnetTx{Tx: &types.SubnetTx_Validation{
			Validation: msg,
		}},
		ProposedAt: h.sm.LatestNonce(),
	})
	h.mu.Unlock()
}

// AccumulateGossipSig verifies and stores a signature received via gossip.
// The sig must recover to group[senderSlot] and the stateHash must match the
// stored DiffRecord for that nonce.
func (h *Host) AccumulateGossipSig(nonce uint64, stateHash, sig []byte, senderSlot uint32) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.verifier == nil || h.store == nil {
		return fmt.Errorf("host not configured for sig accumulation (verifier=%v, store=%v)", h.verifier != nil, h.store != nil)
	}

	expected, ok := h.slotToAddr[senderSlot]
	if !ok {
		return fmt.Errorf("sender slot %d not in group", senderSlot)
	}

	// Verify sig recovers to the expected address.
	sigContent := &types.StateSignatureContent{
		StateRoot: stateHash,
		EscrowId:  h.escrowID,
		Nonce:     nonce,
	}
	sigData, mErr := proto.Marshal(sigContent)
	if mErr != nil {
		return fmt.Errorf("marshal sig content: %w", mErr)
	}
	addr, err := h.verifier.RecoverAddress(sigData, sig)
	if err != nil {
		return fmt.Errorf("recover address: %w", err)
	}
	if addr != expected {
		warmKeys := h.sm.WarmKeys()
		if warmKeys[senderSlot] != addr && !h.sm.CheckWarmKey(addr, expected) {
			return fmt.Errorf("sig from slot %d: expected %s, got %s", senderSlot, expected, addr)
		}
	}

	// Verify stateHash matches stored record.
	records, err := h.store.GetDiffs(h.escrowID, nonce, nonce)
	if err != nil || len(records) == 0 {
		return fmt.Errorf("no stored diff at nonce %d", nonce)
	}
	if !bytes.Equal(records[0].StateHash, stateHash) {
		return fmt.Errorf("state hash mismatch at nonce %d: stored %x, gossip %x", nonce, records[0].StateHash, stateHash)
	}

	// Store sig for all slots owned by this validator address (use cold address for lookup).
	storeAddr := addr
	if addr != expected {
		storeAddr = expected
	}
	for _, slot := range h.addrToSlots[storeAddr] {
		if err := h.store.AddSignature(h.escrowID, nonce, slot, sig); err != nil {
			return err
		}
	}
	h.checkFinalization(nonce)
	return nil
}

// ApplyRecoveredDiffs applies diffs fetched during gossip recovery.
// Returns GossipSig for each successfully applied nonce.
func (h *Host) ApplyRecoveredDiffs(ctx context.Context, diffs []types.Diff) ([]gossip.GossipSig, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var sigs []gossip.GossipSig

	for _, diff := range diffs {
		if err := h.applyAndPersist(diff); err != nil {
			return sigs, fmt.Errorf("apply recovered diff nonce %d: %w", diff.Nonce, err)
		}

		// Sign state with acceptance check (same path as HandleRequest).
		stateSig, root, nonce, err := h.signIfAccepted(nil)
		if err != nil {
			return sigs, fmt.Errorf("sign recovered state: %w", err)
		}

		if stateSig != nil && h.store != nil {
			for slotID := range h.slotIDs {
				sigs = append(sigs, gossip.GossipSig{
					Nonce:     nonce,
					StateHash: root,
					Sig:       stateSig,
					SlotID:    slotID,
				})
			}
		}
	}

	return sigs, nil
}

// ChallengeReceipt is called by a verifying host to challenge the executor.
// It applies missing diffs, checks if this host is the executor for the given
// inference, verifies the payload fields, signs an executor receipt, and triggers
// async execution. Returns the receipt signature and confirmed_at timestamp,
// or nil if this host cannot produce a receipt (not executor, inference not pending, etc).
//
// On payload validation error, returns (nil, 0, nil) -- not an error, because the
// executor IS reachable. The verifier should already have caught bad payloads
// before forwarding (defense-in-depth).
func (h *Host) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, int64, error) {
	receipt, confirmedAt, job, err := h.challengeReceiptLocked(inferenceID, payload, diffs)
	if err != nil || job == nil {
		return receipt, confirmedAt, err
	}
	h.executeAsync(ctx, job)
	return receipt, confirmedAt, nil
}

// challengeReceiptLocked applies diffs, checks executor eligibility, and signs
// the receipt under the mutex. Returns a non-nil ExecuteRequest when async execution is needed.
func (h *Host) challengeReceiptLocked(inferenceID uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, int64, *subnet.ExecuteRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, diff := range diffs {
		if err := h.applyAndPersist(diff); err != nil {
			return nil, 0, nil, fmt.Errorf("apply challenge diff nonce %d: %w", diff.Nonce, err)
		}
	}

	rec, ok := h.sm.GetInference(inferenceID)
	if !ok || rec.Status != types.StatusPending {
		return nil, 0, nil, nil
	}
	if !h.slotIDs[rec.ExecutorSlot] {
		return nil, 0, nil, nil
	}
	if payload == nil {
		return nil, 0, nil, nil
	}
	if err := VerifyPayload(payload, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt); err != nil {
		return nil, 0, nil, nil
	}

	confirmedAt := time.Now().Unix()
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: inferenceID,
		PromptHash:  rec.PromptHash,
		Model:       rec.Model,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		StartedAt:   rec.StartedAt,
		EscrowId:    h.escrowID,
		ConfirmedAt: confirmedAt,
	}
	receiptData, err := proto.Marshal(receiptContent)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("marshal executor receipt: %w", err)
	}
	sig, err := h.signer.Sign(receiptData)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("sign executor receipt: %w", err)
	}

	// Dedup: return receipt (proves executor alive) but skip execution
	// if already in-flight or already finished in mempool.
	if _, dup := h.executing[inferenceID]; dup {
		return sig, confirmedAt, nil, nil
	}
	for _, tx := range h.mempool.Txs() {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return sig, confirmedAt, nil, nil
		}
	}

	h.executing[inferenceID] = struct{}{}

	job := &subnet.ExecuteRequest{
		InferenceID: inferenceID,
		Model:       rec.Model,
		Prompt:      payload.Prompt,
		PromptHash:  rec.PromptHash,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		EscrowID:    h.escrowID,
	}
	return sig, confirmedAt, job, nil
}

func (h *Host) LatestNonce() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sm.LatestNonce()
}

// LastFinalized returns the highest nonce marked as finalized (>2/3 sigs).
func (h *Host) LastFinalized() (uint64, error) {
	if h.store == nil {
		return 0, fmt.Errorf("no storage configured")
	}
	return h.store.LastFinalized(h.escrowID)
}

// checkFinalization checks if a nonce has enough sigs (>2/3) and marks it finalized.
func (h *Host) checkFinalization(nonce uint64) {
	if h.store == nil {
		return
	}
	sigs, err := h.store.GetSignatures(h.escrowID, nonce)
	if err != nil {
		return
	}
	threshold := 2*len(h.group)/3 + 1
	if len(sigs) >= threshold {
		if err := h.store.MarkFinalized(h.escrowID, nonce); err != nil {
			logging.Debug("mark finalized failed", "subsystem", "host", "nonce", nonce, "error", err)
			return
		}
		if h.gsp != nil {
			h.gsp.PruneBelow(nonce)
		}
	}
}

// GetSignatures returns accumulated signatures for a nonce from storage.
func (h *Host) GetSignatures(nonce uint64) (map[uint32][]byte, error) {
	if h.store == nil {
		return nil, fmt.Errorf("no storage configured")
	}
	return h.store.GetSignatures(h.escrowID, nonce)
}

// signState marshals StateSignatureContent{root, escrowID, nonce} and signs it.
func (h *Host) signState(nonce uint64, root []byte) ([]byte, error) {
	sigContent := &types.StateSignatureContent{
		StateRoot: root,
		EscrowId:  h.escrowID,
		Nonce:     nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	if err != nil {
		return nil, fmt.Errorf("marshal state sig content: %w", err)
	}
	return h.signer.Sign(sigData)
}

// signProposer marshals msg and signs it, returning the proposer signature.
func (h *Host) signProposer(msg proto.Message) ([]byte, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal proposer msg: %w", err)
	}
	return h.signer.Sign(data)
}

// VerifyPayload checks that an InferencePayload matches the expected on-chain fields.
// Used by both executor (signReceipt) and verifier (VerifyRefusedTimeout) paths.
func VerifyPayload(p *InferencePayload, promptHash []byte, model string, inputLength, maxTokens uint64, startedAt int64) error {
	hash, err := subnet.CanonicalPromptHash(p.Prompt)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrPromptHashMismatch, err)
	}
	if !bytes.Equal(hash, promptHash) {
		return types.ErrPromptHashMismatch
	}
	if p.InputLength != inputLength {
		return fmt.Errorf("%w: input_length %d vs %d", types.ErrPayloadMismatch, p.InputLength, inputLength)
	}
	if p.MaxTokens != maxTokens {
		return fmt.Errorf("%w: max_tokens %d vs %d", types.ErrPayloadMismatch, p.MaxTokens, maxTokens)
	}
	if p.StartedAt != startedAt {
		return fmt.Errorf("%w: started_at %d vs %d", types.ErrPayloadMismatch, p.StartedAt, startedAt)
	}
	if p.Model != model {
		return fmt.Errorf("%w: model %s vs %s", types.ErrPayloadMismatch, p.Model, model)
	}
	return nil
}
